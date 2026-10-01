package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type EditInput struct {
	Path       string `json:"path" jsonschema:"File to edit. Relative paths resolve from the workspace root."`
	OldString  string `json:"oldString" jsonschema:"Exact text to find and replace"`
	NewString  string `json:"newString" jsonschema:"Text to replace oldString with (must differ from oldString)"`
	ReplaceAll bool   `json:"replaceAll,omitempty" jsonschema:"Replace every occurrence of oldString (default false)"`
}

const editDescription = `Edit the contents of a file by finding and replacing exact text.
When editing text from read output, preserve the exact indentation (tabs or spaces) and omit the line-number prefix, such as "1: ". Never include the prefix in oldString or newString.
The edit fails if oldString is not found. By default oldString must identify a UNIQUE location; multiple matches fail unless replaceAll is true. Add more surrounding context to disambiguate, or set replaceAll to true to replace every occurrence (e.g. renaming a variable).
Line endings and a UTF-8 BOM in the file are preserved. Use write to create a file.`

func registerEdit(server *mcp.Server, d *Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "edit",
		Description: editDescription,
		Annotations: editAnnotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in EditInput) (*mcp.CallToolResult, any, error) {
		res, err := d.edit(in)

		return res, nil, err
	})
}

func (d *Deps) edit(in EditInput) (*mcp.CallToolResult, error) {
	if in.OldString == in.NewString {
		return nil, errors.New("no changes to apply: oldString and newString are identical")
	}

	if in.OldString == "" {
		return nil, errors.New("oldString must not be empty; use write to create or overwrite a file")
	}

	abs, err := d.Files.Resolve(in.Path)
	if err != nil {
		return nil, err
	}

	defer lockFile(abs)()

	data, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("file not found: %s", in.Path)
		}

		return nil, fmt.Errorf("unable to edit %s; %w", in.Path, err)
	}

	source := decodeText(data)
	ending := lineEnding(source.text)
	oldString := toLineEnding(in.OldString, ending)
	newString := toLineEnding(in.NewString, ending)

	count := strings.Count(source.text, oldString)
	if count == 0 {
		return nil, errors.New("could not find oldString in the file; it must match exactly, including whitespace and indentation")
	}

	if count > 1 && !in.ReplaceAll {
		return nil, fmt.Errorf("found %d matches for oldString; provide more surrounding context to make it unique or set replaceAll to true", count)
	}

	replaced := source.text
	if in.ReplaceAll {
		replaced = strings.ReplaceAll(replaced, oldString, newString)
	} else {
		replaced = strings.Replace(replaced, oldString, newString, 1)
	}

	if err := writeFileAtomic(abs, textFile{bom: source.bom, text: replaced}.encode()); err != nil {
		return nil, fmt.Errorf("unable to edit %s; %w", in.Path, err)
	}

	name := d.Files.Display(abs)
	st := diff(name, source.text, replaced)

	replacements := 1
	if in.ReplaceAll {
		replacements = count
	}

	return text(fmt.Sprintf("Edited %s (%d replacement(s), +%d -%d)\n%s",
		name, replacements, st.additions, st.deletions, previewDiff(st.patch))), nil
}
