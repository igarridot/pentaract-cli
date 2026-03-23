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

func TestStartFileAssignsSequentialIndex(t *testing.T) {
	r := newReporter(io.Discard, 3, 300)

	file1 := sourceFile{RelPath: "a.txt", Size: 100}
	file2 := sourceFile{RelPath: "b.txt", Size: 100}

	r.startFile("key1", 1, file1, "/dest/a.txt", 1)
	r.startFile("key2", 2, file2, "/dest/b.txt", 1)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active["key1"].Index != 1 {
		t.Fatalf("key1 index = %d, want 1", r.active["key1"].Index)
	}
	if r.active["key2"].Index != 2 {
		t.Fatalf("key2 index = %d, want 2", r.active["key2"].Index)
	}
}

func TestStartFileRetryReusesIndex(t *testing.T) {
	r := newReporter(io.Discard, 2, 200)

	file := sourceFile{RelPath: "a.txt", Size: 100}
	r.startFile("key1", 1, file, "/dest/a.txt", 1)
	r.startFile("key1", 1, file, "/dest/a.txt", 2) // retry

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nextIndex != 1 {
		t.Fatalf("nextIndex = %d after retry, want 1 (no increment on retry)", r.nextIndex)
	}
	if r.active["key1"].Index != 1 {
		t.Fatalf("key1 index after retry = %d, want 1", r.active["key1"].Index)
	}
}

func TestStartFileSkipsDoNotAffectSequence(t *testing.T) {
	// Simulate: 5 files total, 2 skipped before any startFile call.
	r := newReporter(io.Discard, 5, 500)
	r.skipFile(100)
	r.skipFile(100)

	file := sourceFile{RelPath: "c.txt", Size: 100}
	r.startFile("key3", 3, file, "/dest/c.txt", 1)

	r.mu.Lock()
	defer r.mu.Unlock()
	// totalFiles reduced to 3; first uploaded file gets index 1, not 3.
	if r.totalFiles != 3 {
		t.Fatalf("totalFiles = %d, want 3", r.totalFiles)
	}
	if r.active["key3"].Index != 1 {
		t.Fatalf("key3 index = %d, want 1 (first non-skipped file)", r.active["key3"].Index)
	}
}
