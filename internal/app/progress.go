package app

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Dominux/pentaract-cli/internal/pentaract"
)

type reporter struct {
	out io.Writer

	mu             sync.Mutex
	totalFiles     int64
	totalBytes     int64
	completedFiles int64
	completedBytes int64
	current        currentFileState
	lastPrint      time.Time
}

type currentFileState struct {
	Index             int64
	TotalFiles        int64
	RelativePath      string
	TargetPath        string
	Size              int64
	Attempt           int
	Status            string
	WorkersStatus     string
	UploadedBytes     int64
	UploadedChunks    int64
	TotalChunks       int64
	VerifiedChunks    int64
	VerificationTotal int64
	LastMessage       string
}

func newReporter(out io.Writer, totalFiles, totalBytes int64) *reporter {
	return &reporter{
		out:        out,
		totalFiles: totalFiles,
		totalBytes: totalBytes,
		current: currentFileState{
			Status: "pending",
		},
	}
}

func (r *reporter) startFile(index int64, file sourceFile, targetPath string, attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.current = currentFileState{
		Index:         index,
		TotalFiles:    r.totalFiles,
		RelativePath:  file.RelPath,
		TargetPath:    targetPath,
		Size:          file.Size,
		Attempt:       attempt,
		Status:        "starting",
		WorkersStatus: "active",
	}
	r.printLocked(true)
}

func (r *reporter) updateProgress(progress pentaract.UploadProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.current.Status = progress.Status
	r.current.WorkersStatus = progress.WorkersStatus
	r.current.UploadedBytes = progress.UploadedBytes
	r.current.UploadedChunks = progress.Uploaded
	r.current.TotalChunks = progress.Total
	r.current.VerifiedChunks = progress.Verified
	r.current.VerificationTotal = progress.VerificationTotal
	r.printLocked(false)
}

func (r *reporter) setStatus(status, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.current.Status = status
	r.current.LastMessage = message
	r.printLocked(true)
}

func (r *reporter) completeFile(file sourceFile, finalTarget string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.completedFiles++
	r.completedBytes += file.Size
	r.current.Status = "done"
	r.current.TargetPath = finalTarget
	r.current.UploadedBytes = file.Size
	r.current.LastMessage = "subida completada"
	r.printLocked(true)
}

func (r *reporter) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()

	fmt.Fprintf(
		r.out,
		"Resumen: %d/%d archivos completados, %s/%s transferidos.\n",
		r.completedFiles,
		r.totalFiles,
		humanBytes(r.completedBytes),
		humanBytes(r.totalBytes),
	)
}

func (r *reporter) printLocked(force bool) {
	if !force && time.Since(r.lastPrint) < time.Second {
		return
	}
	r.lastPrint = time.Now()

	currentBytes := r.completedBytes
	if r.current.UploadedBytes > 0 {
		currentBytes += minInt64(r.current.UploadedBytes, r.current.Size)
	}

	filePercent := 100.0
	if r.current.Size > 0 {
		filePercent = float64(minInt64(r.current.UploadedBytes, r.current.Size)) / float64(r.current.Size) * 100
	}
	globalPercent := 100.0
	if r.totalBytes > 0 {
		globalPercent = float64(minInt64(currentBytes, r.totalBytes)) / float64(r.totalBytes) * 100
	}

	fmt.Fprintf(
		r.out,
		"[%d/%d] %s -> %s | intento %d | estado=%s | fichero %.1f%% (%s/%s) | chunks %d/%d | verificacion %d/%d | workers=%s\n",
		r.current.Index,
		r.totalFiles,
		r.current.RelativePath,
		r.current.TargetPath,
		r.current.Attempt,
		r.current.Status,
		filePercent,
		humanBytes(minInt64(r.current.UploadedBytes, r.current.Size)),
		humanBytes(r.current.Size),
		r.current.UploadedChunks,
		r.current.TotalChunks,
		r.current.VerifiedChunks,
		r.current.VerificationTotal,
		emptyFallback(r.current.WorkersStatus, "unknown"),
	)
	fmt.Fprintf(
		r.out,
		"Global: %d/%d archivos, %.1f%% (%s/%s)%s\n",
		r.completedFiles,
		r.totalFiles,
		globalPercent,
		humanBytes(minInt64(currentBytes, r.totalBytes)),
		humanBytes(r.totalBytes),
		renderOptionalMessage(r.current.LastMessage),
	)
}

func renderOptionalMessage(message string) string {
	if message == "" {
		return ""
	}
	return " | " + message
}

func emptyFallback(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func humanBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}

	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	return fmt.Sprintf("%.1f %s", float64(value)/float64(div), suffixes[exp])
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
