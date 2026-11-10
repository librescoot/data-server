package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/librescoot/data-server/client"
)

func TestRootBrowserGetsHTML(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	rr := httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("want text/html content-type, got %q", ct)
	}
	if !strings.Contains(rr.Body.String(), "<html") {
		t.Fatalf("response body is not HTML")
	}
}

func TestRootProgrammaticGetsJSON(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)

	for _, accept := range []string{"application/json", "*/*", ""} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rr := httptest.NewRecorder()
		handle(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("Accept %q: want 200, got %d", accept, rr.Code)
		}
		ct := rr.Header().Get("Content-Type")
		if !strings.Contains(ct, "application/json") {
			t.Fatalf("Accept %q: want application/json, got %q", accept, ct)
		}
		if !strings.Contains(rr.Body.String(), "a.txt") {
			t.Fatalf("Accept %q: JSON listing missing a.txt", accept)
		}
	}
}

func TestUploadPutAndDownload(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	content := []byte("hello data-server")

	req := httptest.NewRequest(http.MethodPut, "/testfile.bin", bytes.NewReader(content))
	rr := httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: want 200, got %d", rr.Code)
	}

	data, err := os.ReadFile(filepath.Join(dir, "testfile.bin"))
	if err != nil {
		t.Fatalf("file not on disk: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("content mismatch on disk")
	}

	req = httptest.NewRequest(http.MethodGet, "/testfile.bin", nil)
	rr = httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d", rr.Code)
	}
	got, _ := io.ReadAll(rr.Body)
	if !bytes.Equal(got, content) {
		t.Fatalf("download content mismatch")
	}
}

func TestSharedClientResumesAfterLostChunkResponse(t *testing.T) {
	dataDir = t.TempDir()
	content := bytes.Repeat([]byte("ota-image-"), 20_000)
	source := filepath.Join(t.TempDir(), "artifact.mender")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var putOffsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putOffsets = append(putOffsets, r.Header.Get(client.HeaderUploadOffset))
			if len(putOffsets) == 1 {
				rr := httptest.NewRecorder()
				handle(rr, r)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		handle(w, r)
	}))
	defer server.Close()

	err := client.UploadFile(context.Background(), server.URL+"/ota/dbc/artifact.mender", source, client.Options{
		ChunkSize:    64 << 10,
		RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if len(putOffsets) < 2 || putOffsets[0] != "0" || putOffsets[1] != strconv.FormatInt(64<<10, 10) {
		t.Fatalf("PUT offsets = %v, expected retry to resume at %d", putOffsets, 64<<10)
	}
	got, err := os.ReadFile(filepath.Join(dataDir, "ota", "dbc", "artifact.mender"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("uploaded content mismatch: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dataDir, "ota", "dbc", ".artifact.mender.librescoot-upload*")); len(matches) != 0 {
		t.Fatalf("resumable upload left temporary files: %v", matches)
	}
	completedPuts := len(putOffsets)
	if err := client.UploadFile(context.Background(), server.URL+"/ota/dbc/artifact.mender", source, client.Options{}); err != nil {
		t.Fatalf("repeat UploadFile: %v", err)
	}
	if len(putOffsets) != completedPuts {
		t.Fatalf("identical completed file was uploaded again: PUT offsets %v", putOffsets[completedPuts:])
	}
}

func TestInterruptedChunkLeavesDurableResumeOffset(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	content := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	digest := sha256.Sum256(content)
	id := hex.EncodeToString(digest[:])

	req := httptest.NewRequest(http.MethodPut, "/partial.bin", bytes.NewReader(content[:12]))
	req.ContentLength = int64(len(content))
	req.Header.Set(client.HeaderUploadID, id)
	req.Header.Set(client.HeaderUploadLength, strconv.Itoa(len(content)))
	req.Header.Set(client.HeaderUploadOffset, "0")
	rr := httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("interrupted PUT: want 400, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodHead, "/partial.bin", nil)
	req.Header.Set(client.HeaderUploadID, id)
	req.Header.Set(client.HeaderUploadLength, strconv.Itoa(len(content)))
	rr = httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusOK || rr.Header().Get(client.HeaderUploadOffset) != "12" {
		t.Fatalf("resume HEAD: status=%d offset=%q", rr.Code, rr.Header().Get(client.HeaderUploadOffset))
	}
	part, _ := uploadPaths(filepath.Join(dir, "partial.bin"))
	got, err := os.ReadFile(part)
	if err != nil || !bytes.Equal(got, content[:12]) {
		t.Fatalf("partial upload = %q, err=%v", got, err)
	}

	source := filepath.Join(t.TempDir(), "partial.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(handle))
	defer server.Close()
	if err := client.UploadFile(context.Background(), server.URL+"/partial.bin", source, client.Options{ChunkSize: 10}); err != nil {
		t.Fatalf("resume UploadFile: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "partial.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("resumed content = %q, err=%v", got, err)
	}
}

func TestUploadPostAndPatch(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir

	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		req := httptest.NewRequest(method, "/via-"+strings.ToLower(method)+".txt", bytes.NewReader([]byte(method)))
		rr := httptest.NewRecorder()
		handle(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d", method, rr.Code)
		}
	}
}

func TestUploadCreatesSubdirs(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	req := httptest.NewRequest(http.MethodPut, "/maps/tiles.mbtiles", bytes.NewReader([]byte("tiles")))
	rr := httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "maps", "tiles.mbtiles")); err != nil {
		t.Fatalf("file not created in subdir: %v", err)
	}
}

func TestPathTraversalStaysInsideBase(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir

	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	os.WriteFile(secret, []byte("top secret"), 0644)
	t.Cleanup(func() { os.Remove(secret) })

	for _, urlPath := range []string{"/../secret.txt", "/foo/../../secret.txt"} {
		req := httptest.NewRequest(http.MethodGet, urlPath, nil)
		rr := httptest.NewRecorder()
		handle(rr, req)
		if rr.Code == http.StatusOK && bytes.Contains(rr.Body.Bytes(), []byte("top secret")) {
			t.Fatalf("path %q: traversal succeeded", urlPath)
		}
	}
}

func TestDeleteFile(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	fpath := filepath.Join(dir, "todelete.txt")
	os.WriteFile(fpath, []byte("bye"), 0644)

	req := httptest.NewRequest(http.MethodDelete, "/todelete.txt", nil)
	rr := httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rr.Code)
	}
	if _, err := os.Stat(fpath); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted")
	}
}

