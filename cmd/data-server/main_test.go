package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GET / with browser Accept → HTML UI
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

// GET / without text/html Accept → JSON listing
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
