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

type activeUpload struct {
	fileKey     string
	index       int64
	file        sourceFile
	desiredPath string
	finalPath   string
	tempPath    string
	attempt     int
	uploadID    string
	handle      *pentaract.UploadHandle
}

type uploadCoordinator struct {
	ctx        context.Context
	client     *pentaract.Client
	reporter   *reporter
	planner    *remoteNamePlanner
	storageID  string
	token      string
	destRoot   string
	sessionID  string
	retries    int
	retryDelay time.Duration
}

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
	coordinator := &uploadCoordinator{
		ctx:        ctx,
		client:     client,
		reporter:   reporter,
		planner:    planner,
		storageID:  storageID,
		token:      token,
		destRoot:   destRoot,
		sessionID:  sessionID,
		retries:    cfg.Retries,
		retryDelay: cfg.RetryDelay,
	}

	fmt.Fprintf(stdout, "Subiendo %d archivo(s) a storage %q (%s) en %q desde %s\n", stats.Files, storageName, storageID, emptyFallback(destRoot, "/"), cfg.SourceDir)

	var verifyingUpload *activeUpload
	var uploadingUpload *activeUpload
	index := int64(0)
	err = walkSource(cfg.SourceDir, func(file sourceFile) error {
		index++
		if verifyingUpload != nil {
			if err := coordinator.finishUpload(verifyingUpload); err != nil {
				cancelUploads(context.WithoutCancel(ctx), client, token, reporter, uploadingUpload)
				return err
			}
			verifyingUpload = nil
		}

		next, err := coordinator.launchUpload(file, index)
		if err != nil {
			cancelUploads(context.WithoutCancel(ctx), client, token, reporter, uploadingUpload)
			return err
		}
		verifyingUpload = uploadingUpload
		uploadingUpload = next
		return nil
	}, nil)
	if err != nil {
		return err
	}

	if verifyingUpload != nil {
		if err := coordinator.finishUpload(verifyingUpload); err != nil {
			cancelUploads(context.WithoutCancel(ctx), client, token, reporter, uploadingUpload)
			return err
		}
	}
	if uploadingUpload != nil {
		if err := coordinator.finishUpload(uploadingUpload); err != nil {
			return err
		}
	}

	reporter.finish()
	return nil
}

func (u *uploadCoordinator) launchUpload(file sourceFile, index int64) (*activeUpload, error) {
	desiredPath := joinRemotePath(u.destRoot, file.RelPath)
	finalPath, err := u.planner.ResolveAvailablePath(u.ctx, desiredPath)
	if err != nil {
		return nil, err
	}

	return u.launchUploadAttempt(file, index, desiredPath, finalPath, 1)
}

func (u *uploadCoordinator) launchUploadAttempt(file sourceFile, index int64, desiredPath, finalPath string, attempt int) (*activeUpload, error) {
	fileKey := fmt.Sprintf("%d:%s", index, file.RelPath)
	u.reporter.startFile(fileKey, index, file, finalPath, attempt)

	tempPath := buildTempUploadPath(u.sessionID, u.destRoot, file.RelPath, attempt)
	uploadID := fmt.Sprintf("%s-%d-%d", u.sessionID, index, attempt)
	session, err := u.client.StartUpload(u.ctx, pentaract.UploadInput{
		StorageID:      u.storageID,
		Token:          u.token,
		LocalPath:      file.AbsPath,
		RemotePath:     tempPath,
		RemoteFilename: remoteFilenameFromPath(tempPath),
		UploadID:       uploadID,
		OnProgress: func(progress pentaract.UploadProgress) {
			u.reporter.updateProgress(fileKey, progress)
		},
	})
	if err != nil {
		u.reporter.removeFile(fileKey)
		return nil, fmt.Errorf("subiendo %s: %w", file.RelPath, err)
	}

	_ = session.WaitForRequest()

	return &activeUpload{
		fileKey:     fileKey,
		index:       index,
		file:        file,
		desiredPath: desiredPath,
		finalPath:   finalPath,
		tempPath:    tempPath,
		attempt:     attempt,
		uploadID:    uploadID,
		handle:      session,
	}, nil
}

func (u *uploadCoordinator) finishUpload(upload *activeUpload) error {
	uploadCompleted, lastErr, err := u.waitForUploadResult(upload)
	if err != nil {
		u.reporter.removeFile(upload.fileKey)
		return err
	}
	if !uploadCompleted {
		u.reporter.removeFile(upload.fileKey)
		if lastErr == nil {
			lastErr = errors.New("upload did not complete")
		}
		return fmt.Errorf("subiendo %s: %w", upload.file.RelPath, lastErr)
	}

	u.reporter.setStatus(upload.fileKey, "moving", "moviendo al destino final")
	for moveAttempt := 1; moveAttempt <= u.retries; moveAttempt++ {
		err := u.client.Move(u.ctx, u.token, u.storageID, upload.tempPath, upload.finalPath)
		if err == nil {
			u.planner.RememberPath(upload.finalPath)
			u.reporter.completeFile(upload.fileKey, upload.file, upload.finalPath)
			return nil
		}

		if moveAttempt == u.retries || !pentaract.IsRetryable(err) {
			u.reporter.removeFile(upload.fileKey)
			return fmt.Errorf("moviendo %s a %s: %w", upload.tempPath, upload.finalPath, err)
		}

		u.planner.Invalidate(pathDir(upload.finalPath))
		upload.finalPath, err = u.planner.ResolveAvailablePath(u.ctx, upload.desiredPath)
		if err != nil {
			u.reporter.removeFile(upload.fileKey)
			return err
		}
		u.reporter.setStatus(upload.fileKey, "moving", fmt.Sprintf("reintentando move a %s", upload.finalPath))
		if err := sleepWithContext(u.ctx, u.retryDelay); err != nil {
			u.reporter.removeFile(upload.fileKey)
			return err
		}
	}

	u.reporter.removeFile(upload.fileKey)
	return nil
}

func (u *uploadCoordinator) waitForUploadResult(upload *activeUpload) (bool, error, error) {
	for {
		err := upload.handle.Wait()
		if err == nil {
			return true, nil, nil
		}

		lastErr := err
		exists, existsErr := u.client.FileExists(context.WithoutCancel(u.ctx), u.token, u.storageID, upload.tempPath)
		if existsErr == nil && exists {
			return true, nil, nil
		}

		if upload.attempt == u.retries || !pentaract.IsRetryable(err) {
			return false, lastErr, nil
		}

		u.reporter.setStatus(upload.fileKey, "retrying", fmt.Sprintf("reintento %d/%d tras error: %v", upload.attempt, u.retries, err))
		if err := sleepWithContext(u.ctx, u.retryDelay); err != nil {
			return false, nil, err
		}

		nextAttempt, startErr := u.launchUploadAttempt(upload.file, upload.index, upload.desiredPath, upload.finalPath, upload.attempt+1)
		if startErr != nil {
			return false, nil, startErr
		}

		upload.tempPath = nextAttempt.tempPath
		upload.attempt = nextAttempt.attempt
		upload.uploadID = nextAttempt.uploadID
		upload.handle = nextAttempt.handle
	}
}

func cancelUploads(ctx context.Context, client *pentaract.Client, token string, reporter *reporter, uploads ...*activeUpload) {
	for _, upload := range uploads {
		if upload == nil {
			continue
		}
		reporter.removeFile(upload.fileKey)
		if upload.uploadID == "" {
			continue
		}
		_ = client.CancelUpload(ctx, token, upload.uploadID)
	}
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
