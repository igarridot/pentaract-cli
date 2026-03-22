package pentaract

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUploadFileWithProgressStreamsMultipartAndCompletes(t *testing.T) {
	var (
		mu               sync.Mutex
		receivedPath     string
		receivedUploadID string
		receivedFilename string
		receivedBody     string
		progressCalls    int
	)

	trackerReady := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/storages/storage-1/files/upload":
			reader, err := r.MultipartReader()
			if err != nil {
				t.Fatalf("MultipartReader error: %v", err)
			}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("NextPart error: %v", err)
				}
				data, _ := io.ReadAll(part)
				switch part.FormName() {
				case "path":
					receivedPath = string(data)
				case "upload_id":
					receivedUploadID = string(data)
				case "file":
					receivedFilename = part.FileName()
					receivedBody = string(data)
				}
			}
			close(trackerReady)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"upload_id":"upload-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/upload_progress":
			select {
			case <-trackerReady:
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out waiting for upload")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"status\":\"uploading\",\"total\":1,\"uploaded\":1,\"total_bytes\":11,\"uploaded_bytes\":11,\"verification_total\":1,\"verified\":0,\"workers_status\":\"active\"}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(10 * time.Millisecond)
			_, _ = io.WriteString(w, "data: {\"status\":\"done\",\"total\":1,\"uploaded\":1,\"total_bytes\":11,\"uploaded_bytes\":11,\"verification_total\":1,\"verified\":1,\"workers_status\":\"active\"}\n\n")
		default:
			http.NotFound(w, r)
		}
		mu.Lock()
		progressCalls++
		mu.Unlock()
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	dir := t.TempDir()
	localPath := filepath.Join(dir, "local.txt")
	if err := os.WriteFile(localPath, []byte("hello world"), 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	err = client.UploadFileWithProgress(context.Background(), UploadInput{
		StorageID:      "storage-1",
		Token:          "token",
		LocalPath:      localPath,
		RemotePath:     "dest/tmp.txt",
		RemoteFilename: "tmp.txt",
		UploadID:       "upload-1",
	})
	if err != nil {
		t.Fatalf("UploadFileWithProgress error: %v", err)
	}

	if receivedPath != "dest" {
		t.Fatalf("received path = %q, want dest", receivedPath)
	}
	if receivedUploadID != "upload-1" {
		t.Fatalf("received upload id = %q, want upload-1", receivedUploadID)
	}
	if receivedFilename != "tmp.txt" {
		t.Fatalf("received filename = %q, want tmp.txt", receivedFilename)
	}
	if receivedBody != "hello world" {
		t.Fatalf("received body = %q, want hello world", receivedBody)
	}
}

