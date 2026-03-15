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

func TestBuildMultipartEnvelopeProducesValidBody(t *testing.T) {
	prefix, suffix, contentType, err := buildMultipartEnvelope("dir/sub", "remote.bin", "upload-9")
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
	if fields["file_name"] != "remote.bin" {
		t.Fatalf("file name = %q, want remote.bin", fields["file_name"])
	}
	if fields["file_body"] != "DATA" {
		t.Fatalf("file body = %q, want DATA", fields["file_body"])
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

func mimeParseMediaType(v string) (string, map[string]string, error) {
	header := http.Header{}
	header.Set("Content-Type", v)
	value := header.Get("Content-Type")
	return mimeParse(value)
}

func mimeParse(v string) (string, map[string]string, error) {
	return mime.ParseMediaType(v)
}
