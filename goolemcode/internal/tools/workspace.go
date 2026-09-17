package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Workspace is the current working directory for tools (files, grep, git).
// It is mobile: the user can navigate it with /cd during the session to
// manage other folders, keeping the context (conversation and memory) anchored
// where it started. All operations are confined to the current root on each
// call (mobile sandbox, not without sandbox).
type Workspace struct {
	mu   sync.RWMutex
	root string
}

func NewWorkspace(root string) *Workspace {
	abs, _ := filepath.Abs(root)
	return &Workspace{root: abs}
}

func (w *Workspace) Root() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.root
}

// Resolve resolves a relative path within the current root, rejecting sandbox
// escapes (reuses safeJoin). Useful for @file mentions.
func (w *Workspace) Resolve(rel string) (string, error) {
	return safeJoin(w.Root(), rel)
}

// SetRoot changes the working directory. Accepts absolute or relative paths
// from the current directory; verifies it exists and is a directory.
func (w *Workspace) SetRoot(dir string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := dir
	if !filepath.IsAbs(p) {
		p = filepath.Join(w.root, p)
	}
	p = filepath.Clean(p)
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("not an accessible directory: %s", dir)
	}
	w.root = p
	return p, nil
}