func TestStartUploadSeparatesRequestCompletionFromTerminalVerification(t *testing.T) {
	trackerReady := make(chan struct{})
	allowDone := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/storages/storage-1/files/upload":
			reader, err := r.MultipartReader()
			if err != nil {
				t.Fatalf("MultipartReader error: %v", err)
			}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("NextPart error: %v", err)
				}
				_, _ = io.Copy(io.Discard, part)
			}
			close(trackerReady)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"upload_id":"upload-2"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/upload_progress":
			select {
			case <-trackerReady:
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out waiting for upload")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"status\":\"verifying\",\"total\":1,\"uploaded\":1,\"total_bytes\":11,\"uploaded_bytes\":11,\"verification_total\":1,\"verified\":0,\"workers_status\":\"active\"}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-allowDone
			_, _ = io.WriteString(w, "data: {\"status\":\"done\",\"total\":1,\"uploaded\":1,\"total_bytes\":11,\"uploaded_bytes\":11,\"verification_total\":1,\"verified\":1,\"workers_status\":\"active\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	dir := t.TempDir()
	localPath := filepath.Join(dir, "local.txt")
	if err := os.WriteFile(localPath, []byte("hello world"), 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	handle, err := client.StartUpload(context.Background(), UploadInput{
		StorageID:      "storage-1",
		Token:          "token",
		LocalPath:      localPath,
		RemotePath:     "dest/tmp.txt",
		RemoteFilename: "tmp.txt",
		UploadID:       "upload-2",
	})
	if err != nil {
		t.Fatalf("StartUpload error: %v", err)
	}

	if err := handle.WaitForRequest(); err != nil {
		t.Fatalf("WaitForRequest error: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- handle.Wait()
	}()

	select {
	case err := <-waitDone:
		t.Fatalf("Wait returned too early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(allowDone)

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Wait did not return after terminal progress")
	}
}

func TestBuildMultipartEnvelopeProducesValidBody(t *testing.T) {
	prefix, suffix, contentType, err := buildMultipartEnvelope("dir/sub", "remote.bin", "upload-9", "keep_both", 4)
	if err != nil {
		t.Fatalf("buildMultipartEnvelope error: %v", err)
	}

	payload := append([]byte{}, prefix...)
	payload = append(payload, []byte("DATA")...)
	payload = append(payload, suffix...)

	mediaType, params, err := mimeParseMediaType(contentType)
	if err != nil {
		t.Fatalf("mimeParseMediaType error: %v", err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("media type = %q, want multipart/form-data", mediaType)
	}

	reader := multipart.NewReader(strings.NewReader(string(payload)), params["boundary"])
	fields := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart error: %v", err)
		}
		data, _ := io.ReadAll(part)
		if part.FormName() == "file" {
			fields["file_name"] = part.FileName()
			fields["file_body"] = string(data)
			continue
		}
		fields[part.FormName()] = string(data)
	}

	if fields["path"] != "dir/sub" {
		t.Fatalf("path = %q, want dir/sub", fields["path"])
	}
	if fields["upload_id"] != "upload-9" {
		t.Fatalf("upload_id = %q, want upload-9", fields["upload_id"])
	}
	if fields["on_conflict"] != "keep_both" {
		t.Fatalf("on_conflict = %q, want keep_both", fields["on_conflict"])
	}
	if fields["file_size"] != "4" {
		t.Fatalf("file_size = %q, want 4", fields["file_size"])
	}
	if fields["file_name"] != "remote.bin" {
		t.Fatalf("file name = %q, want remote.bin", fields["file_name"])
	}
	if fields["file_body"] != "DATA" {
		t.Fatalf("file body = %q, want DATA", fields["file_body"])
	}
}

func TestBuildMultipartEnvelopePassesOnConflictSkip(t *testing.T) {
	prefix, suffix, contentType, err := buildMultipartEnvelope("dir", "file.txt", "u1", "skip", 1024)
	if err != nil {
		t.Fatalf("buildMultipartEnvelope error: %v", err)
	}

	payload := append([]byte{}, prefix...)
	payload = append(payload, []byte("DATA")...)
	payload = append(payload, suffix...)

	_, params, err := mimeParseMediaType(contentType)
	if err != nil {
		t.Fatalf("mimeParseMediaType error: %v", err)
	}

	reader := multipart.NewReader(strings.NewReader(string(payload)), params["boundary"])
	fields := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart error: %v", err)
		}
		data, _ := io.ReadAll(part)
		fields[part.FormName()] = string(data)
	}

	if fields["on_conflict"] != "skip" {
		t.Fatalf("on_conflict = %q, want skip", fields["on_conflict"])
	}
}

func TestParseUploadProgressDetectsSentinel(t *testing.T) {
	progress, err := parseUploadProgress(`{"status":"done"}`)
	if err != nil {
		t.Fatalf("parseUploadProgress error: %v", err)
	}
	if progress.HasMetrics {
		t.Fatalf("HasMetrics = true, want false")
	}

	progress, err = parseUploadProgress(`{"status":"done","total":0,"uploaded":0,"workers_status":"active"}`)
	if err != nil {
		t.Fatalf("parseUploadProgress error: %v", err)
	}
	if !progress.HasMetrics {
		t.Fatalf("HasMetrics = false, want true")
	}
}

func TestPostUploadWaitsUntilServerConsumesMultipartBody(t *testing.T) {
	const fileSize = 3 * 1024 * 1024

	var receivedBytes atomic.Int64
	consumedBody := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader error: %v", err)
		}

		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart error: %v", err)
			}

			if part.FormName() != "file" {
				_, _ = io.Copy(io.Discard, part)
				continue
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"upload_id":"u1"}`))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}

			buf := make([]byte, 32*1024)
			for {
				n, err := part.Read(buf)
				if n > 0 {
					receivedBytes.Add(int64(n))
					time.Sleep(250 * time.Microsecond)
				}
				if err == io.EOF {
					close(consumedBody)
					return
				}
				if err != nil {
					t.Fatalf("part.Read error: %v", err)
				}
			}
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	dir := t.TempDir()
	localPath := filepath.Join(dir, "large.bin")
	if err := os.WriteFile(localPath, []byte(strings.Repeat("a", fileSize)), 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	if err := client.postUpload(context.Background(), "token", "storage-1", localPath, "dest/large.bin", "large.bin", "u1", "keep_both"); err != nil {
		t.Fatalf("postUpload error: %v", err)
	}

	select {
	case <-consumedBody:
	case <-time.After(2 * time.Second):
		t.Fatalf("postUpload returned before the server finished consuming the request body")
	}

	if got := receivedBytes.Load(); got != fileSize {
		t.Fatalf("received bytes = %d, want %d", got, fileSize)
	}
}

func TestCancelUploadSendsCorrectRequest(t *testing.T) {
	var cancelCalled atomic.Bool
	var receivedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/upload_cancel/upload-42" {
			cancelCalled.Store(true)
			receivedAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	if err := client.CancelUpload(context.Background(), "my-token", "upload-42"); err != nil {
		t.Fatalf("CancelUpload error: %v", err)
	}

	if !cancelCalled.Load() {
		t.Fatal("CancelUpload did not hit the cancel endpoint")
	}
	if receivedAuth != "Bearer my-token" {
		t.Fatalf("Authorization = %q, want Bearer my-token", receivedAuth)
	}
}

func TestCancelUploadWorksWithFreshContextAfterParentCancelled(t *testing.T) {
	var cancelCalled atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/upload_cancel/") {
			cancelCalled.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	// Simulate: parent context is already cancelled (Ctrl+C happened)
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// CancelUpload with the cancelled context should fail
	err = client.CancelUpload(cancelledCtx, "token", "upload-1")
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}

	// But with a fresh context (as the CLI does), it should succeed
	freshCtx, freshCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer freshCancel()

	err = client.CancelUpload(freshCtx, "token", "upload-1")
	if err != nil {
		t.Fatalf("CancelUpload with fresh context: %v", err)
	}

	if !cancelCalled.Load() {
		t.Fatal("CancelUpload with fresh context did not reach the server")
	}
}

func TestUploadFileWithProgressReturnsCancelledOnContextCancel(t *testing.T) {
	uploadStarted := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/storages/s1/files/upload":
			close(uploadStarted)
			// Block until the client disconnects
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/api/upload_progress":
			// Return non-terminal status so the watcher keeps polling
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"status\":\"uploading\",\"total\":1,\"uploaded\":0}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	dir := t.TempDir()
	localPath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(localPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.UploadFileWithProgress(ctx, UploadInput{
			StorageID:      "s1",
			Token:          "token",
			LocalPath:      localPath,
			RemotePath:     "dest/file.txt",
			RemoteFilename: "file.txt",
			UploadID:       "u1",
		})
	}()

	// Wait for the upload to start, then cancel
	select {
	case <-uploadStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not start in time")
	}

	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error after context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UploadFileWithProgress did not return after context cancellation")
	}
}

func mimeParseMediaType(v string) (string, map[string]string, error) {
	header := http.Header{}
	header.Set("Content-Type", v)
	value := header.Get("Content-Type")
	return mimeParse(value)
}

func mimeParse(v string) (string, map[string]string, error) {
	return mime.ParseMediaType(v)
}
