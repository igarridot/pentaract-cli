package app

import "testing"

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

func TestBuildTempUploadPath(t *testing.T) {
	got := buildTempUploadPath("session123", "backups", "videos/demo.mp4", 2)
	want := ".pentaract-cli-tmp/session123/backups/videos/demo.__pentaract_cli_session123_02.mp4"
	if got != want {
		t.Fatalf("buildTempUploadPath() = %q, want %q", got, want)
	}
}
