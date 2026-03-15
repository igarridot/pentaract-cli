package app

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type sourceFile struct {
	AbsPath string
	RelPath string
	Size    int64
}

type sourceStats struct {
	Files          int64
	Bytes          int64
	SkippedEntries []string
}

func scanSource(sourceDir string) (sourceStats, error) {
	stats := sourceStats{}
	err := walkSource(sourceDir, func(file sourceFile) error {
		stats.Files++
		stats.Bytes += file.Size
		return nil
	}, func(skipped string) {
		stats.SkippedEntries = append(stats.SkippedEntries, skipped)
	})
	return stats, err
}

func walkSource(sourceDir string, onFile func(sourceFile) error, onSkip func(string)) error {
	info, err := fs.Stat(osDirFS{}, sourceDir)
	if err != nil {
		return fmt.Errorf("source dir %q: %w", sourceDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source path %q is not a directory", sourceDir)
	}

	return filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == sourceDir {
			return nil
		}
		if d.Name() == ".gitkeep" {
			if onSkip != nil {
				onSkip(path)
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if onSkip != nil {
				onSkip(path)
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			if onSkip != nil {
				onSkip(path)
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		return onFile(sourceFile{
			AbsPath: path,
			RelPath: filepath.ToSlash(rel),
			Size:    info.Size(),
		})
	})
}

type osDirFS struct{}

func (osDirFS) Open(name string) (fs.File, error) {
	return os.Open(name)
}
