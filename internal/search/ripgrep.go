package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const maxRecordBytes = 1024 * 1024

// Ripgrep runs the rg binary, mirroring the arguments opencode uses.
type Ripgrep struct {
	Binary string
}

func (Ripgrep) Name() string { return "ripgrep" }

func (r Ripgrep) Glob(ctx context.Context, in GlobInput) ([]string, error) {
	args := []string{"--no-config", "--files", "--sort=path"}
	if in.Hidden {
		args = append(args, "--hidden")
	}

	args = append(args, "--glob="+in.Pattern, "--glob=!**/.git/**", ".")

	var out []string

	err := r.run(ctx, in.Dir, args, func(line string) bool {
		out = append(out, cleanRel(line))

		return len(out) <= in.Limit
	})

	return out, err
}

type rgRecord struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

func (r Ripgrep) Grep(ctx context.Context, in GrepInput) ([]Match, error) {
	args := []string{"--no-config", "--json", "--hidden", "--no-messages", "--sort=path"}
	if in.Literal {
		args = append(args, "--fixed-strings")
	}

	if in.CaseSensitive {
		args = append(args, "--case-sensitive")
	} else {
		args = append(args, "--ignore-case")
	}

	if in.Include != "" {
		args = append(args, "--glob="+in.Include)
	}

	target := "."
	if in.File != "" {
		target = in.File
	}

	args = append(args, "--glob=!**/.git/**", "--", in.Pattern, target)

	var out []Match

	err := r.run(ctx, in.Dir, args, func(line string) bool {
		if !strings.HasPrefix(line, `{"type":"match"`) {
			return true
		}

		var rec rgRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return true
		}

		out = append(out, Match{
			Path: cleanRel(rec.Data.Path.Text),
			Line: rec.Data.LineNumber,
			Text: strings.TrimRight(rec.Data.Lines.Text, "\r\n"),
		})

		return len(out) <= in.Limit
	})

	return out, err
}

// run streams rg stdout lines to fn until fn returns false, then stops rg.
func (r Ripgrep) run(ctx context.Context, dir string, args []string, fn func(string) bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Dir = dir

	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 8 * 1024}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ripgrep stdout; %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ripgrep; %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxRecordBytes)

	stopped := false

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if !fn(line) {
			stopped = true

			cancel()

			break
		}
	}

	waitErr := cmd.Wait()
	if stopped {
		return nil
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read ripgrep output; %w", err)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		msg := strings.TrimSpace(stderr.String())

		switch exitErr.ExitCode() {
		case 1:
			// No matches.
			return nil
		case 2:
			if strings.Contains(msg, "regex parse error") || strings.Contains(msg, "error parsing") {
				return fmt.Errorf("invalid pattern: %s", msg)
			}
			// Some files could not be read; keep the partial result.
			return nil
		}

		if msg == "" {
			msg = waitErr.Error()
		}

		return fmt.Errorf("ripgrep failed: %s", msg)
	}

	if waitErr != nil {
		return fmt.Errorf("ripgrep failed; %w", waitErr)
	}

	return nil
}

type limitedWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}

	return len(p), nil
}
