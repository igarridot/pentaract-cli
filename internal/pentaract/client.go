package pentaract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const progressRetryDelay = time.Second

var errUploadTrackerNotFound = errors.New("upload progress tracker not found")

type Client struct {
	apiBase string
	http    *http.Client
}

type UploadHandle struct {
	requestErrCh chan error
	watchErrCh   chan error

	requestOnce sync.Once
	requestErr  error

	watchOnce sync.Once
	watchErr  error
}

func NewClient(rawBaseURL string) (*Client, error) {
	apiBase, err := normalizeAPIBase(rawBaseURL)
	if err != nil {
		return nil, err
	}

	return &Client{
		apiBase: apiBase,
		http:    &http.Client{},
	}, nil
}

func normalizeAPIBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("PENTARACT_BASE_URL is required")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid PENTARACT_BASE_URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("PENTARACT_BASE_URL must include scheme and host")
	}

	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/api") {
		u.Path += "/api"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (c *Client) Login(ctx context.Context, email, password string) (string, error) {
	reqBody := map[string]string{
		"email":    email,
		"password": password,
	}

	var resp LoginResponse
	if err := c.doJSON(ctx, http.MethodPost, c.apiBase+"/auth/login", "", reqBody, &resp); err != nil {
		return "", err
	}
	return resp.AccessToken, nil
}

func (c *Client) ListStorages(ctx context.Context, token string) ([]Storage, error) {
	var resp []Storage
	if err := c.doJSON(ctx, http.MethodGet, c.apiBase+"/storages", token, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) ListDir(ctx context.Context, token, storageID, dir string) ([]FSElement, error) {
	endpoint := c.apiBase + "/storages/" + url.PathEscape(storageID) + "/files/tree/"
	if cleanRemotePath(dir) != "" {
		endpoint += url.PathEscape(cleanRemotePath(dir))
	}

	var resp []FSElement
	if err := c.doJSON(ctx, http.MethodGet, endpoint, token, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) Move(ctx context.Context, token, storageID, oldPath, newPath string) error {
	reqBody := map[string]string{
		"old_path": oldPath,
		"new_path": newPath,
	}

	return c.doJSON(ctx, http.MethodPost, c.apiBase+"/storages/"+url.PathEscape(storageID)+"/files/move", token, reqBody, nil)
}

func (c *Client) CancelUpload(ctx context.Context, token, uploadID string) error {
	return c.doJSON(ctx, http.MethodPost, c.apiBase+"/upload_cancel/"+url.PathEscape(uploadID), token, nil, nil)
}

func (c *Client) FileExists(ctx context.Context, token, storageID, fullPath string) (bool, error) {
	dir, name := splitRemotePath(fullPath)
	items, err := c.ListDir(ctx, token, storageID, dir)
	if err != nil {
		return false, err
	}

	for _, item := range items {
		if item.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) UploadFileWithProgress(ctx context.Context, input UploadInput) error {
	handle, err := c.StartUpload(ctx, input)
	if err != nil {
		return err
	}
	return handle.Wait()
}

func (h *UploadHandle) WaitForRequest() error {
	if h == nil {
		return nil
	}
	h.requestOnce.Do(func() {
		h.requestErr = <-h.requestErrCh
	})
	return h.requestErr
}

func (h *UploadHandle) waitForProgress() error {
	if h == nil {
		return nil
	}
	h.watchOnce.Do(func() {
		h.watchErr = <-h.watchErrCh
	})
	return h.watchErr
}

func (h *UploadHandle) Wait() error {
	postErr := h.WaitForRequest()
	watchErr := h.waitForProgress()

	switch {
	case postErr == nil && watchErr == nil:
		return nil
	case postErr == nil:
		return watchErr
	case watchErr == nil:
		return nil
	case errors.Is(watchErr, errUploadTrackerNotFound):
		return postErr
	default:
		return errors.Join(postErr, watchErr)
	}
}

func (c *Client) StartUpload(ctx context.Context, input UploadInput) (*UploadHandle, error) {
	if input.UploadID == "" {
		return nil, errors.New("upload id is required")
	}
	if input.RemotePath == "" {
		return nil, errors.New("remote path is required")
	}
	remotePath := cleanRemotePath(input.RemotePath)
	remoteFilename := input.RemoteFilename
	if strings.TrimSpace(remoteFilename) == "" {
		_, remoteFilename = splitRemotePath(remotePath)
	}

	requestDone := make(chan struct{})
	handle := &UploadHandle{
		requestErrCh: make(chan error, 1),
		watchErrCh:   make(chan error, 1),
	}

	go func() {
		handle.watchErrCh <- c.watchUploadProgress(ctx, input.Token, input.UploadID, requestDone, input.OnProgress)
	}()

	go func() {
		postErr := c.postUpload(ctx, input.Token, input.StorageID, input.LocalPath, remotePath, remoteFilename, input.UploadID)
		close(requestDone)
		handle.requestErrCh <- postErr
	}()

	return handle, nil
}

func (c *Client) postUpload(ctx context.Context, token, storageID, localPath, remotePath, remoteFilename, uploadID string) error {
	fileInfo, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", localPath, err)
	}
	if !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", localPath)
	}

	parentDir, _ := splitRemotePath(remotePath)
	prefix, suffix, contentType, err := buildMultipartEnvelope(parentDir, remoteFilename, uploadID)
	if err != nil {
		return err
	}

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer file.Close()

	body := io.MultiReader(bytes.NewReader(prefix), file, bytes.NewReader(suffix))
	contentLength := int64(len(prefix)) + fileInfo.Size() + int64(len(suffix))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/storages/"+url.PathEscape(storageID)+"/files/upload", body)
	if err != nil {
		return err
	}
	req.ContentLength = contentLength
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return parseAPIError(resp)
	}

	// The backend flushes the 202 response as soon as it has registered the
	// upload, but only returns from the handler after the multipart body has been
	// fully consumed. We must therefore read to EOF here; otherwise the Go client
	// can stop sending the remaining body bytes and leave the file record without
	// any persisted chunks.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}

	var accepted UploadAccepted
	if err := json.Unmarshal(respBody, &accepted); err != nil {
		return err
	}
	return nil
}

