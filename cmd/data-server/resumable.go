package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/librescoot/data-server/client"
)

const maxUploadChunk = 8 << 20

var resumableUploadMu sync.Mutex

type uploadMetadata struct {
	ID     string `json:"id"`
	Length int64  `json:"length"`
}

func handleResumableHead(w http.ResponseWriter, r *http.Request) {
	fpath, id, length, ok := resumableRequest(r, w)
	if !ok {
		return
	}
	resumableUploadMu.Lock()
	defer resumableUploadMu.Unlock()

	part, meta := uploadPaths(fpath)
	if _, err := os.Stat(part); os.IsNotExist(err) {
		if info, err := os.Stat(fpath); err == nil && info.Mode().IsRegular() && info.Size() == length {
			if digest, err := hashFile(fpath); err == nil && digest == id {
				setUploadState(w, length, length, true)
				w.WriteHeader(http.StatusOK)
				return
			}
		}
	}
	state, err := readUploadMetadata(meta)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "invalid upload metadata", http.StatusConflict)
		return
	}
	if os.IsNotExist(err) {
		if _, statErr := os.Stat(part); statErr == nil {
			_ = os.Remove(part)
		}
		setUploadState(w, 0, length, false)
		w.WriteHeader(http.StatusOK)
		return
	}
	if state.ID != id || state.Length != length {
		http.Error(w, "another upload is incomplete for this path", http.StatusConflict)
		return
	}
	offset := int64(0)
	if info, err := os.Stat(part); err == nil {
		offset = info.Size()
	}
	if offset > length {
		_ = os.Remove(part)
		_ = os.Remove(meta)
		offset = 0
	}
	if offset == length {
		if digest, err := hashFile(part); err == nil && digest == id {
			if err := os.Rename(part, fpath); err != nil {
				http.Error(w, "finalize upload failed", http.StatusInternalServerError)
				return
			}
			_ = os.Remove(meta)
			syncParent(fpath)
			setUploadState(w, length, length, true)
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = os.Remove(part)
		_ = os.Remove(meta)
		offset = 0
	}
	setUploadState(w, offset, length, false)
	w.WriteHeader(http.StatusOK)
}

func handleResumablePut(w http.ResponseWriter, r *http.Request) {
	fpath, id, length, ok := resumableRequest(r, w)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get(client.HeaderUploadOffset), 10, 64)
	if err != nil || offset < 0 || offset > length || r.ContentLength > maxUploadChunk || (r.ContentLength >= 0 && offset+r.ContentLength > length) {
		http.Error(w, "invalid upload offset or chunk size", http.StatusBadRequest)
		return
	}
	if length == 0 && (offset != 0 || r.ContentLength > 0) {
		http.Error(w, "invalid empty-file upload", http.StatusBadRequest)
		return
	}
	if r.ContentLength >= 0 && offset+r.ContentLength > length {
		http.Error(w, "chunk exceeds upload length", http.StatusBadRequest)
		return
	}

	resumableUploadMu.Lock()
	defer resumableUploadMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
		http.Error(w, "mkdir failed", http.StatusInternalServerError)
		return
	}
	part, meta := uploadPaths(fpath)
	state, err := readUploadMetadata(meta)
	if os.IsNotExist(err) {
		if offset != 0 {
			setUploadState(w, 0, length, false)
			http.Error(w, "upload offset conflict", http.StatusConflict)
			return
		}
		if err := writeUploadMetadata(meta, uploadMetadata{ID: id, Length: length}); err != nil {
			http.Error(w, "creating upload metadata failed", http.StatusInternalServerError)
			return
		}
		state = uploadMetadata{ID: id, Length: length}
	} else if err != nil {
		http.Error(w, "invalid upload metadata", http.StatusConflict)
		return
	}
	if state.ID != id || state.Length != length {
		http.Error(w, "another upload is incomplete for this path", http.StatusConflict)
		return
	}
	current := int64(0)
	if info, err := os.Stat(part); err == nil {
		current = info.Size()
	} else if !os.IsNotExist(err) {
		http.Error(w, "checking upload failed", http.StatusInternalServerError)
		return
	}
	if current != offset {
		setUploadState(w, current, length, false)
		http.Error(w, "upload offset conflict", http.StatusConflict)
		return
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		http.Error(w, "opening upload failed", http.StatusInternalServerError)
		return
	}
	limit := int64(maxUploadChunk)
	if remain := length - offset; remain < limit {
		limit = remain
	}
	written, readErr := io.Copy(f, io.LimitReader(r.Body, limit+1))
	overflow := written > limit
	if overflow {
		_ = f.Truncate(current + limit)
		written = limit
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil || closeErr != nil {
		http.Error(w, "writing upload failed", http.StatusInternalServerError)
		return
	}
	current += written
	if overflow || readErr != nil || (r.ContentLength >= 0 && written != r.ContentLength) {
		setUploadState(w, current, length, false)
		http.Error(w, "incomplete upload chunk", http.StatusBadRequest)
		return
	}
	if current == length {
		digest, err := hashFile(part)
		if err != nil || digest != id {
			_ = os.Remove(part)
			_ = os.Remove(meta)
			http.Error(w, "upload checksum mismatch", http.StatusUnprocessableEntity)
			return
		}
		if err := os.Rename(part, fpath); err != nil {
			http.Error(w, "finalizing upload failed", http.StatusInternalServerError)
			return
		}
		_ = os.Remove(meta)
		syncParent(fpath)
		setUploadState(w, length, length, true)
		w.WriteHeader(http.StatusCreated)
		return
	}
	setUploadState(w, current, length, false)
	w.WriteHeader(http.StatusPermanentRedirect)
}

func resumableRequest(r *http.Request, w http.ResponseWriter) (string, string, int64, bool) {
	fpath, ok := safePath(dataDir, r.URL.Path)
	if !ok || fpath == dataDir {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return "", "", 0, false
	}
	id := strings.ToLower(r.Header.Get(client.HeaderUploadID))
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != sha256.Size {
		http.Error(w, "invalid upload ID", http.StatusBadRequest)
		return "", "", 0, false
	}
	length, err := strconv.ParseInt(r.Header.Get(client.HeaderUploadLength), 10, 64)
	if err != nil || length < 0 {
		http.Error(w, "invalid upload length", http.StatusBadRequest)
		return "", "", 0, false
	}
	return fpath, id, length, true
}

func setUploadState(w http.ResponseWriter, offset, length int64, complete bool) {
	w.Header().Set(client.HeaderUploadOffset, strconv.FormatInt(offset, 10))
	w.Header().Set(client.HeaderUploadLength, strconv.FormatInt(length, 10))
	if complete {
		w.Header().Set(client.HeaderUploadComplete, "true")
	}
}

func uploadPaths(target string) (string, string) {
	part := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".librescoot-upload")
	return part, part + ".json"
}

func readUploadMetadata(path string) (uploadMetadata, error) {
	var metadata uploadMetadata
	data, err := os.ReadFile(path)
	if err != nil {
		return metadata, err
	}
	err = json.Unmarshal(data, &metadata)
	return metadata, err
}

func writeUploadMetadata(path string, metadata uploadMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncParent(path)
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func syncParent(path string) {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return
	}
	_ = dir.Sync()
	_ = dir.Close()
}
