// Package client contains the shared HTTP client for uploads to data-server.
package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	HeaderUploadID       = "X-Librescoot-Upload-ID"
	HeaderUploadLength   = "X-Librescoot-Upload-Length"
	HeaderUploadOffset   = "X-Librescoot-Upload-Offset"
	HeaderUploadComplete = "X-Librescoot-Upload-Complete"
	DefaultChunkSize     = int64(4 << 20)
)

// Options configures bounded per-request retries without imposing a total
// upload deadline. Context cancellation remains available to the caller.
type Options struct {
	HTTPClient     *http.Client
	ChunkSize      int64
	RequestTimeout time.Duration
	MaxRetries     int
	RetryBackoff   time.Duration
}

// UploadFile uploads path to targetURL. A target data-server resumes at its
// last durable byte offset; older servers receive one atomic full-file PUT.
func UploadFile(ctx context.Context, targetURL, path string, options Options) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("upload source is not a regular file: %s", path)
	}
	chunkSize := options.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize > 8<<20 {
		return fmt.Errorf("upload chunk size %d exceeds the 8 MiB limit", chunkSize)
	}
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	maxRetries := options.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 6
	}
	backoff := options.RetryBackoff
	if backoff <= 0 {
		backoff = 250 * time.Millisecond
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash upload source: %w", err)
	}
	id := hex.EncodeToString(h.Sum(nil))
	total := info.Size()

	var offset int64
	var supported bool
	for attempt := 0; ; attempt++ {
		offset, supported, err = queryOffset(ctx, client, targetURL, id, total, options.RequestTimeout)
		if err == nil {
			break
		}
		if attempt >= maxRetries {
			return fmt.Errorf("checking resumable upload state after %d retries: %w", maxRetries, err)
		}
		if err := pause(ctx, backoff*time.Duration(attempt+1)); err != nil {
			return err
		}
	}
	if !supported {
		return putLegacy(ctx, client, targetURL, f, total)
	}
	for offset < total || (total == 0 && offset == 0) {
		chunk := chunkSize
		if remain := total - offset; remain < chunk {
			chunk = remain
		}
		attempt := 0
		for {
			section := io.NewSectionReader(f, offset, chunk)
			req, cancel, err := newRequest(ctx, options.RequestTimeout, http.MethodPut, targetURL, section)
			if err != nil {
				return err
			}
			req.ContentLength = chunk
			req.Header.Set("Content-Type", "application/octet-stream")
			req.Header.Set(HeaderUploadID, id)
			req.Header.Set(HeaderUploadLength, strconv.FormatInt(total, 10))
			req.Header.Set(HeaderUploadOffset, strconv.FormatInt(offset, 10))
			resp, requestErr := client.Do(req)
			if requestErr == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				cancel()
				if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusPermanentRedirect {
					if next, ok := parseOffset(resp, total); ok && next > offset {
						offset = next
						break
					}
					if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
						return nil
					}
				}
				if resp.StatusCode < 500 && resp.StatusCode != http.StatusConflict {
					return fmt.Errorf("resumable PUT %s: HTTP %d", targetURL, resp.StatusCode)
				}
			} else {
				cancel()
			}
			attempt++
			if attempt > maxRetries {
				if requestErr != nil {
					return fmt.Errorf("resumable PUT %s after %d retries: %w", targetURL, maxRetries, requestErr)
				}
				return fmt.Errorf("resumable PUT %s after %d retries: server returned an error", targetURL, maxRetries)
			}
			if err := pause(ctx, backoff*time.Duration(attempt)); err != nil {
				return err
			}
			next, _, err := queryOffset(ctx, client, targetURL, id, total, options.RequestTimeout)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				continue
			}
			if next > total {
				return fmt.Errorf("server upload offset %d exceeds file size %d", next, total)
			}
			if next == total {
				return nil
			}
			offset = next
		}
	}
	return nil
}

func queryOffset(ctx context.Context, client *http.Client, targetURL, id string, total int64, timeout time.Duration) (int64, bool, error) {
	req, cancel, err := newRequest(ctx, timeout, http.MethodHead, targetURL, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set(HeaderUploadID, id)
	req.Header.Set(HeaderUploadLength, strconv.FormatInt(total, 10))
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return 0, false, err
	}
	defer func() {
		resp.Body.Close()
		cancel()
	}()
	if resp.Header.Get(HeaderUploadOffset) == "" {
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("resumable HEAD %s: HTTP %d", targetURL, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, true, fmt.Errorf("resumable HEAD %s: HTTP %d", targetURL, resp.StatusCode)
	}
	offset, ok := parseOffset(resp, total)
	if !ok {
		return 0, true, fmt.Errorf("resumable HEAD %s returned an invalid offset", targetURL)
	}
	if resp.Header.Get(HeaderUploadComplete) == "true" && offset == total {
		return total, true, nil
	}
	return offset, true, nil
}

func parseOffset(resp *http.Response, total int64) (int64, bool) {
	offset, err := strconv.ParseInt(resp.Header.Get(HeaderUploadOffset), 10, 64)
	return offset, err == nil && offset >= 0 && offset <= total
}

func putLegacy(ctx context.Context, client *http.Client, targetURL string, f *os.File, total int64) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	req, cancel, err := newRequest(ctx, 0, http.MethodPut, targetURL, f)
	if err != nil {
		return err
	}
	req.ContentLength = int64(total)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return err
	}
	defer func() {
		resp.Body.Close()
		cancel()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("PUT %s: HTTP %d", targetURL, resp.StatusCode)
	}
	return nil
}

func newRequest(ctx context.Context, timeout time.Duration, method, targetURL string, body io.Reader) (*http.Request, context.CancelFunc, error) {
	var requestCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		requestCtx, cancel = context.WithCancel(ctx)
	}
	req, err := http.NewRequestWithContext(requestCtx, method, targetURL, body)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return req, cancel, nil
}

func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
