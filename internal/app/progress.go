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
	nextIndex      int64 // upload sequence; increments only for non-skipped files
	active         map[string]currentFileState
	lastPrint      time.Time
}

type currentFileState struct {
	Index             int64
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
		active:     map[string]currentFileState{},
	}
}

func (r *reporter) startFile(fileKey string, _ int64, file sourceFile, targetPath string, attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// On retries the file is already in active; reuse its display index.
	// On first attempt assign the next upload sequence number.
	var displayIndex int64
	if existing, ok := r.active[fileKey]; ok {
		displayIndex = existing.Index
	} else {
		r.nextIndex++
		displayIndex = r.nextIndex
	}

	state := currentFileState{
		Index:         displayIndex,
		RelativePath:  file.RelPath,
		TargetPath:    targetPath,
		Size:          file.Size,
		Attempt:       attempt,
		Status:        "starting",
		WorkersStatus: "active",
	}
	r.active[fileKey] = state
	r.printLocked(state, true)
}

func (r *reporter) updateProgress(fileKey string, progress pentaract.UploadProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.active[fileKey]
	if !ok {
		return
	}

	state.Status = progress.Status
	state.WorkersStatus = progress.WorkersStatus
	state.UploadedBytes = progress.UploadedBytes
	state.UploadedChunks = progress.Uploaded
	state.TotalChunks = progress.Total
	state.VerifiedChunks = progress.Verified
	state.VerificationTotal = progress.VerificationTotal
	r.active[fileKey] = state
	r.printLocked(state, false)
}

func (r *reporter) setStatus(fileKey, status, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.active[fileKey]
	if !ok {
		return
	}

	state.Status = status
	state.LastMessage = message
	r.active[fileKey] = state
	r.printLocked(state, true)
}

func (r *reporter) completeFile(fileKey string, file sourceFile, finalTarget string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.active[fileKey]
	if !ok {
		state = currentFileState{
			RelativePath: file.RelPath,
			TargetPath:   finalTarget,
			Size:         file.Size,
			Status:       "done",
		}
	}

	delete(r.active, fileKey)
	r.completedFiles++
	r.completedBytes += file.Size

	state.Status = "done"
	state.TargetPath = finalTarget
	state.UploadedBytes = file.Size
	state.LastMessage = "upload complete"
	r.printLocked(state, true)
}

func (r *reporter) removeFile(fileKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.active, fileKey)
}

// skipFile removes a file from the reporter totals so that progress
// percentages and the final summary reflect only the files that were
// actually uploaded.
func (r *reporter) skipFile(size int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalFiles--
	r.totalBytes -= size
}

func (r *reporter) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()

	fmt.Fprintf(
		r.out,
		"Summary: %d/%d files completed, %s/%s transferred.\n",
		r.completedFiles,
		r.totalFiles,
		humanBytes(r.completedBytes),
		humanBytes(r.totalBytes),
	)
}

func (r *reporter) currentUploadedBytesLocked() int64 {
	currentBytes := r.completedBytes
	for _, state := range r.active {
		currentBytes += minInt64(state.UploadedBytes, state.Size)
	}
	return currentBytes
}

func (r *reporter) printLocked(state currentFileState, force bool) {
	if !force && time.Since(r.lastPrint) < time.Second {
		return
	}
	r.lastPrint = time.Now()

	currentBytes := r.currentUploadedBytesLocked()

	filePercent := 100.0
	if state.Size > 0 {
		filePercent = float64(minInt64(state.UploadedBytes, state.Size)) / float64(state.Size) * 100
	}
	globalPercent := 100.0
	if r.totalBytes > 0 {
		globalPercent = float64(minInt64(currentBytes, r.totalBytes)) / float64(r.totalBytes) * 100
	}

	fmt.Fprintf(
		r.out,
		"[%d/%d] %s -> %s | attempt %d | status=%s | file %.1f%% (%s/%s) | chunks %d/%d | verification %d/%d | workers=%s\n",
		state.Index,
		r.totalFiles,
		state.RelativePath,
		state.TargetPath,
		state.Attempt,
		state.Status,
		filePercent,
		humanBytes(minInt64(state.UploadedBytes, state.Size)),
		humanBytes(state.Size),
		state.UploadedChunks,
		state.TotalChunks,
		state.VerifiedChunks,
		state.VerificationTotal,
		emptyFallback(state.WorkersStatus, "unknown"),
	)
	fmt.Fprintf(
		r.out,
		"Global: %d/%d files, %.1f%% (%s/%s)%s\n",
		r.completedFiles,
		r.totalFiles,
		globalPercent,
		humanBytes(minInt64(currentBytes, r.totalBytes)),
		humanBytes(r.totalBytes),
		renderOptionalMessage(state.LastMessage),
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
