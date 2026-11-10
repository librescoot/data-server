package client

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadFileFallsBackForOlderServer(t *testing.T) {
	content := []byte("legacy upload")
	source := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			http.NotFound(w, r)
		case http.MethodPut:
			var err error
			uploaded, err = io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("reading legacy PUT: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	if err := UploadFile(context.Background(), server.URL+"/file.bin", source, Options{}); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if !bytes.Equal(uploaded, content) {
		t.Fatalf("legacy PUT = %q, want %q", uploaded, content)
	}
}

func TestUploadFileCanCreateEmptyFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "empty")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var target string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set(HeaderUploadOffset, "0")
			w.Header().Set(HeaderUploadLength, "0")
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPut {
			body, err := io.ReadAll(r.Body)
			if err != nil || len(body) != 0 {
				t.Errorf("empty PUT body = %v, err=%v", body, err)
			}
			target = "created"
			w.Header().Set(HeaderUploadOffset, "0")
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	defer server.Close()

	if err := UploadFile(context.Background(), server.URL+"/empty", source, Options{}); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if target != "created" {
		t.Fatal("empty file was not uploaded")
	}
}
