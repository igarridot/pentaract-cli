package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanSourceSkipsGitkeep(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o600); err != nil {
		t.Fatalf("WriteFile .gitkeep error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "episode.mkv"), []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile episode.mkv error: %v", err)
	}

	stats, err := scanSource(dir)
	if err != nil {
		t.Fatalf("scanSource error: %v", err)
	}

	if stats.Files != 1 {
		t.Fatalf("files = %d, want 1", stats.Files)
	}
	if stats.Bytes != 4 {
		t.Fatalf("bytes = %d, want 4", stats.Bytes)
	}
}
