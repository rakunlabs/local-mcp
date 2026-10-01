package tools

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type WriteInput struct {
	Path    string `json:"path" jsonschema:"File to write. Relative paths resolve from the workspace root."`
	Content string `json:"content" jsonschema:"Content to write to the file"`
}

const writeDescription = `Create a text file or completely replace its content. Missing parent directories are created.
An existing UTF-8 BOM is preserved. Use edit instead when only part of an existing file should change.`

func registerWrite(server *mcp.Server, d *Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "write",
		Description: writeDescription,
		Annotations: editAnnotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in WriteInput) (*mcp.CallToolResult, any, error) {
		res, err := d.write(in)

		return res, nil, err
	})
}

func (d *Deps) write(in WriteInput) (*mcp.CallToolResult, error) {
	abs, err := d.Files.Resolve(in.Path)
	if err != nil {
		return nil, err
	}

	defer lockFile(abs)()

	existed := false
	next := decodeText([]byte(in.Content))

	if st, err := os.Stat(abs); err == nil {
		if st.IsDir() {
			return nil, fmt.Errorf("path is a directory: %s", in.Path)
		}

		existed = true

		if old, err := os.ReadFile(abs); err == nil && decodeText(old).bom {
			next.bom = true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("unable to write %s; %w", in.Path, err)
	}

	if err := writeFileAtomic(abs, next.encode()); err != nil {
		return nil, fmt.Errorf("unable to write %s; %w", in.Path, err)
	}

	verb := "Created"
	if existed {
		verb = "Wrote"
	}

	return text(fmt.Sprintf("%s file successfully: %s", verb, d.Files.Display(abs))), nil
}
