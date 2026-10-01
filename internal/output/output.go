// Package output bounds tool output. Text over the limit is cut to a preview
// and the full text is saved to a file the model can grep or read in pages.
package output

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const retention = 7 * 24 * time.Hour

type Direction int

const (
	Head Direction = iota
	Tail
)

type Store struct {
	dir      string
	maxLines int
	maxBytes int
	seq      atomic.Uint64
}

func New(dir string, maxLines, maxBytes int) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create output dir; %w", err)
	}

	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve output dir; %w", err)
	}

	s := &Store{dir: dir, maxLines: maxLines, maxBytes: maxBytes}
	s.cleanup()

	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) newPath() string {
	return filepath.Join(s.dir, fmt.Sprintf("tool_%d_%d", time.Now().UnixNano(), s.seq.Add(1)))
}

// Create opens a new file in the store for streaming output into.
func (s *Store) Create() (*os.File, error) {
	f, err := os.OpenFile(s.newPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create output file; %w", err)
	}

	return f, nil
}

// TailFile returns the file's content when it fits the limits, removing the
// file when keep is false. Otherwise it returns the last lines that fit and
// keeps the file so the full output stays available.
func (s *Store) TailFile(path string, keep bool) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return Result{}, err
	}

	size := st.Size()
	start := max(size-int64(s.maxBytes), 0)

	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return Result{}, err
	}

	content := string(buf)
	lines := strings.Split(content, "\n")

	if start == 0 && len(lines) <= s.maxLines {
		if !keep {
			_ = os.Remove(path)
		}

		return Result{Content: content}, nil
	}

	if start > 0 {
		// The first line is probably cut in the middle.
		lines = lines[1:]
	}

	if len(lines) > s.maxLines {
		lines = lines[len(lines)-s.maxLines:]
	}

	preview := strings.Join(lines, "\n")
	removed := size - int64(len(preview))

	return Result{
		Content: fmt.Sprintf("...%d bytes truncated...\n\nThe output was truncated. Full output saved to: %s\n"+
			"Use grep to search the full content or read with offset/limit to view specific sections.\n\n%s",
			removed, path, preview),
		Truncated: true,
		Path:      path,
	}, nil
}

// Write saves text to a new file in the store and returns its path.
func (s *Store) Write(text string) (string, error) {
	path := s.newPath()

	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("save output; %w", err)
	}

	return path, nil
}

type Result struct {
	Content   string
	Truncated bool
	Path      string
}

// Truncate returns text unchanged when it fits, otherwise a preview from the
// head or tail with a hint pointing at the saved full text.
func (s *Store) Truncate(text string, direction Direction) Result {
	lines := strings.Split(text, "\n")
	if len(lines) <= s.maxLines && len(text) <= s.maxBytes {
		return Result{Content: text}
	}

	var (
		kept    []string
		bytes   int
		hitByte bool
	)

	if direction == Head {
		for i := 0; i < len(lines) && i < s.maxLines; i++ {
			size := len(lines[i])
			if i > 0 {
				size++
			}

			if bytes+size > s.maxBytes {
				hitByte = true

				break
			}

			kept = append(kept, lines[i])
			bytes += size
		}
	} else {
		for i := len(lines) - 1; i >= 0 && len(kept) < s.maxLines; i-- {
			size := len(lines[i])
			if len(kept) > 0 {
				size++
			}

			if bytes+size > s.maxBytes {
				hitByte = true

				break
			}

			kept = append([]string{lines[i]}, kept...)
			bytes += size
		}
	}

	removed, unit := len(lines)-len(kept), "lines"
	if hitByte {
		removed, unit = len(text)-bytes, "bytes"
	}

	preview := strings.Join(kept, "\n")

	path, err := s.Write(text)
	if err != nil {
		return Result{
			Content:   fmt.Sprintf("%s\n\n...%d %s truncated (full output could not be saved: %v)...", preview, removed, unit, err),
			Truncated: true,
		}
	}

	hint := fmt.Sprintf("The tool call succeeded but the output was truncated. Full output saved to: %s\n"+
		"Use grep to search the full content or read with offset/limit to view specific sections.", path)

	if direction == Head {
		return Result{
			Content:   fmt.Sprintf("%s\n\n...%d %s truncated...\n\n%s", preview, removed, unit, hint),
			Truncated: true,
			Path:      path,
		}
	}

	return Result{
		Content:   fmt.Sprintf("...%d %s truncated...\n\n%s\n\n%s", removed, unit, hint, preview),
		Truncated: true,
		Path:      path,
	}
}

func (s *Store) cleanup() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-retention)

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "tool_") {
			continue
		}

		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}

		_ = os.Remove(filepath.Join(s.dir, entry.Name()))
	}
}
