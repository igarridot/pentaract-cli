package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Dominux/pentaract-cli/internal/config"
	"github.com/Dominux/pentaract-cli/internal/pentaract"
)

const (
	defaultOutputDir = "/downloads"
	// Parallel file downloads — matches the WUI behavior where the server
	// handles per-file chunk parallelism internally; we overlap the
	// server-side decryption/download of one file with disk I/O of another.
	maxConcurrentDownloads = 2
)

type remoteFile struct {
	Path string
	Name string
	Size int64
}

func runDownload(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		envFile = fs.String("env-file", ".env", "Path to the .env file")
		storage = fs.String("storage", "", "Storage name or ID")
		src     = fs.String("src", "", "Remote path inside the storage to download")
		output  = fs.String("output", "", "Local output directory")
		retries = fs.Int("retries", 0, "Maximum retries per file")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	if err := ensureContainer(); err != nil {
		return err
	}

	cfg, err := config.Load(*envFile)
	if err != nil {
		return err
	}

	if *retries > 0 {
		cfg.Retries = *retries
	}

	storageRef := firstNonEmpty(*storage, cfg.DefaultStorage)
	if storageRef == "" {
		return errors.New("must specify --storage or set PENTARACT_STORAGE")
	}
	if cfg.BaseURL == "" || cfg.Email == "" || cfg.Password == "" {
		return errors.New("missing credentials: check PENTARACT_BASE_URL, PENTARACT_EMAIL, and PENTARACT_PASSWORD")
	}

	outputDir := firstNonEmpty(*output, defaultOutputDir)

	client, err := pentaract.NewClient(cfg.BaseURL)
	if err != nil {
		return err
	}

	token, err := loginWithRetry(ctx, client, cfg.Email, cfg.Password, cfg.Retries, cfg.RetryDelay)
	if err != nil {
		return fmt.Errorf("Pentaract login: %w", err)
	}

	storageID, storageName, err := resolveStorage(ctx, client, token, storageRef)
	if err != nil {
		return err
	}

	remotePath := cleanRemotePath(*src)

	// Walk the remote tree to discover all files
	fmt.Fprintf(stdout, "Listing files in storage %q (%s) at %q...\n", storageName, storageID, emptyFallback(remotePath, "/"))
	files, err := walkRemoteTree(ctx, client, token, storageID, remotePath)
	if err != nil {
		return fmt.Errorf("listing remote files: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no files found at remote path %q", emptyFallback(remotePath, "/"))
	}

	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
	}

	reporter := newDownloadReporter(stdout, int64(len(files)), totalBytes)
	fmt.Fprintf(stdout, "Downloading %d file(s) (%s) from storage %q to %s\n", len(files), humanBytes(totalBytes), storageName, outputDir)

	// Parallel pipelined downloads — overlap server-side decryption of one
	// file with disk writes of another, matching WUI concurrency.
	err = runPipelinedDownloads(ctx, files, maxConcurrentDownloads, func(ctx context.Context, index int64, file remoteFile) error {
		// Determine relative path for local filesystem
		relPath := file.Path
		if remotePath != "" {
			relPath = strings.TrimPrefix(file.Path, remotePath+"/")
			if relPath == file.Path {
				relPath = file.Name
			}
		}
		localPath := filepath.Join(outputDir, filepath.FromSlash(relPath))
		fileKey := fmt.Sprintf("%d:%s", index, file.Path)

		var lastErr error
		for attempt := 1; attempt <= cfg.Retries; attempt++ {
			reporter.startFile(fileKey, index, file, localPath, attempt)

			err := client.DownloadFile(ctx, pentaract.DownloadInput{
				StorageID:  storageID,
				Token:      token,
				RemotePath: file.Path,
				LocalPath:  localPath,
				OnProgress: func(bytesWritten int64) {
					reporter.updateBytes(fileKey, bytesWritten)
				},
			})
			if err == nil {
				reporter.completeFile(fileKey, file)
				return nil
			}

			lastErr = err
			if attempt == cfg.Retries || !pentaract.IsRetryable(err) {
				reporter.setStatus(fileKey, "error", fmt.Sprintf("ERROR: %v", err))
				reporter.removeFile(fileKey)
				return fmt.Errorf("downloading %s: %w", file.Path, err)
			}

			backoff := retryBackoff(cfg.RetryDelay, attempt)
			reporter.setStatus(fileKey, "retrying", fmt.Sprintf("retry %d/%d after error: %v", attempt, cfg.Retries, err))
			if err := sleepWithContext(ctx, backoff); err != nil {
				reporter.removeFile(fileKey)
				return err
			}
		}

		reporter.removeFile(fileKey)
		if lastErr == nil {
			lastErr = errors.New("download did not complete")
		}
		return fmt.Errorf("downloading %s: %w", file.Path, lastErr)
	})
	if err != nil {
		return err
	}

	reporter.finish()
	return nil
}

// runPipelinedDownloads processes files with bounded concurrency.
// On first error, cancels remaining downloads and returns that error.
func runPipelinedDownloads(ctx context.Context, files []remoteFile, maxConcurrent int, downloadFn func(ctx context.Context, index int64, file remoteFile) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

loop:
	for i, file := range files {
		select {
		case <-ctx.Done():
			break loop
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(index int64, f remoteFile) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := downloadFn(ctx, index, f); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				errMu.Unlock()
			}
		}(int64(i+1), file)
	}
	wg.Wait()
	return firstErr
}

// walkRemoteTree recursively lists all files under the given remote path.
func walkRemoteTree(ctx context.Context, client *pentaract.Client, token, storageID, remotePath string) ([]remoteFile, error) {
	items, err := client.ListDir(ctx, token, storageID, remotePath)
	if err != nil {
		return nil, err
	}

	var files []remoteFile
	for _, item := range items {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if item.IsFile {
			files = append(files, remoteFile{
				Path: item.Path,
				Name: item.Name,
				Size: item.Size,
			})
		} else {
			subFiles, err := walkRemoteTree(ctx, client, token, storageID, item.Path)
			if err != nil {
				return nil, err
			}
			files = append(files, subFiles...)
		}
	}
	return files, nil
}
