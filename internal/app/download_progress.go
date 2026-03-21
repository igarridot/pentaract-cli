package app

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type downloadReporter struct {
	out io.Writer

	mu             sync.Mutex
	totalFiles     int64
	totalBytes     int64
	completedFiles atomic.Int64
	completedBytes atomic.Int64
	active         map[string]*downloadFileState
	lastPrint      time.Time
}

type downloadFileState struct {
	Index        int64
	TotalFiles   int64
	RelativePath string
	LocalPath    string
	Size         int64
	Attempt      int
	Status       string
	WrittenBytes atomic.Int64
}

func newDownloadReporter(out io.Writer, totalFiles, totalBytes int64) *downloadReporter {
	return &downloadReporter{
		out:        out,
		totalFiles: totalFiles,
		totalBytes: totalBytes,
		active:     map[string]*downloadFileState{},
	}
}

func (r *downloadReporter) startFile(fileKey string, index int64, rf remoteFile, localPath string, attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := &downloadFileState{
		Index:        index,
		TotalFiles:   r.totalFiles,
		RelativePath: rf.Path,
		LocalPath:    localPath,
		Size:         rf.Size,
		Attempt:      attempt,
		Status:       "downloading",
	}
	r.active[fileKey] = state
	r.printLocked(state, true)
}

func (r *downloadReporter) updateBytes(fileKey string, bytesWritten int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.active[fileKey]
	if !ok {
		return
	}
	state.WrittenBytes.Store(bytesWritten)
	r.printLocked(state, false)
}

func (r *downloadReporter) setStatus(fileKey, status, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.active[fileKey]
	if !ok {
		return
	}
	state.Status = status
	r.printLocked(state, true)
	if message != "" {
		fmt.Fprintf(r.out, "  %s\n", message)
	}
}

func (r *downloadReporter) completeFile(fileKey string, rf remoteFile) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.active, fileKey)
	r.completedFiles.Add(1)
	r.completedBytes.Add(rf.Size)

	state := &downloadFileState{
		Index:        0,
		TotalFiles:   r.totalFiles,
		RelativePath: rf.Path,
		Size:         rf.Size,
		Status:       "done",
	}
	state.WrittenBytes.Store(rf.Size)
	r.printLocked(state, true)
}

func (r *downloadReporter) removeFile(fileKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, fileKey)
}

func (r *downloadReporter) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()

	fmt.Fprintf(
		r.out,
		"Summary: %d/%d files downloaded, %s/%s transferred.\n",
		r.completedFiles.Load(),
		r.totalFiles,
		humanBytes(r.completedBytes.Load()),
		humanBytes(r.totalBytes),
	)
}

func (r *downloadReporter) currentDownloadedBytesLocked() int64 {
	currentBytes := r.completedBytes.Load()
	for _, state := range r.active {
		currentBytes += minInt64(state.WrittenBytes.Load(), state.Size)
	}
	return currentBytes
}

func (r *downloadReporter) printLocked(state *downloadFileState, force bool) {
	if !force && time.Since(r.lastPrint) < time.Second {
		return
	}
	r.lastPrint = time.Now()

	currentBytes := r.currentDownloadedBytesLocked()
	written := state.WrittenBytes.Load()

	filePercent := 100.0
	if state.Size > 0 {
		filePercent = float64(minInt64(written, state.Size)) / float64(state.Size) * 100
	}
	globalPercent := 100.0
	if r.totalBytes > 0 {
		globalPercent = float64(minInt64(currentBytes, r.totalBytes)) / float64(r.totalBytes) * 100
	}

	fmt.Fprintf(
		r.out,
		"[%d/%d] %s | attempt %d | status=%s | file %.1f%% (%s/%s)\n",
		state.Index,
		state.TotalFiles,
		state.RelativePath,
		state.Attempt,
		state.Status,
		filePercent,
		humanBytes(minInt64(written, state.Size)),
		humanBytes(state.Size),
	)
	fmt.Fprintf(
		r.out,
		"Global: %d/%d files, %.1f%% (%s/%s)\n",
		r.completedFiles.Load(),
		r.totalFiles,
		globalPercent,
		humanBytes(minInt64(currentBytes, r.totalBytes)),
		humanBytes(r.totalBytes),
	)
}