func buildMultipartEnvelope(parentDir, remoteFilename, uploadID string) (prefix, suffix []byte, contentType string, err error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	if err := writer.WriteField("path", parentDir); err != nil {
		return nil, nil, "", err
	}
	if err := writer.WriteField("upload_id", uploadID); err != nil {
		return nil, nil, "", err
	}
	if err := writer.WriteField("on_conflict", "keep_both"); err != nil {
		return nil, nil, "", err
	}
	if _, err := writer.CreateFormFile("file", remoteFilename); err != nil {
		return nil, nil, "", err
	}

	contentType = writer.FormDataContentType()
	prefixLen := buf.Len()
	if err := writer.Close(); err != nil {
		return nil, nil, "", err
	}

	all := buf.Bytes()
	prefix = append([]byte(nil), all[:prefixLen]...)
	suffix = append([]byte(nil), all[prefixLen:]...)
	return prefix, suffix, contentType, nil
}

func (c *Client) watchUploadProgress(ctx context.Context, token, uploadID string, requestDone <-chan struct{}, onProgress func(UploadProgress)) error {
	requestFinished := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-requestDone:
			requestFinished = true
			requestDone = nil
		default:
		}

		progress, terminal, err := c.consumeUploadProgressOnce(ctx, token, uploadID, onProgress)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := sleepWithContext(ctx, progressRetryDelay); err != nil {
				return err
			}
			continue
		}

		if !terminal {
			if err := sleepWithContext(ctx, progressRetryDelay); err != nil {
				return err
			}
			continue
		}

		if progress.Status == "done" && !progress.HasMetrics {
			if requestFinished {
				return errUploadTrackerNotFound
			}
			if err := sleepWithContext(ctx, progressRetryDelay); err != nil {
				return err
			}
			continue
		}

		switch progress.Status {
		case "done":
			return nil
		case "error", "cancelled", "skipped":
			return &UploadFailedError{Status: progress.Status}
		default:
			if requestFinished {
				return fmt.Errorf("unexpected terminal upload status %q", progress.Status)
			}
		}
	}
}

func (c *Client) consumeUploadProgressOnce(ctx context.Context, token, uploadID string, onProgress func(UploadProgress)) (UploadProgress, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/upload_progress?upload_id="+url.QueryEscape(uploadID), nil)
	if err != nil {
		return UploadProgress{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return UploadProgress{}, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return UploadProgress{}, false, parseAPIError(resp)
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return UploadProgress{}, false, nil
			}
			return UploadProgress{}, false, err
		}

		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		progress, err := parseUploadProgress(line[6:])
		if err != nil {
			continue
		}
		if onProgress != nil {
			onProgress(progress)
		}
		if isTerminalStatus(progress.Status) {
			return progress, true, nil
		}
	}
}

func parseUploadProgress(raw string) (UploadProgress, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return UploadProgress{}, err
	}

	var progress UploadProgress
	if err := decodeField(payload, "status", &progress.Status); err != nil {
		return UploadProgress{}, err
	}
	_ = decodeField(payload, "total", &progress.Total)
	_ = decodeField(payload, "uploaded", &progress.Uploaded)
	_ = decodeField(payload, "total_bytes", &progress.TotalBytes)
	_ = decodeField(payload, "uploaded_bytes", &progress.UploadedBytes)
	_ = decodeField(payload, "verification_total", &progress.VerificationTotal)
	_ = decodeField(payload, "verified", &progress.Verified)
	_ = decodeField(payload, "workers_status", &progress.WorkersStatus)
	progress.HasMetrics = len(payload) > 1
	return progress, nil
}

func decodeField(payload map[string]json.RawMessage, key string, dst any) error {
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func (c *Client) doJSON(ctx context.Context, method, endpoint, token string, reqBody any, respBody any) error {
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp)
	}
	if respBody == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return decodeJSONBody(resp.Body, respBody)
}

func decodeJSONBody(r io.Reader, dst any) error {
	decoder := json.NewDecoder(r)
	return decoder.Decode(dst)
}

func parseAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var apiBody apiErrorBody
	if err := json.Unmarshal(body, &apiBody); err == nil && strings.TrimSpace(apiBody.Error) != "" {
		return &APIError{StatusCode: resp.StatusCode, Message: apiBody.Error}
	}
	return &APIError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("request failed with status %d", resp.StatusCode)}
}

func splitRemotePath(fullPath string) (dir, name string) {
	fullPath = cleanRemotePath(fullPath)
	dir, name = path.Split(fullPath)
	dir = strings.TrimSuffix(dir, "/")
	return dir, name
}

func cleanRemotePath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	p = strings.Trim(p, "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return p
}

func isTerminalStatus(status string) bool {
	switch status {
	case "done", "error", "cancelled", "skipped":
		return true
	default:
		return false
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= http.StatusInternalServerError
	}

	var uploadErr *UploadFailedError
	if errors.As(err, &uploadErr) {
		return uploadErr.Status != "cancelled"
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	return true
}

func LocalRelativePath(sourceDir, absPath string) (string, error) {
	rel, err := filepath.Rel(sourceDir, absPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
