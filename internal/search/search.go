// Package search finds files by glob and lines by regular expression, using
// ripgrep when available and a built-in Go implementation otherwise.
package search

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

type GlobInput struct {
	Dir     string
	Pattern string
	Hidden  bool
	// Limit stops the search after this many results; the engine returns at
	// most Limit+1 so the caller can tell the result was cut.
	Limit int
}

type GrepInput struct {
	// Dir is the directory searched, or the parent of File.
	Dir string
	// File, when set, restricts the search to this file (relative to Dir).
	File          string
	Pattern       string
	Literal       bool
	CaseSensitive bool
	Include       string
	Limit         int
}

type Match struct {
	// Path is relative to the searched directory, slash separated.
	Path string
	Line int
	Text string
}

type Engine interface {
	Name() string
	// Glob returns file paths relative to in.Dir, slash separated.
	Glob(ctx context.Context, in GlobInput) ([]string, error)
	Grep(ctx context.Context, in GrepInput) ([]Match, error)
}

// New selects an engine: "rg", "go" or "auto" (rg when it can be found).
func New(engine, rgPath string) (Engine, error) {
	switch engine {
	case "go":
		return Native{}, nil
	case "rg", "auto", "":
		path := rgPath
		if path == "" {
			path = "rg"
		}

		resolved, err := exec.LookPath(path)
		if err != nil {
			if engine == "rg" {
				return nil, fmt.Errorf("ripgrep not found (%s); %w", path, err)
			}

			slog.Info("ripgrep not found, using built-in search")

			return Native{}, nil
		}

		return Ripgrep{Binary: resolved}, nil
	default:
		return nil, fmt.Errorf("unknown search engine %q", engine)
	}
}

func cleanRel(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}

	return strings.TrimPrefix(p, "/")
}
