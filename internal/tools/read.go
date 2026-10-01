package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	readMaxLines      = 2000
	readMaxBytes      = 50 * 1024
	readMaxLineLength = 2000
	readMaxMediaBytes = 20 * 1024 * 1024
)

var readLineSuffix = fmt.Sprintf("... (line truncated to %d chars)", readMaxLineLength)

type ReadInput struct {
	Path   string `json:"path" jsonschema:"File or directory to read. Relative paths resolve from the workspace root."`
	Offset int    `json:"offset,omitempty" jsonschema:"The 1-based line or directory entry to start reading from"`
	Limit  int    `json:"limit,omitempty" jsonschema:"The maximum number of lines or directory entries to read (defaults to and capped at 2000)"`
}

const readDescription = `Read the contents of a file or directory. Supports text files, images, and PDFs.
Each text line is prefixed by its 1-based line number as <line>: <content>. The prefix is for reference and is not part of the file content.
Directory entries are returned one per line, sorted with directories first and marked with a trailing /.
Use offset and limit to read large files or directories in sections. Text pages hold at most 2000 lines and 50 KiB; lines longer than 2000 characters are shortened.
Prefer one larger read over many small slices, and use grep to find specific content in large files.`

func registerRead(server *mcp.Server, d *Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read",
		Description: readDescription,
		Annotations: readOnlyAnnotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ReadInput) (*mcp.CallToolResult, any, error) {
		res, err := d.read(in)

		return res, nil, err
	})
}

func (d *Deps) read(in ReadInput) (*mcp.CallToolResult, error) {
	if in.Offset < 0 || in.Limit < 0 {
		return nil, errors.New("offset and limit must be positive")
	}

	if in.Limit > readMaxLines {
		in.Limit = readMaxLines
	}

	abs, err := d.Readable.Resolve(in.Path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("file not found: %s%s", in.Path, d.suggest(abs))
		}

		return nil, fmt.Errorf("unable to read %s; %w", in.Path, err)
	}

	if info.IsDir() {
		return d.readDir(abs, in)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a file or directory: %s", in.Path)
	}

	return readFile(abs, in, info.Size())
}

func (d *Deps) readDir(abs string, in ReadInput) (*mcp.CallToolResult, error) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("unable to list %s; %w", in.Path, err)
	}

	type item struct {
		name string
		dir  bool
	}

	items := make([]item, 0, len(entries))

	for _, entry := range entries {
		full := filepath.Join(abs, entry.Name())

		target, err := filepath.EvalSymlinks(full)
		if err != nil || !d.Readable.Contains(target) {
			continue
		}

		st, err := os.Stat(target)
		if err != nil {
			continue
		}

		if st.IsDir() {
			items = append(items, item{entry.Name() + "/", true})
		} else if st.Mode().IsRegular() {
			items = append(items, item{entry.Name(), false})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].dir != items[j].dir {
			return items[i].dir
		}

		return items[i].name < items[j].name
	})

	offset := max(in.Offset, 1)
	limit := in.Limit
	if limit == 0 {
		limit = readMaxLines
	}

	start := min(offset-1, len(items))
	end := min(start+limit, len(items))

	var b strings.Builder
	for _, it := range items[start:end] {
		b.WriteString(it.name)
		b.WriteByte('\n')
	}

	if len(items) == 0 {
		b.WriteString("(empty directory)\n")
	}

	if end < len(items) {
		fmt.Fprintf(&b, "\n(Showing entries %d-%d of %d. Use offset=%d to continue.)", start+1, end, len(items), end+1)
	} else if start > 0 || end > 0 {
		fmt.Fprintf(&b, "\n(%d entries)", len(items))
	}

	return text(strings.TrimRight(b.String(), "\n")), nil
}

var binaryExtensions = map[string]bool{
	".zip": true, ".tar": true, ".gz": true, ".exe": true, ".dll": true, ".so": true,
	".class": true, ".jar": true, ".war": true, ".7z": true, ".doc": true, ".docx": true,
	".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true, ".odt": true, ".ods": true,
	".odp": true, ".bin": true, ".dat": true, ".obj": true, ".o": true, ".a": true,
	".lib": true, ".wasm": true, ".pyc": true, ".pyo": true,
}

