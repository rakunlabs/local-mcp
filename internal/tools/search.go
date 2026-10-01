package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rakunlabs/local-mcp/internal/search"
)

const (
	searchDefaultLimit = 100
	searchTimeout      = 30 * time.Second
	grepMaxLineLength  = 2000
)

type GlobInput struct {
	Pattern string `json:"pattern" jsonschema:"Glob pattern to match files against, e.g. **/*.go or src/**/*.ts"`
	Path    string `json:"path,omitempty" jsonschema:"Directory to search. Defaults to the workspace root."`
	Hidden  bool   `json:"hidden,omitempty" jsonschema:"Include hidden files and directories (default false)"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum number of matching files to return (default 100)"`
}

const globDescription = `Search file paths using a glob pattern (examples: "**/*.go", "src/**/*.tsx").
A pattern without a slash matches the file name at any depth. Files ignored by .gitignore are skipped.
Returns matching file paths, one per line.`

func registerGlob(server *mcp.Server, d *Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "glob",
		Description: globDescription,
		Annotations: readOnlyAnnotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GlobInput) (*mcp.CallToolResult, any, error) {
		res, err := d.glob(ctx, in)

		return res, nil, err
	})
}

func (d *Deps) glob(ctx context.Context, in GlobInput) (*mcp.CallToolResult, error) {
	if strings.TrimSpace(in.Pattern) == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}

	dir, err := d.Readable.Resolve(in.Path)
	if err != nil {
		return nil, err
	}

	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", in.Path)
	}

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	files, err := d.Search.Glob(ctx, search.GlobInput{
		Dir:     dir,
		Pattern: in.Pattern,
		Hidden:  in.Hidden,
		Limit:   limit,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to find files matching %s; %w", in.Pattern, err)
	}

	if len(files) == 0 {
		return text("No files found"), nil
	}

	truncated := len(files) > limit
	if truncated {
		files = files[:limit]
	}

	lines := make([]string, len(files))
	for i, f := range files {
		lines[i] = d.Readable.Display(filepath.Join(dir, filepath.FromSlash(f)))
	}

	out := strings.Join(lines, "\n")
	if truncated {
		out += fmt.Sprintf("\n\n(Results are truncated at %d files. Use a more specific path or pattern, or raise limit.)", limit)
	}

	return text(out), nil
}

type GrepInput struct {
	Pattern       string `json:"pattern" jsonschema:"Regular expression (RE2 / ripgrep syntax) or literal text to match in file contents"`
	Path          string `json:"path,omitempty" jsonschema:"File or directory to search. Defaults to the workspace root."`
	Include       string `json:"include,omitempty" jsonschema:"Glob pattern to filter files, e.g. *.go or *.{ts,tsx}"`
	Literal       bool   `json:"literal,omitempty" jsonschema:"Treat pattern as exact text instead of a regular expression (default false)"`
	CaseSensitive *bool  `json:"caseSensitive,omitempty" jsonschema:"Use case-sensitive matching (default true)"`
	Limit         int    `json:"limit,omitempty" jsonschema:"Maximum number of matching lines to return (default 100)"`
}

const grepDescription = `Search file contents using regular expressions or literal text.
Narrow the search with path and include. Files ignored by .gitignore are skipped; hidden files are searched.
Returns matching file paths, line numbers, and line previews, grouped by file.`

func registerGrep(server *mcp.Server, d *Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "grep",
		Description: grepDescription,
		Annotations: readOnlyAnnotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GrepInput) (*mcp.CallToolResult, any, error) {
		res, err := d.grep(ctx, in)

		return res, nil, err
	})
}

func (d *Deps) grep(ctx context.Context, in GrepInput) (*mcp.CallToolResult, error) {
	if in.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}

	target, err := d.Readable.Resolve(in.Path)
	if err != nil {
		return nil, err
	}

	st, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("path not found: %s", in.Path)
	}

	dir, file := target, ""
	if !st.IsDir() {
		dir, file = filepath.Dir(target), filepath.Base(target)
	}

	caseSensitive := in.CaseSensitive == nil || *in.CaseSensitive

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	matches, err := d.Search.Grep(ctx, search.GrepInput{
		Dir:           dir,
		File:          file,
		Pattern:       in.Pattern,
		Literal:       in.Literal,
		CaseSensitive: caseSensitive,
		Include:       in.Include,
		Limit:         limit,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to grep for %s; %w", in.Pattern, err)
	}

	if len(matches) == 0 {
		return text("No matches found"), nil
	}

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}

	var b strings.Builder

	fmt.Fprintf(&b, "Found %d matches", len(matches))
	if truncated {
		fmt.Fprintf(&b, " (truncated at limit %d)", limit)
	}

	current := ""

	for _, m := range matches {
		if m.Path != current {
			current = m.Path
			fmt.Fprintf(&b, "\n\n%s:", d.Readable.Display(filepath.Join(dir, filepath.FromSlash(m.Path))))
		}

		line := m.Text
		if len(line) > grepMaxLineLength {
			line = line[:grepMaxLineLength] + "..."
		}

		fmt.Fprintf(&b, "\n  Line %d: %s", m.Line, line)
	}

	return text(d.Output.Truncate(b.String(), 0).Content), nil
}
