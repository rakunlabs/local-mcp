// Package patch parses and applies the "*** Begin Patch" file-oriented patch
// format used by OpenCode's patch tool.
package patch

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Kind string

const (
	Add    Kind = "add"
	Delete Kind = "delete"
	Update Kind = "update"
)

type Hunk struct {
	Kind     Kind
	Path     string
	MovePath string
	Contents string
	Chunks   []Chunk
}

type Chunk struct {
	OldLines      []string
	NewLines      []string
	ChangeContext string
	EndOfFile     bool
}

var heredoc = regexp.MustCompile(`(?s)^(?:cat\s+)?<<['"]?(\w+)['"]?\s*\n(.*?)\n(\w+)\s*$`)

func stripHeredoc(s string) string {
	if m := heredoc.FindStringSubmatch(s); m != nil && m[1] == m[3] {
		return m[2]
	}

	return s
}

func Parse(text string) ([]Hunk, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(stripHeredoc(strings.TrimSpace(text)), "\n")

	begin, end := -1, -1

	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case "*** Begin Patch":
			if begin == -1 {
				begin = i
			}
		case "*** End Patch":
			end = i
		}
	}

	if begin == -1 || end == -1 || begin >= end {
		return nil, errors.New("invalid patch format: missing *** Begin Patch / *** End Patch markers")
	}

	var hunks []Hunk

	i := begin + 1
	for i < end {
		line := lines[i]

		switch {
		case strings.HasPrefix(line, "*** Add File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))
			if path == "" {
				return nil, errors.New("invalid add file path")
			}

			var content []string

			i++
			for i < end && !strings.HasPrefix(lines[i], "***") {
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("invalid add file line (must start with +): %s", lines[i])
				}

				content = append(content, lines[i][1:])
				i++
			}

			hunks = append(hunks, Hunk{Kind: Add, Path: path, Contents: strings.Join(content, "\n")})
		case strings.HasPrefix(line, "*** Delete File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))
			if path == "" {
				return nil, errors.New("invalid delete file path")
			}

			hunks = append(hunks, Hunk{Kind: Delete, Path: path})
			i++
		case strings.HasPrefix(line, "*** Update File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
			if path == "" {
				return nil, errors.New("invalid update file path")
			}

			h := Hunk{Kind: Update, Path: path}

			i++
			if i < end && strings.HasPrefix(lines[i], "*** Move to:") {
				h.MovePath = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to:"))
				if h.MovePath == "" {
					return nil, errors.New("invalid move file path")
				}

				i++
			}

			chunks, next, err := parseUpdate(lines, i, end)
			if err != nil {
				return nil, err
			}

			if len(chunks) == 0 && h.MovePath == "" {
				return nil, fmt.Errorf("invalid update hunk for %s: expected at least one @@ chunk", path)
			}

			h.Chunks = chunks
			hunks = append(hunks, h)
			i = next
		case strings.TrimSpace(line) == "":
			i++
		default:
			return nil, fmt.Errorf("invalid patch line: %s", line)
		}
	}

	return hunks, nil
}

func parseUpdate(lines []string, i, end int) ([]Chunk, int, error) {
	var chunks []Chunk

	for i < end && !strings.HasPrefix(lines[i], "***") {
		if !strings.HasPrefix(lines[i], "@@") {
			return nil, i, fmt.Errorf("invalid update file line (expected @@): %s", lines[i])
		}

		c := Chunk{ChangeContext: strings.TrimSpace(lines[i][2:])}

		i++
		for i < end && !strings.HasPrefix(lines[i], "@@") {
			line := lines[i]
			if line == "*** End of File" {
				c.EndOfFile = true
				i++

				break
			}

			if strings.HasPrefix(line, "***") {
				break
			}

			switch {
			case strings.HasPrefix(line, " "):
				c.OldLines = append(c.OldLines, line[1:])
				c.NewLines = append(c.NewLines, line[1:])
			case strings.HasPrefix(line, "-"):
				c.OldLines = append(c.OldLines, line[1:])
			case strings.HasPrefix(line, "+"):
				c.NewLines = append(c.NewLines, line[1:])
			case line == "":
				// A blank context line whose leading space was stripped.
				c.OldLines = append(c.OldLines, "")
				c.NewLines = append(c.NewLines, "")
			default:
				return nil, i, fmt.Errorf("invalid update chunk line: %s", line)
			}

			i++
		}

		chunks = append(chunks, c)
	}

	return chunks, i, nil
}

type replacement struct {
	start  int
	remove int
	insert []string
}

// Derive applies update chunks to the original text (without BOM). The result
// ends with a newline and uses the original line endings.
func Derive(path string, chunks []Chunk, original string) (string, error) {
	ending := "\n"
	if strings.Contains(original, "\r\n") {
		ending = "\r\n"
	}

	lines := strings.Split(strings.ReplaceAll(original, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var reps []replacement

	idx := 0

	for _, c := range chunks {
		if c.ChangeContext != "" {
			at := seek(lines, []string{c.ChangeContext}, idx, false)
			if at == -1 {
				return "", fmt.Errorf("failed to find context %q in %s", c.ChangeContext, path)
			}

			idx = at + 1
		}

		if len(c.OldLines) == 0 {
			reps = append(reps, replacement{start: len(lines), insert: c.NewLines})

			continue
		}

		oldLines, newLines := c.OldLines, c.NewLines

		found := seek(lines, oldLines, idx, c.EndOfFile)
		if found == -1 && oldLines[len(oldLines)-1] == "" {
			oldLines = oldLines[:len(oldLines)-1]
			if len(newLines) > 0 && newLines[len(newLines)-1] == "" {
				newLines = newLines[:len(newLines)-1]
			}

			found = seek(lines, oldLines, idx, c.EndOfFile)
		}

		if found == -1 {
			return "", fmt.Errorf("failed to find expected lines in %s:\n%s", path, strings.Join(c.OldLines, "\n"))
		}

		reps = append(reps, replacement{start: found, remove: len(oldLines), insert: newLines})
		idx = found + len(oldLines)
	}

	// Chunks are located in order, so reps are already sorted by start.
	for i := len(reps) - 1; i >= 0; i-- {
		r := reps[i]
		tail := append([]string(nil), lines[r.start+r.remove:]...)
		lines = append(append(lines[:r.start], r.insert...), tail...)
	}

	return strings.Join(lines, ending) + ending, nil
}

func seek(lines, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 {
		return -1
	}

	for _, cmp := range []func(a, b string) bool{exact, rstrip, trim, normalized} {
		if eof {
			off := len(lines) - len(pattern)
			if off >= start && matches(lines, pattern, off, cmp) {
				return off
			}
		}

		for off := start; off <= len(lines)-len(pattern); off++ {
			if matches(lines, pattern, off, cmp) {
				return off
			}
		}
	}

	return -1
}

func matches(lines, pattern []string, off int, cmp func(a, b string) bool) bool {
	for i, p := range pattern {
		if !cmp(lines[off+i], p) {
			return false
		}
	}

	return true
}

func exact(a, b string) bool  { return a == b }
func rstrip(a, b string) bool { return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") }
func trim(a, b string) bool   { return strings.TrimSpace(a) == strings.TrimSpace(b) }
func normalized(a, b string) bool {
	return normalize(strings.TrimSpace(a)) == normalize(strings.TrimSpace(b))
}

var normalizer = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", "\u201a", "'", "\u201b", "'",
	"\u201c", `"`, "\u201d", `"`, "\u201e", `"`, "\u201f", `"`,
	"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-",
	"\u2026", "...", "\u00a0", " ",
)

func normalize(s string) string { return normalizer.Replace(s) }
