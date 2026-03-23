package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewNotifier_NilWhenMissingCredentials(t *testing.T) {
	if NewNotifier("", "") != nil {
		t.Fatal("expected nil notifier when both token and chatID are empty")
	}
	if NewNotifier("token", "") != nil {
		t.Fatal("expected nil notifier when chatID is empty")
	}
	if NewNotifier("", "123") != nil {
		t.Fatal("expected nil notifier when token is empty")
	}
}

func TestNewNotifier_NotNilWithCredentials(t *testing.T) {
	n := NewNotifier("mytoken", "123456")
	if n == nil {
		t.Fatal("expected non-nil notifier with valid credentials")
	}
}

func TestSend_NilNotifier(t *testing.T) {
	var n *Notifier
	if err := n.Send(context.Background(), "test"); err != nil {
		t.Fatalf("nil notifier Send should be a no-op, got error: %v", err)
	}
}

func TestSend_PostsCorrectPayload(t *testing.T) {
	var received sendMessageRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n := &Notifier{
		token:  "testtoken",
		chatID: "42",
		client: srv.Client(),
	}
	// Override the URL by using a custom transport that redirects to the test server
	n.client.Transport = rewriteTransport(srv.URL)

	if err := n.Send(context.Background(), "Upload failed: data.csv"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if received.ChatID != "42" {
		t.Errorf("chat_id = %q, want 42", received.ChatID)
	}
	if received.Text != "Upload failed: data.csv" {
		t.Errorf("text = %q, want Upload failed: data.csv", received.Text)
	}
}

func TestSend_ReturnsErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	n := &Notifier{
		token:  "badtoken",
		chatID: "42",
		client: srv.Client(),
	}
	n.client.Transport = rewriteTransport(srv.URL)

	err := n.Send(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q does not mention status code", err.Error())
	}
}

func TestSend_ReturnsErrorOnNetworkFailure(t *testing.T) {
	n := &Notifier{
		token:  "token",
		chatID: "42",
		client: &http.Client{},
	}
	// Point at a non-existent server
	n.client.Transport = rewriteTransport("http://127.0.0.1:1")

	err := n.Send(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for network failure, got nil")
	}
}

// rewriteTransport rewrites all requests to the given base URL (preserving path).
type rewriteTransport string

func (base rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme = "http"
	r2.URL.Host = strings.TrimPrefix(string(base), "http://")
	return http.DefaultTransport.RoundTrip(r2)
}
