// Package workspace resolves tool paths and keeps them inside the directories
// the server was allowed to touch.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var ErrOutside = errors.New("path is outside the allowed directories")

type Workspace struct {
	root    string
	allowed []string
}

// New canonicalizes root and the extra allowed directories. Missing allowed
// directories are kept as given so they work once they are created.
func New(root string, allowed ...string) (*Workspace, error) {
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory; %w", err)
		}

		root = wd
	}

	canonicalRoot, err := canonical(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root %q; %w", root, err)
	}

	info, err := os.Stat(canonicalRoot)
	if err != nil {
		return nil, fmt.Errorf("stat root; %w", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", root)
	}

	w := &Workspace{root: canonicalRoot}

	for _, dir := range allowed {
		if dir == "" {
			continue
		}

		dir, err := expandHome(dir)
		if err != nil {
			return nil, err
		}

		if !filepath.IsAbs(dir) {
			dir = filepath.Join(canonicalRoot, dir)
		}

		c, err := canonical(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve allowed dir %q; %w", dir, err)
		}

		w.allowed = append(w.allowed, c)
	}

	return w, nil
}

func (w *Workspace) Root() string { return w.root }

func (w *Workspace) Allowed() []string { return append([]string(nil), w.allowed...) }

// Resolve turns a tool path into a canonical absolute path. Relative paths
// resolve from the root. Symlinks are followed, so a link pointing outside the
// allowed directories is rejected. The target itself need not exist.
func (w *Workspace) Resolve(p string) (string, error) {
	if p == "" {
		p = "."
	}

	p, err := expandHome(p)
	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(p) {
		p = filepath.Join(w.root, p)
	}

	c, err := canonical(p)
	if err != nil {
		return "", err
	}

	if !w.Contains(c) {
		return "", fmt.Errorf("%w: %s", ErrOutside, p)
	}

	return c, nil
}

// Contains reports whether a canonical path is inside the root or an allowed
// directory.
func (w *Workspace) Contains(p string) bool {
	if Within(w.root, p) {
		return true
	}

	for _, dir := range w.allowed {
		if Within(dir, p) {
			return true
		}
	}

	return false
}

// Display returns a path relative to the root when it is inside it, and the
// absolute path otherwise.
func (w *Workspace) Display(p string) string {
	if rel, err := filepath.Rel(w.root, p); err == nil && Within(w.root, p) {
		return filepath.ToSlash(rel)
	}

	return p
}

// Within reports whether child is parent or below it. Both must be clean.
func Within(parent, child string) bool {
	if parent == child {
		return true
	}

	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}

	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// canonical resolves symlinks of the longest existing prefix of p and joins
// the remaining, not yet existing, components onto it.
func canonical(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}

	var rest []string

	current := p
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, rest[i])
			}

			return resolved, nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return p, nil
		}

		rest = append(rest, filepath.Base(current))
		current = parent
	}
}

func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand ~; %w", err)
	}

	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}
