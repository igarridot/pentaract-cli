package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dominux/pentaract-cli/internal/config"
	"github.com/Dominux/pentaract-cli/internal/pentaract"
	"github.com/Dominux/pentaract-cli/internal/telegram"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "upload":
		return runUpload(ctx, args[1:], stdout, stderr)
	case "download":
		return runDownload(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stdout)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runUpload(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		envFile    = fs.String("env-file", ".env", "Path to the .env file")
		storage    = fs.String("storage", "", "Storage name or ID")
		dest       = fs.String("dest", "", "Destination path inside the storage")
		source     = fs.String("source", "", "Source directory inside the container")
		retries    = fs.Int("retries", 0, "Maximum retries per file")
		onConflict = fs.String("on-conflict", "keep_both", "Conflict mode: skip (skip files matching name, path, and size) or keep_both (rename with suffix)")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	conflictMode := *onConflict
	if conflictMode != "skip" && conflictMode != "keep_both" {
		return fmt.Errorf("invalid --on-conflict value %q: must be skip or keep_both", conflictMode)
	}

	if err := ensureContainer(); err != nil {
		return err
	}

	cfg, err := config.Load(*envFile)
	if err != nil {
		return err
	}

	if *source != "" {
		cfg.SourceDir = *source
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

	notifier := telegram.NewNotifier(cfg.TelegramBotToken, cfg.TelegramChatID)

	client, err := pentaract.NewClient(cfg.BaseURL)
	if err != nil {
		return err
	}

	stats, err := scanSource(cfg.SourceDir)
	if err != nil {
		return err
	}
	if stats.Files == 0 {
		return fmt.Errorf("no regular files to upload in %s", cfg.SourceDir)
	}
	if len(stats.SkippedEntries) > 0 {
		fmt.Fprintf(stderr, "Warning: skipping %d unsupported entries (symlinks, devices, or special files).\n", len(stats.SkippedEntries))
	}

	token, err := loginWithRetry(ctx, client, cfg.Email, cfg.Password, cfg.Retries, cfg.RetryDelay)
	if err != nil {
		return fmt.Errorf("Pentaract login: %w", err)
	}

	storageID, storageName, err := resolveStorage(ctx, client, token, storageRef)
	if err != nil {
		return err
	}

	destRoot := cleanRemotePath(*dest)
	reporter := newReporter(stdout, stats.Files, stats.Bytes)
	planner := newRemoteNamePlanner(client, token, storageID)

	// C4: pre-warm directory cache in parallel
	var files []sourceFile
	err = walkSource(cfg.SourceDir, func(file sourceFile) error {
		files = append(files, file)
		return nil
	}, nil)
	if err != nil {
		return err
	}

	planner.PrewarmDirs(ctx, destRoot, files)

	fmt.Fprintf(stdout, "Uploading %d file(s) to storage %q (%s) at %q from %s\n", stats.Files, storageName, storageID, emptyFallback(destRoot, "/"), cfg.SourceDir)

	var skippedFiles int64
	var fileFailureNotified atomic.Bool

	// C1: pipeline uploads — start next file while previous is verifying.
	// Concurrency of 2 overlaps verification of file N with upload of file N+1.
	const maxConcurrentUploads = 2
	err = runPipelinedUploads(ctx, files, maxConcurrentUploads, func(ctx context.Context, index int64, file sourceFile) error {
		desiredPath := joinRemotePath(destRoot, file.RelPath)

		// In skip mode, check if the file already exists with the same size
		if conflictMode == "skip" {
			exists, err := planner.ExistsWithSize(ctx, desiredPath, file.Size)
			if err != nil {
				return err
			}
			if exists {
				skippedFiles++
				reporter.skipFile(file.Size)
				fmt.Fprintf(stdout, "[%d/%d] %s — skipped (already exists with same size)\n", index, stats.Files, file.RelPath)
				return nil
			}
		}

		finalPath, err := planner.ResolveAvailablePath(ctx, desiredPath)
		if err != nil {
			return err
		}

		_, name := splitRemotePath(finalPath)
		fileKey := fmt.Sprintf("%d:%s", index, file.RelPath)

		var lastErr error
		for attempt := 1; attempt <= cfg.Retries; attempt++ {
			reporter.startFile(fileKey, index, file, finalPath, attempt)
			uploadID := fmt.Sprintf("%d-%d", index, attempt)

			err := client.UploadFileWithProgress(ctx, pentaract.UploadInput{
				StorageID:      storageID,
				Token:          token,
				LocalPath:      file.AbsPath,
				RemotePath:     finalPath,
				RemoteFilename: name,
				UploadID:       uploadID,
				FileSize:       file.Size,
				OnConflict:     conflictMode,
				OnProgress: func(p pentaract.UploadProgress) {
					reporter.updateProgress(fileKey, p)
				},
			})
			if err == nil {
				planner.RememberPath(finalPath, file.Size)
				reporter.completeFile(fileKey, file, finalPath)
				return nil
			}

			// If the upload was interrupted by cancellation (Ctrl+C),
			// tell the backend to cancel the upload and clean up partial
			// chunks from Telegram.
			if ctx.Err() != nil {
				cancelCtx, cancelFn := context.WithTimeout(context.Background(), 10*time.Second)
				_ = client.CancelUpload(cancelCtx, token, uploadID)
				cancelFn()
				reporter.removeFile(fileKey)
				return ctx.Err()
			}

			lastErr = err
			exists, existsErr := client.FileExists(context.WithoutCancel(ctx), token, storageID, finalPath)
			if existsErr == nil && exists {
				planner.RememberPath(finalPath, file.Size)
				reporter.completeFile(fileKey, file, finalPath)
				return nil
			}

			if attempt == cfg.Retries || !pentaract.IsRetryable(err) {
				reporter.removeFile(fileKey)
				fileFailureNotified.Store(true)
				if notifyErr := notifier.Send(ctx, fmt.Sprintf("Upload failed for file: %s\nError: %v", file.RelPath, err)); notifyErr != nil {
					fmt.Fprintf(stderr, "Warning: failed to send Telegram notification: %v\n", notifyErr)
				}
				return fmt.Errorf("uploading %s: %w", file.RelPath, err)
			}

			// C2: exponential backoff with jitter
			backoff := retryBackoff(cfg.RetryDelay, attempt)
			reporter.setStatus(fileKey, "retrying", fmt.Sprintf("retry %d/%d after error: %v", attempt, cfg.Retries, err))
			if err := sleepWithContext(ctx, backoff); err != nil {
				reporter.removeFile(fileKey)
				return err
			}
		}

		reporter.removeFile(fileKey)
		if lastErr == nil {
			lastErr = errors.New("upload did not complete")
		}
		return fmt.Errorf("uploading %s: %w", file.RelPath, lastErr)
	})
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(stderr, "\nUpload cancelled. Partial uploads have been cleaned up on the server.\n")
		} else if !fileFailureNotified.Load() {
			// Overall process failure not caused by a specific file — notify once.
			if notifyErr := notifier.Send(ctx, fmt.Sprintf("Upload process failed: %v", err)); notifyErr != nil {
				fmt.Fprintf(stderr, "Warning: failed to send Telegram notification: %v\n", notifyErr)
			}
		}
		return err
	}

	reporter.finish()
	if skippedFiles > 0 {
		fmt.Fprintf(stdout, "Skipped %d file(s) (already exist with same size)\n", skippedFiles)
	}
	return nil
}

