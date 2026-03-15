package app

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/Dominux/pentaract-cli/internal/pentaract"
)

type dirLister interface {
	ListDir(ctx context.Context, token, storageID, dir string) ([]pentaract.FSElement, error)
}

type remoteNamePlanner struct {
	client    dirLister
	token     string
	storageID string
	cache     map[string]map[string]struct{}
}

func newRemoteNamePlanner(client dirLister, token, storageID string) *remoteNamePlanner {
	return &remoteNamePlanner{
		client:    client,
		token:     token,
		storageID: storageID,
		cache:     map[string]map[string]struct{}{},
	}
}

func (p *remoteNamePlanner) ResolveAvailablePath(ctx context.Context, desired string) (string, error) {
	dir, name := splitRemotePath(desired)
	names, err := p.loadDirNames(ctx, dir)
	if err != nil {
		return "", err
	}
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

func (p *remoteNamePlanner) RememberPath(fullPath string) {
	dir, name := splitRemotePath(fullPath)
	names, ok := p.cache[dir]
	if !ok {
		names = map[string]struct{}{}
		p.cache[dir] = names
	}
	names[name] = struct{}{}
}

func (p *remoteNamePlanner) Invalidate(dir string) {
	delete(p.cache, cleanRemotePath(dir))
}

func (p *remoteNamePlanner) loadDirNames(ctx context.Context, dir string) (map[string]struct{}, error) {
	dir = cleanRemotePath(dir)
	if names, ok := p.cache[dir]; ok {
		return names, nil
	}

	items, err := p.client.ListDir(ctx, p.token, p.storageID, dir)
	if err != nil {
		return nil, fmt.Errorf("listing remote dir %q: %w", dir, err)
	}

	names := map[string]struct{}{}
	for _, item := range items {
		names[item.Name] = struct{}{}
	}
	p.cache[dir] = names
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

func buildTempUploadPath(sessionID, destRoot, relativePath string, attempt int) string {
	relDir, relName := splitRemotePath(relativePath)
	lastDot := strings.LastIndex(relName, ".")
	stem := relName
	ext := ""
	if lastDot > 0 {
		stem = relName[:lastDot]
		ext = relName[lastDot:]
	}

	tempName := fmt.Sprintf("%s.__pentaract_cli_%s_%02d%s", stem, sessionID, attempt, ext)
	return joinRemotePath(".pentaract-cli-tmp", sessionID, destRoot, relDir, tempName)
}
