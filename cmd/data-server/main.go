package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var (
	version = "dev"
	dataDir string
	addr    string
)

//go:embed ui.html
var uiHTML []byte

func init() {
	flag.StringVar(&dataDir, "data", "/data", "base directory to serve")
	flag.StringVar(&addr, "addr", "0.0.0.0:8080", "listen address")
}

func main() {
	flag.Parse()
	if os.Getenv("JOURNAL_STREAM") != "" {
		log.SetFlags(0)
	} else {
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	}
	log.Printf("data-server %s listening on %s (data: %s)", version, addr, dataDir)
	http.HandleFunc("/", handle)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func safePath(base, urlPath string) (string, bool) {
	result := filepath.Join(base, filepath.Clean("/"+urlPath))
	rel, err := filepath.Rel(base, result)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return result, true
}

func handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		handleGet(w, r)
	case http.MethodPut, http.MethodPost, http.MethodPatch:
		handleWrite(w, r)
	case http.MethodDelete:
		handleDelete(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(uiHTML)
			return
		}
		serveJSONListing(w)
		return
	}

	http.FileServer(http.Dir(dataDir)).ServeHTTP(w, r)
}

type fileEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir,omitempty"`
}

func serveJSONListing(w http.ResponseWriter) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		http.Error(w, "readdir failed", http.StatusInternalServerError)
		return
	}
	result := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, fileEntry{
			Name: e.Name(),
			Size: info.Size(),
			Dir:  e.IsDir(),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleWrite(w http.ResponseWriter, r *http.Request) {
	fpath, ok := safePath(dataDir, r.URL.Path)
	if !ok || fpath == dataDir {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
		http.Error(w, "mkdir failed", http.StatusInternalServerError)
		return
	}
	// CreateTemp avoids races when concurrent uploads target the same path.
	f, err := os.CreateTemp(filepath.Dir(fpath), ".upload-*")
	if err != nil {
		http.Error(w, "create failed", http.StatusInternalServerError)
		return
	}
	tmp := f.Name()
	if _, err := io.Copy(f, r.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		http.Error(w, "sync failed", http.StatusInternalServerError)
		return
	}
	f.Close()
	if err := os.Rename(tmp, fpath); err != nil {
		os.Remove(tmp)
		http.Error(w, "rename failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	fpath, ok := safePath(dataDir, r.URL.Path)
	if !ok || fpath == dataDir {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if err := os.Remove(fpath); err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "delete failed", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
