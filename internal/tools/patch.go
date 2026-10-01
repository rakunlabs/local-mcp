package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rakunlabs/local-mcp/internal/patch"
)

type PatchInput struct {
	PatchText string `json:"patchText" jsonschema:"The full patch text describing add, update, move and delete operations"`
}

const patchDescription = `Apply one patch that can add, update, move, or delete several files.

*** Begin Patch
*** Add File: path/new.txt
+first line of the new file
*** Update File: path/app.py
*** Move to: path/main.py
@@ def greet():
-print("Hi")
+print("Hello, world!")
*** Delete File: path/obsolete.txt
*** End Patch

- Add File: every following line is a + line (the initial contents).
- Update File: one or more @@ chunks. Text after @@ is an optional context line that is located first. Lines start with " " (context), "-" (remove) or "+" (add). Include a few context lines around each change. "*** End of File" anchors a chunk at the end of the file.
- Move to: optional, directly after Update File, renames the file.
- Delete File: nothing follows.
Paths are relative to the workspace root. Every operation is validated before any file is changed.`

func registerPatch(server *mcp.Server, d *Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "patch",
		Description: patchDescription,
		Annotations: editAnnotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PatchInput) (*mcp.CallToolResult, any, error) {
		res, err := d.patch(in)

		return res, nil, err
	})
}

type preparedChange struct {
	hunk    patch.Hunk
	abs     string
	moveAbs string
	bom     bool
	before  string
	after   string
}

func (d *Deps) patch(in PatchInput) (*mcp.CallToolResult, error) {
	if strings.TrimSpace(in.PatchText) == "" {
		return nil, errors.New("patchText is required")
	}

	hunks, err := patch.Parse(in.PatchText)
	if err != nil {
		return nil, fmt.Errorf("patch verification failed: %w", err)
	}

	if len(hunks) == 0 {
		return nil, errors.New("patch rejected: empty patch")
	}

	changes := make([]preparedChange, 0, len(hunks))
	seen := map[string]bool{}

	for _, h := range hunks {
		c, err := d.preparePatch(h)
		if err != nil {
			return nil, fmt.Errorf("unable to apply patch at %s: %w", h.Path, err)
		}

		for _, p := range []string{c.abs, c.moveAbs} {
			if p == "" {
				continue
			}

			if seen[p] {
				return nil, fmt.Errorf("patch touches %s more than once", d.Files.Display(p))
			}

			seen[p] = true
		}

		changes = append(changes, c)
	}

	for _, c := range changes {
		defer lockFile(c.abs)()
	}

	var (
		applied []string
		diffs   []string
		adds    int
		dels    int
	)

	for _, c := range changes {
		if err := applyChange(c); err != nil {
			if len(applied) == 0 {
				return nil, fmt.Errorf("unable to apply patch at %s: %w", c.hunk.Path, err)
			}

			return nil, fmt.Errorf("patch partially applied before failing at %s: %w\nApplied:\n%s",
				c.hunk.Path, err, strings.Join(applied, "\n"))
		}

		name := d.Files.Display(c.abs)

		var label string

		switch {
		case c.hunk.Kind == patch.Add:
			label = "A " + name
		case c.hunk.Kind == patch.Delete:
			label = "D " + name
		case c.moveAbs != "":
			label = fmt.Sprintf("R %s -> %s", name, d.Files.Display(c.moveAbs))
		default:
			label = "M " + name
		}

		applied = append(applied, label)

		st := diff(name, c.before, c.after)
		adds += st.additions
		dels += st.deletions

		if st.patch != "" {
			diffs = append(diffs, strings.TrimRight(st.patch, "\n"))
		}
	}

	out := fmt.Sprintf("Applied patch (+%d -%d):\n%s", adds, dels, strings.Join(applied, "\n"))
	if len(diffs) > 0 {
		out += "\n" + previewDiff(strings.Join(diffs, "\n"))
	}

	return text(out), nil
}

func (d *Deps) preparePatch(h patch.Hunk) (preparedChange, error) {
	abs, err := d.Files.Resolve(h.Path)
	if err != nil {
		return preparedChange{}, err
	}

	c := preparedChange{hunk: h, abs: abs}

	if h.Kind == patch.Add {
		if _, err := os.Stat(abs); err == nil {
			return c, errors.New("file already exists; use Update File to change it")
		}

		c.after = h.Contents
		if c.after != "" && !strings.HasSuffix(c.after, "\n") {
			c.after += "\n"
		}

		return c, nil
	}

	st, err := os.Stat(abs)
	if err != nil {
		return c, fmt.Errorf("file not found")
	}

	if !st.Mode().IsRegular() {
		return c, fmt.Errorf("not a regular file")
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return c, err
	}

	source := decodeText(data)
	c.bom = source.bom
	c.before = source.text

	if h.Kind == patch.Delete {
		return c, nil
	}

	c.after = source.text
	if len(h.Chunks) > 0 {
		c.after, err = patch.Derive(h.Path, h.Chunks, source.text)
		if err != nil {
			return c, err
		}
	}

	if h.MovePath != "" {
		c.moveAbs, err = d.Files.Resolve(h.MovePath)
		if err != nil {
			return c, err
		}

		if c.moveAbs == abs {
			c.moveAbs = ""
		} else if _, err := os.Stat(c.moveAbs); err == nil {
			return c, fmt.Errorf("move target %s already exists", h.MovePath)
		}
	}

	return c, nil
}

func applyChange(c preparedChange) error {
	switch c.hunk.Kind {
	case patch.Add:
		return writeFileAtomic(c.abs, []byte(c.after))
	case patch.Delete:
		return os.Remove(c.abs)
	}

	data := textFile{bom: c.bom, text: c.after}.encode()

	if c.moveAbs == "" {
		return writeFileAtomic(c.abs, data)
	}

	if err := writeFileAtomic(c.moveAbs, data); err != nil {
		return err
	}

	if st, err := os.Stat(c.abs); err == nil {
		_ = os.Chmod(c.moveAbs, st.Mode().Perm())
	}

	return os.Remove(c.abs)
}
