package app

import (
	"io"
	"testing"
)

func TestSkipFileReducesTotals(t *testing.T) {
	r := newReporter(io.Discard, 5, 1000)

	r.skipFile(200)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.totalFiles != 4 {
		t.Fatalf("totalFiles = %d, want 4", r.totalFiles)
	}
	if r.totalBytes != 800 {
		t.Fatalf("totalBytes = %d, want 800", r.totalBytes)
	}
}

func TestSkipFileMultipleConcurrent(t *testing.T) {
	r := newReporter(io.Discard, 10, 2000)

	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		go func() {
			r.skipFile(100)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 5; i++ {
		<-done
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.totalFiles != 5 {
		t.Fatalf("totalFiles = %d, want 5 after 5 concurrent skips", r.totalFiles)
	}
	if r.totalBytes != 1500 {
		t.Fatalf("totalBytes = %d, want 1500 after 5 concurrent skips of 100 bytes each", r.totalBytes)
	}
}