// C1: runPipelinedUploads processes files with bounded concurrency.
// On first error, cancels remaining uploads and returns that error.
func runPipelinedUploads(ctx context.Context, files []sourceFile, maxConcurrent int, uploadFn func(ctx context.Context, index int64, file sourceFile) error) error {
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
		go func(index int64, f sourceFile) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := uploadFn(ctx, index, f); err != nil {
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

// C2: retryBackoff calculates exponential backoff with jitter.
// baseDelay * 2^(attempt-1) + random jitter up to 25% of the delay.
func retryBackoff(baseDelay time.Duration, attempt int) time.Duration {
	delay := baseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	// Add jitter: 0-25% of the delay
	jitter := time.Duration(rand.Int64N(int64(delay) / 4))
	return delay + jitter
}

func loginWithRetry(ctx context.Context, client *pentaract.Client, email, password string, retries int, delay time.Duration) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		token, err := client.Login(ctx, email, password)
		if err == nil {
			return token, nil
		}
		lastErr = err
		if attempt == retries || !pentaract.IsRetryable(err) {
			break
		}
		if err := sleepWithContext(ctx, delay); err != nil {
			return "", err
		}
	}
	return "", lastErr
}

func resolveStorage(ctx context.Context, client *pentaract.Client, token, ref string) (string, string, error) {
	storages, err := client.ListStorages(ctx, token)
	if err != nil {
		return "", "", fmt.Errorf("listing storages: %w", err)
	}

	ref = strings.TrimSpace(ref)
	for _, storage := range storages {
		if storage.ID == ref || storage.Name == ref {
			return storage.ID, storage.Name, nil
		}
	}

	lowerRef := strings.ToLower(ref)
	var matched *pentaract.Storage
	for _, storage := range storages {
		if strings.ToLower(storage.Name) != lowerRef {
			continue
		}
		if matched != nil {
			return "", "", fmt.Errorf("storage %q is ambiguous; use the exact ID", ref)
		}
		storageCopy := storage
		matched = &storageCopy
	}
	if matched != nil {
		return matched.ID, matched.Name, nil
	}

	return "", "", fmt.Errorf("storage %q not found", ref)
}

func ensureContainer() error {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return nil
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		text := string(data)
		if strings.Contains(text, "docker") || strings.Contains(text, "containerd") || strings.Contains(text, "kubepods") || strings.Contains(text, "podman") {
			return nil
		}
	}
	return errors.New("this CLI must run inside a container")
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pentaract-cli upload   --storage <id|name> --dest <path>")
	fmt.Fprintln(w, "  pentaract-cli download --storage <id|name> --src <path>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Upload options:")
	fmt.Fprintln(w, "  --env-file .env   Path to .env with credentials")
	fmt.Fprintln(w, "  --source /source  Source directory inside the container")
	fmt.Fprintln(w, "  --dest <path>     Destination path inside the storage")
	fmt.Fprintln(w, "  --on-conflict     skip or keep_both (default: keep_both)")
	fmt.Fprintln(w, "  --retries 3       Retries per file")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Download options:")
	fmt.Fprintln(w, "  --env-file .env       Path to .env with credentials")
	fmt.Fprintln(w, "  --src <path>          Remote path inside the storage to download")
	fmt.Fprintln(w, "  --output /downloads   Local output directory")
	fmt.Fprintln(w, "  --retries 3           Retries per file")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