func mediaType(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png"
	case bytes.HasPrefix(head, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case bytes.HasPrefix(head, []byte("GIF8")):
		return "image/gif"
	case len(head) >= 12 && bytes.HasPrefix(head, []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return "image/webp"
	case bytes.HasPrefix(head, []byte("%PDF")):
		return "application/pdf"
	}

	return ""
}

func looksBinary(b []byte) bool {
	if len(b) == 0 {
		return false
	}

	nonPrintable := 0

	for _, c := range b {
		if c == 0 {
			return true
		}

		if c < 9 || (c > 13 && c < 32) {
			nonPrintable++
		}
	}

	return float64(nonPrintable)/float64(len(b)) > 0.3
}

func readFile(abs string, in ReadInput, size int64) (*mcp.CallToolResult, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("unable to read %s; %w", in.Path, err)
	}
	defer func() { _ = f.Close() }()

	reader := bufio.NewReaderSize(f, 64*1024)
	head, _ := reader.Peek(8 * 1024)

	if mt := mediaType(head); mt != "" {
		if size > readMaxMediaBytes {
			return nil, fmt.Errorf("media exceeds %d byte limit: %s", readMaxMediaBytes, in.Path)
		}

		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("unable to read %s; %w", in.Path, err)
		}

		if strings.HasPrefix(mt, "image/") {
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "Image read successfully: " + in.Path},
				&mcp.ImageContent{Data: data, MIMEType: mt},
			}}, nil
		}

		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "PDF read successfully: " + in.Path},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
				URI:      "file://" + filepath.ToSlash(abs),
				MIMEType: mt,
				Blob:     data,
			}},
		}}, nil
	}

	if binaryExtensions[strings.ToLower(filepath.Ext(abs))] || looksBinary(head) {
		return nil, fmt.Errorf("cannot read binary file: %s", in.Path)
	}

	offset := max(in.Offset, 1)
	limit := in.Limit
	if limit == 0 {
		limit = readMaxLines
	}

	var (
		b     strings.Builder
		bytes int
		line  int
		kept  int
		next  int
	)

	for {
		raw, err := reader.ReadString('\n')
		if raw == "" && err != nil {
			if err != io.EOF {
				return nil, fmt.Errorf("unable to read %s; %w", in.Path, err)
			}

			break
		}

		line++

		if line < offset {
			continue
		}

		if kept >= limit {
			next = line

			break
		}

		content := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
		if strings.IndexByte(content, 0) >= 0 {
			return nil, fmt.Errorf("cannot read binary file: %s", in.Path)
		}

		if !utf8.ValidString(content) {
			return nil, fmt.Errorf("file is not valid UTF-8: %s", in.Path)
		}

		if utf8.RuneCountInString(content) > readMaxLineLength {
			content = string([]rune(content)[:readMaxLineLength]) + readLineSuffix
		}

		entry := fmt.Sprintf("%d: %s\n", line, content)
		if bytes+len(entry) > readMaxBytes {
			next = line

			break
		}

		b.WriteString(entry)
		bytes += len(entry)
		kept++

		if err == io.EOF {
			break
		}
	}

	if kept == 0 && offset > 1 {
		return nil, fmt.Errorf("offset %d is out of range for this file (%d lines)", offset, line)
	}

	out := strings.TrimSuffix(b.String(), "\n")

	switch {
	case next > 0:
		out += fmt.Sprintf("\n\n(Showing lines %d-%d. File has more lines; use offset=%d to continue.)", offset, next-1, next)
	case kept == 0:
		out = "(empty file)"
	default:
		out += fmt.Sprintf("\n\n(End of file - total %d lines)", line)
	}

	return text(out), nil
}

// suggest lists files in the parent directory with a similar name, so a
// typo'd path is easy to correct.
func (d *Deps) suggest(abs string) string {
	dir, base := filepath.Dir(abs), strings.ToLower(filepath.Base(abs))

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var hits []string

	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if strings.Contains(name, base) || strings.Contains(base, name) {
			hits = append(hits, d.Readable.Display(filepath.Join(dir, e.Name())))
		}

		if len(hits) == 3 {
			break
		}
	}

	if len(hits) == 0 {
		return ""
	}

	return "\n\nDid you mean one of these?\n" + strings.Join(hits, "\n")
}
