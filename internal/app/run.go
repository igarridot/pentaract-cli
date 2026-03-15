package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Dominux/pentaract-cli/internal/config"
	"github.com/Dominux/pentaract-cli/internal/pentaract"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "upload":
		return runUpload(ctx, args[1:], stdout, stderr)
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
		envFile = fs.String("env-file", ".env", "Ruta del fichero .env")
		storage = fs.String("storage", "", "Nombre o ID del storage")
		dest    = fs.String("dest", "", "Ruta destino dentro del storage")
		source  = fs.String("source", "", "Directorio origen dentro del contenedor")
		retries = fs.Int("retries", 0, "Numero maximo de intentos por fichero")
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

	if *source != "" {
		cfg.SourceDir = *source
	}
	if *retries > 0 {
		cfg.Retries = *retries
	}

	storageRef := firstNonEmpty(*storage, cfg.DefaultStorage)
	if storageRef == "" {
		return errors.New("debes indicar --storage o definir PENTARACT_STORAGE")
	}
	if cfg.BaseURL == "" || cfg.Email == "" || cfg.Password == "" {
		return errors.New("faltan credenciales: revisa PENTARACT_BASE_URL, PENTARACT_EMAIL y PENTARACT_PASSWORD")
	}

	client, err := pentaract.NewClient(cfg.BaseURL)
	if err != nil {
		return err
	}

	stats, err := scanSource(cfg.SourceDir)
	if err != nil {
		return err
	}
	if stats.Files == 0 {
		return fmt.Errorf("no hay ficheros regulares para subir en %s", cfg.SourceDir)
	}
	if len(stats.SkippedEntries) > 0 {
		fmt.Fprintf(stderr, "Aviso: se omiten %d entradas no compatibles (symlinks, dispositivos o especiales).\n", len(stats.SkippedEntries))
	}

	token, err := loginWithRetry(ctx, client, cfg.Email, cfg.Password, cfg.Retries, cfg.RetryDelay)
	if err != nil {
		return fmt.Errorf("login en Pentaract: %w", err)
	}

	storageID, storageName, err := resolveStorage(ctx, client, token, storageRef)
	if err != nil {
		return err
	}

	destRoot := cleanRemotePath(*dest)
	reporter := newReporter(stdout, stats.Files, stats.Bytes)
	planner := newRemoteNamePlanner(client, token, storageID)
	sessionID := newSessionID()

	fmt.Fprintf(stdout, "Subiendo %d archivo(s) a storage %q (%s) en %q desde %s\n", stats.Files, storageName, storageID, emptyFallback(destRoot, "/"), cfg.SourceDir)

	index := int64(0)
	err = walkSource(cfg.SourceDir, func(file sourceFile) error {
		index++
		desiredPath := joinRemotePath(destRoot, file.RelPath)
		finalPath, err := planner.ResolveAvailablePath(ctx, desiredPath)
		if err != nil {
			return err
		}

		reporter.startFile(index, file, finalPath, 1)

		var tempPath string
		var uploadCompleted bool
		var lastErr error

		for attempt := 1; attempt <= cfg.Retries; attempt++ {
			reporter.startFile(index, file, finalPath, attempt)
			tempPath = buildTempUploadPath(sessionID, destRoot, file.RelPath, attempt)
			uploadID := fmt.Sprintf("%s-%d-%d", sessionID, index, attempt)

			err := client.UploadFileWithProgress(ctx, pentaract.UploadInput{
				StorageID:      storageID,
				Token:          token,
				LocalPath:      file.AbsPath,
				RemotePath:     tempPath,
				RemoteFilename: remoteFilenameFromPath(tempPath),
				UploadID:       uploadID,
				OnProgress:     reporter.updateProgress,
			})
			if err == nil {
				uploadCompleted = true
				break
			}

			lastErr = err
			exists, existsErr := client.FileExists(context.WithoutCancel(ctx), token, storageID, tempPath)
			if existsErr == nil && exists {
				uploadCompleted = true
				break
			}

			if attempt == cfg.Retries || !pentaract.IsRetryable(err) {
				return fmt.Errorf("subiendo %s: %w", file.RelPath, err)
			}

			reporter.setStatus("retrying", fmt.Sprintf("reintento %d/%d tras error: %v", attempt, cfg.Retries, err))
			if err := sleepWithContext(ctx, cfg.RetryDelay); err != nil {
				return err
			}
		}

		if !uploadCompleted {
			if lastErr == nil {
				lastErr = errors.New("upload did not complete")
			}
			return fmt.Errorf("subiendo %s: %w", file.RelPath, lastErr)
		}

		reporter.setStatus("moving", "moviendo al destino final")
		for moveAttempt := 1; moveAttempt <= cfg.Retries; moveAttempt++ {
			err := client.Move(ctx, token, storageID, tempPath, finalPath)
			if err == nil {
				planner.RememberPath(finalPath)
				reporter.completeFile(file, finalPath)
				return nil
			}

			if moveAttempt == cfg.Retries || !pentaract.IsRetryable(err) {
				return fmt.Errorf("moviendo %s a %s: %w", tempPath, finalPath, err)
			}

			planner.Invalidate(pathDir(finalPath))
			finalPath, err = planner.ResolveAvailablePath(ctx, desiredPath)
			if err != nil {
				return err
			}
			reporter.setStatus("moving", fmt.Sprintf("reintentando move a %s", finalPath))
			if err := sleepWithContext(ctx, cfg.RetryDelay); err != nil {
				return err
			}
		}

		return nil
	}, nil)
	if err != nil {
		return err
	}

	reporter.finish()
	return nil
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
		return "", "", fmt.Errorf("listando storages: %w", err)
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
			return "", "", fmt.Errorf("el storage %q es ambiguo; usa el ID exacto", ref)
		}
		storageCopy := storage
		matched = &storageCopy
	}
	if matched != nil {
		return matched.ID, matched.Name, nil
	}

	return "", "", fmt.Errorf("storage %q no encontrado", ref)
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
	return errors.New("esta CLI debe ejecutarse dentro de un contenedor")
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Uso:")
	fmt.Fprintln(w, "  pentaract-cli upload --storage <id|nombre> --dest <ruta>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Opciones:")
	fmt.Fprintln(w, "  --env-file .env   Ruta del .env con credenciales")
	fmt.Fprintln(w, "  --source /source  Directorio origen dentro del contenedor")
	fmt.Fprintln(w, "  --retries 3       Reintentos por fichero")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newSessionID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func remoteFilenameFromPath(remotePath string) string {
	_, name := splitRemotePath(remotePath)
	return name
}

func pathDir(remotePath string) string {
	dir, _ := splitRemotePath(remotePath)
	return dir
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