func TestDeleteMissingFile(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	req := httptest.NewRequest(http.MethodDelete, "/nonexistent.txt", nil)
	rr := httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
}

func TestWriteToBaseDirRejected(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodPatch} {
		req := httptest.NewRequest(method, "/", bytes.NewReader([]byte("x")))
		rr := httptest.NewRecorder()
		handle(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s /: want 400, got %d", method, rr.Code)
		}
	}
}

func TestDeleteBaseDirRejected(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	rr := httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("DELETE /: want 400, got %d", rr.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	req := httptest.NewRequest(http.MethodConnect, "/foo.txt", nil)
	rr := httptest.NewRecorder()
	handle(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rr.Code)
	}
}

func checkServerHeader(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	hdr := rr.Header().Get("Server")
	if !strings.HasPrefix(hdr, "librescoot-data-server/") {
		t.Fatalf("Server header: want prefix %q, got %q", "librescoot-data-server/", hdr)
	}
}

func TestServerHeader(t *testing.T) {
	dir := t.TempDir()
	dataDir = dir
	handler := serverHeader(handle)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)
	checkServerHeader(t, rr)

	req = httptest.NewRequest(http.MethodPut, "/somefile", bytes.NewReader([]byte("x")))
	rr = httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: want 200, got %d", rr.Code)
	}
	checkServerHeader(t, rr)

	req = httptest.NewRequest(http.MethodDelete, "/somefile", nil)
	rr = httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE: want 204, got %d", rr.Code)
	}
	checkServerHeader(t, rr)

	req = httptest.NewRequest(http.MethodOptions, "/", nil)
	rr = httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("OPTIONS: want 405, got %d", rr.Code)
	}
	checkServerHeader(t, rr)
}
