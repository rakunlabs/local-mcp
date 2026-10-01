package search

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// Native is a dependency-free engine. It walks the tree honouring .gitignore,
// .ignore and .git/info/exclude, and matches globs the way ripgrep's --glob
// does: a pattern without a slash matches the file name at any depth.
type Native struct{}

func (Native) Name() string { return "go" }

func (Native) Glob(ctx context.Context, in GlobInput) ([]string, error) {
	if !doublestar.ValidatePattern(in.Pattern) {
		return nil, fmt.Errorf("invalid glob pattern %q", in.Pattern)
	}

	var out []string

	err := walk(ctx, in.Dir, in.Hidden, in.Pattern, func(rel string) bool {
		if matchGlob(in.Pattern, rel) {
			out = append(out, rel)
		}

		return len(out) <= in.Limit
	})

	return out, err
}

func (Native) Grep(ctx context.Context, in GrepInput) ([]Match, error) {
	expr := in.Pattern
	if in.Literal {
		expr = regexp.QuoteMeta(expr)
	}

	if !in.CaseSensitive {
		expr = "(?i)" + expr
	}

	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	if in.Include != "" && !doublestar.ValidatePattern(in.Include) {
		return nil, fmt.Errorf("invalid include pattern %q", in.Include)
	}

	var out []Match

	visit := func(rel string) bool {
		if in.Include != "" && !matchGlob(in.Include, rel) {
			return true
		}

		matches, _ := grepFile(filepath.Join(in.Dir, filepath.FromSlash(rel)), rel, re, in.Limit+1-len(out))
		out = append(out, matches...)

		return len(out) <= in.Limit
	}

	if in.File != "" {
		visit(cleanRel(filepath.ToSlash(in.File)))

		return out, nil
	}

	err = walk(ctx, in.Dir, true, in.Include, visit)

	return out, err
}

func grepFile(abs, rel string, re *regexp.Regexp, limit int) ([]Match, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	reader := bufio.NewReaderSize(f, 64*1024)

	head, _ := reader.Peek(8 * 1024)
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, nil
	}

	var out []Match

	for lineNo := 1; len(out) < limit; lineNo++ {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			text := strings.TrimRight(line, "\r\n")
			if re.MatchString(text) {
				out = append(out, Match{Path: rel, Line: lineNo, Text: text})
			}
		}

		if err == io.EOF {
			break
		}

		if err != nil {
			return out, err
		}
	}

	return out, nil
}

// matchGlob applies ripgrep --glob semantics, including a leading "!" for
// negation.
func matchGlob(pattern, rel string) bool {
	negate := strings.HasPrefix(pattern, "!")
	pattern = strings.TrimPrefix(pattern, "!")

	var ok bool

	switch {
	case strings.HasPrefix(pattern, "/"):
		ok = doublestar.MatchUnvalidated(pattern[1:], rel)
	case strings.Contains(strings.TrimSuffix(pattern, "/"), "/"):
		ok = doublestar.MatchUnvalidated(pattern, rel) || doublestar.MatchUnvalidated("**/"+pattern, rel)
	default:
		ok = doublestar.MatchUnvalidated(pattern, path.Base(rel))
	}

	return ok != negate
}

var ignoreFiles = []string{".gitignore", ".ignore", ".rgignore"}

// walk visits every regular file below root, in lexical order, until fn
// returns false. Like ripgrep's --glob, a path matching override is kept even
// when ignore files exclude it.
func walk(ctx context.Context, root string, hidden bool, override string, fn func(rel string) bool) error {
	var patterns []gitignore.Pattern

	whitelist := override != "" && !strings.HasPrefix(override, "!")

	patterns = append(patterns, readIgnore(filepath.Join(root, ".git", "info", "exclude"), nil)...)

	var visit func(dir string, parts []string, patterns []gitignore.Pattern) (bool, error)

	visit = func(dir string, parts []string, patterns []gitignore.Pattern) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}

		for _, name := range ignoreFiles {
			patterns = append(patterns, readIgnore(filepath.Join(dir, name), parts)...)
		}

		matcher := gitignore.NewMatcher(patterns)

		entries, err := os.ReadDir(dir)
		if err != nil {
			// Unreadable directories are skipped, as ripgrep does.
			return true, nil
		}

		for _, entry := range entries {
			name := entry.Name()
			if name == ".git" {
				continue
			}

			if entry.Type()&os.ModeSymlink != 0 {
				// Symlinks are not followed, matching ripgrep without --follow.
				continue
			}

			child := append(append([]string(nil), parts...), name)
			isDir := entry.IsDir()

			if !isDir && whitelist && matchGlob(override, strings.Join(child, "/")) {
				if entry.Type().IsRegular() && !fn(strings.Join(child, "/")) {
					return false, nil
				}

				continue
			}

			if !hidden && strings.HasPrefix(name, ".") {
				continue
			}

			if matcher.Match(child, isDir) {
				continue
			}

			if isDir {
				cont, err := visit(filepath.Join(dir, name), child, patterns)
				if err != nil || !cont {
					return cont, err
				}

				continue
			}

			if !entry.Type().IsRegular() {
				continue
			}

			if !fn(strings.Join(child, "/")) {
				return false, nil
			}
		}

		return true, nil
	}

	_, err := visit(root, nil, patterns)

	return err
}

func readIgnore(file string, domain []string) []gitignore.Pattern {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}

	var ps []gitignore.Pattern

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}

		ps = append(ps, gitignore.ParsePattern(line, domain))
	}

	return ps
}
