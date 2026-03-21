package app

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/Dominux/pentaract-cli/internal/pentaract"
)

type dirLister interface {
	ListDir(ctx context.Context, token, storageID, dir string) ([]pentaract.FSElement, error)
}

type remoteNamePlanner struct {
	client    dirLister
	token     string
	storageID string
	mu        sync.Mutex // C1/C4: thread-safe for concurrent uploads
	cache     map[string]map[string]int64 // dir → name → size
}

func newRemoteNamePlanner(client dirLister, token, storageID string) *remoteNamePlanner {
	return &remoteNamePlanner{
		client:    client,
		token:     token,
		storageID: storageID,
		cache:     map[string]map[string]int64{},
	}
}

func (p *remoteNamePlanner) ResolveAvailablePath(ctx context.Context, desired string) (string, error) {
	dir, name := splitRemotePath(desired)
	names, err := p.loadDirNames(ctx, dir)
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := names[name]; !exists {
		return joinRemotePath(dir, name), nil
	}

	for i := 1; ; i++ {
		candidate := addCopySuffix(name, i)
		if _, exists := names[candidate]; exists {
			continue
		}
		return joinRemotePath(dir, candidate), nil
	}
}

// ExistsWithSize checks if a file with the given path and size already exists
// on the remote. Uses the pre-warmed cache when available.
func (p *remoteNamePlanner) ExistsWithSize(ctx context.Context, desired string, size int64) (bool, error) {
	dir, name := splitRemotePath(desired)
	names, err := p.loadDirNames(ctx, dir)
	if err != nil {
		return false, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	remoteSize, exists := names[name]
	return exists && remoteSize == size, nil
}

func (p *remoteNamePlanner) RememberPath(fullPath string, size int64) {
	dir, name := splitRemotePath(fullPath)
	p.mu.Lock()
	defer p.mu.Unlock()
	names, ok := p.cache[dir]
	if !ok {
		names = map[string]int64{}
		p.cache[dir] = names
	}
	names[name] = size
}

// PrewarmDirs fetches directory listings in parallel for all unique
// parent directories of the given files. C4: reduces sequential API
// calls during upload by pre-populating the cache.
func (p *remoteNamePlanner) PrewarmDirs(ctx context.Context, destRoot string, files []sourceFile) {
	dirs := map[string]struct{}{}
	for _, f := range files {
		dir, _ := splitRemotePath(joinRemotePath(destRoot, f.RelPath))
		dirs[dir] = struct{}{}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for dir := range dirs {
		dir := dir
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			p.loadDirNames(ctx, dir)
		}()
	}
	wg.Wait()
}

func (p *remoteNamePlanner) loadDirNames(ctx context.Context, dir string) (map[string]int64, error) {
	dir = cleanRemotePath(dir)
	p.mu.Lock()
	if names, ok := p.cache[dir]; ok {
		p.mu.Unlock()
		return names, nil
	}
	p.mu.Unlock()

	items, err := p.client.ListDir(ctx, p.token, p.storageID, dir)
	if err != nil {
		return nil, fmt.Errorf("listing remote dir %q: %w", dir, err)
	}

	names := map[string]int64{}
	for _, item := range items {
		names[item.Name] = item.Size
	}

	p.mu.Lock()
	// Double-check in case another goroutine populated it
	if existing, ok := p.cache[dir]; ok {
		p.mu.Unlock()
		return existing, nil
	}
	p.cache[dir] = names
	p.mu.Unlock()
	return names, nil
}

func splitRemotePath(fullPath string) (dir, name string) {
	fullPath = cleanRemotePath(fullPath)
	dir, name = path.Split(fullPath)
	dir = strings.TrimSuffix(dir, "/")
	return dir, name
}

func joinRemotePath(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = cleanRemotePath(part)
		if part == "" {
			continue
		}
		cleaned = append(cleaned, part)
	}
	return strings.Join(cleaned, "/")
}

func cleanRemotePath(raw string) string {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	raw = strings.Trim(raw, "/")
	for strings.Contains(raw, "//") {
		raw = strings.ReplaceAll(raw, "//", "/")
	}
	return raw
}

func addCopySuffix(filename string, n int) string {
	if n < 1 {
		return filename
	}

	lastDot := strings.LastIndex(filename, ".")
	if lastDot <= 0 {
		return fmt.Sprintf("%s (%d)", filename, n)
	}
	return fmt.Sprintf("%s (%d)%s", filename[:lastDot], n, filename[lastDot:])
}
