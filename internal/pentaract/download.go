package pentaract

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// DownloadInput holds parameters for a file download.
type DownloadInput struct {
	StorageID  string
	Token      string
	RemotePath string
	LocalPath  string
	OnProgress func(bytesWritten int64) // called periodically with cumulative bytes
}

// DownloadFile downloads a file from the storage and writes it to LocalPath,
// creating parent directories as needed. Streams directly to disk using a
// fixed 256KB buffer — never holds the full file in memory.
func (c *Client) DownloadFile(ctx context.Context, input DownloadInput) error {
	endpoint := c.apiBase + "/storages/" + url.PathEscape(input.StorageID) +
		"/files/download/" + url.PathEscape(cleanRemotePath(input.RemotePath)) +
		"?access_token=" + url.QueryEscape(input.Token)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp)
	}

	if err := os.MkdirAll(filepath.Dir(input.LocalPath), 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", filepath.Dir(input.LocalPath), err)
	}

	f, err := os.Create(input.LocalPath)
	if err != nil {
		return fmt.Errorf("creating file %s: %w", input.LocalPath, err)
	}

	var dest io.Writer = f
	if input.OnProgress != nil {
		dest = &countingWriter{w: f, onProgress: input.OnProgress}
	}

	// Use a fixed 256KB buffer to avoid allocating a large buffer per download.
	buf := make([]byte, 256*1024)
	_, copyErr := io.CopyBuffer(dest, resp.Body, buf)
	closeErr := f.Close()

	if copyErr != nil {
		os.Remove(input.LocalPath)
		return fmt.Errorf("writing file %s: %w", input.LocalPath, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing file %s: %w", input.LocalPath, closeErr)
	}

	return nil
}

// countingWriter wraps an io.Writer and reports cumulative bytes written.
type countingWriter struct {
	w          io.Writer
	written    int64
	onProgress func(int64)
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if n > 0 {
		cw.written += int64(n)
		cw.onProgress(cw.written)
	}
	return n, err
}
