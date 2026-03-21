package app

import (
	"context"
	"testing"

	"github.com/Dominux/pentaract-cli/internal/pentaract"
)

type fakeDirLister struct {
	items map[string][]pentaract.FSElement
}

func (f *fakeDirLister) ListDir(_ context.Context, _, _, dir string) ([]pentaract.FSElement, error) {
	return f.items[dir], nil
}

func TestAddCopySuffix(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		index    int
		want     string
	}{
		{name: "with extension", filename: "movie.mkv", index: 2, want: "movie (2).mkv"},
		{name: "without extension", filename: "archive", index: 1, want: "archive (1)"},
		{name: "hidden file", filename: ".env", index: 3, want: ".env (3)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addCopySuffix(tt.filename, tt.index); got != tt.want {
				t.Fatalf("addCopySuffix(%q, %d) = %q, want %q", tt.filename, tt.index, got, tt.want)
			}
		})
	}
}

func TestExistsWithSizeMatchesNameAndSize(t *testing.T) {
	lister := &fakeDirLister{
		items: map[string][]pentaract.FSElement{
			"docs": {
				{Name: "readme.txt", Size: 100, IsFile: true},
				{Name: "notes.txt", Size: 200, IsFile: true},
			},
		},
	}
	planner := newRemoteNamePlanner(lister, "tok", "s1")

	// Exact match
	exists, err := planner.ExistsWithSize(context.Background(), "docs/readme.txt", 100)
	if err != nil {
		t.Fatalf("ExistsWithSize error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true for matching name and size")
	}

	// Same name, different size
	exists, err = planner.ExistsWithSize(context.Background(), "docs/readme.txt", 999)
	if err != nil {
		t.Fatalf("ExistsWithSize error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for different size")
	}

	// File not present
	exists, err = planner.ExistsWithSize(context.Background(), "docs/missing.txt", 100)
	if err != nil {
		t.Fatalf("ExistsWithSize error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for missing file")
	}
}

func TestResolveAvailablePathRenamesOnConflict(t *testing.T) {
	lister := &fakeDirLister{
		items: map[string][]pentaract.FSElement{
			"photos": {
				{Name: "img.jpg", Size: 500, IsFile: true},
			},
		},
	}
	planner := newRemoteNamePlanner(lister, "tok", "s1")

	// No conflict
	got, err := planner.ResolveAvailablePath(context.Background(), "photos/new.jpg")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got != "photos/new.jpg" {
		t.Fatalf("got %q, want photos/new.jpg", got)
	}

	// Conflict — should rename
	got, err = planner.ResolveAvailablePath(context.Background(), "photos/img.jpg")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got != "photos/img (1).jpg" {
		t.Fatalf("got %q, want photos/img (1).jpg", got)
	}
}

func TestRememberPathUpdatesCache(t *testing.T) {
	lister := &fakeDirLister{
		items: map[string][]pentaract.FSElement{
			"dir": {},
		},
	}
	planner := newRemoteNamePlanner(lister, "tok", "s1")

	// Initially file doesn't exist
	exists, _ := planner.ExistsWithSize(context.Background(), "dir/file.txt", 42)
	if exists {
		t.Fatal("expected not exists before RememberPath")
	}

	planner.RememberPath("dir/file.txt", 42)

	// Now it should exist
	exists, _ = planner.ExistsWithSize(context.Background(), "dir/file.txt", 42)
	if !exists {
		t.Fatal("expected exists after RememberPath")
	}
}

