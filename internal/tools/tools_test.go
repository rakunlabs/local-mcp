package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rakunlabs/local-mcp/internal/config"
	"github.com/rakunlabs/local-mcp/internal/output"
	"github.com/rakunlabs/local-mcp/internal/search"
	"github.com/rakunlabs/local-mcp/internal/workspace"
)

type harness struct {
	t       *testing.T
	root    string
	outside string
	session *mcp.ClientSession
}

func newHarness(t *testing.T, mutate ...func(*config.Config)) *harness {
	t.Helper()

	root := t.TempDir()
	outside := t.TempDir()

	cfg := &config.Config{
		Search: config.Search{Engine: "go"},
		Shell:  config.Shell{DefaultTimeoutMS: 10_000},
		Output: config.Output{Dir: t.TempDir(), MaxLines: 2000, MaxBytes: 50 * 1024},
	}
	for _, m := range mutate {
		m(cfg)
	}

	files, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}

	store, err := output.New(cfg.Output.Dir, cfg.Output.MaxLines, cfg.Output.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}

	readable, err := workspace.New(root, store.Dir())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	jobs := NewJobs(ctx, store)
	t.Cleanup(jobs.Close)

	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	if _, err := Register(server, &Deps{
		Files: files, Readable: readable, Output: store, Search: search.Native{}, Jobs: jobs, Config: cfg,
	}); err != nil {
		t.Fatal(err)
	}

	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "client"}, nil)

	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = session.Close() })

	return &harness{t: t, root: root, outside: outside, session: session}
}

func (h *harness) file(name, content string) string {
	h.t.Helper()

	p := filepath.Join(h.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}

	return p
}

func (h *harness) read(name string) string {
	h.t.Helper()

	data, err := os.ReadFile(filepath.Join(h.root, filepath.FromSlash(name)))
	if err != nil {
		h.t.Fatal(err)
	}

	return string(data)
}

// call returns the text of the result and whether it is an error.
func (h *harness) call(name string, args map[string]any) (string, bool) {
	h.t.Helper()

	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("call %s: %v", name, err)
	}

	var parts []string

	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}

	return strings.Join(parts, "\n"), res.IsError
}

func (h *harness) ok(name string, args map[string]any) string {
	h.t.Helper()

	out, isErr := h.call(name, args)
	if isErr {
		h.t.Fatalf("%s returned error: %s", name, out)
	}

	return out
}

func (h *harness) fail(name string, args map[string]any, contains string) {
	h.t.Helper()

	out, isErr := h.call(name, args)
	if !isErr {
		h.t.Fatalf("%s: expected error, got %s", name, out)
	}

	if !strings.Contains(out, contains) {
		h.t.Fatalf("%s: error %q does not contain %q", name, out, contains)
	}
}

func TestRead(t *testing.T) {
	h := newHarness(t)
	h.file("a.txt", "one\ntwo\r\nthree\n")
	h.file("dir/sub/x", "")
	h.file("dir/b.txt", "")

	out := h.ok("read", map[string]any{"path": "a.txt"})
	if !strings.HasPrefix(out, "1: one\n2: two\n3: three\n") || !strings.Contains(out, "total 3 lines") {
		t.Fatalf("read: %q", out)
	}

	out = h.ok("read", map[string]any{"path": "a.txt", "offset": 2, "limit": 1})
	if !strings.HasPrefix(out, "2: two\n") || !strings.Contains(out, "offset=3") {
		t.Fatalf("paged read: %q", out)
	}

	h.fail("read", map[string]any{"path": "a.txt", "offset": 10}, "out of range")

	out = h.ok("read", map[string]any{"path": "dir"})
	if !strings.HasPrefix(out, "sub/\nb.txt") {
		t.Fatalf("dir: %q", out)
	}

	h.fail("read", map[string]any{"path": "missing.txt"}, "file not found")
	h.fail("read", map[string]any{"path": filepath.Join(h.outside, "x")}, "outside")
	h.fail("read", map[string]any{"path": "../escape"}, "outside")

	h.file("bin.o", "abc")
	h.fail("read", map[string]any{"path": "bin.o"}, "binary")

	h.file("long.txt", strings.Repeat("x", 2500))
	if out := h.ok("read", map[string]any{"path": "long.txt"}); !strings.Contains(out, "line truncated to 2000") {
		t.Fatalf("long line not truncated")
	}
}

func TestReadSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}

	h := newHarness(t)
	secret := filepath.Join(h.outside, "secret")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(secret, filepath.Join(h.root, "link")); err != nil {
		t.Fatal(err)
	}

	h.fail("read", map[string]any{"path": "link"}, "outside")
	h.fail("write", map[string]any{"path": "link", "content": "y"}, "outside")
}

func TestGlobGrep(t *testing.T) {
	h := newHarness(t)
	h.file("a.go", "package a\nfunc Hello() {}\n")
	h.file("sub/b.go", "package sub\n// hello\n")
	h.file("c.md", "Hello\n")

	out := h.ok("glob", map[string]any{"pattern": "**/*.go"})
	if out != "a.go\nsub/b.go" {
		t.Fatalf("glob: %q", out)
	}

	if out := h.ok("glob", map[string]any{"pattern": "*.rs"}); out != "No files found" {
		t.Fatalf("glob none: %q", out)
	}

	out = h.ok("glob", map[string]any{"pattern": "*", "limit": 1})
	if !strings.Contains(out, "truncated at 1") {
		t.Fatalf("glob limit: %q", out)
	}

	out = h.ok("grep", map[string]any{"pattern": "hello", "include": "*.go"})
	if !strings.Contains(out, "Found 1 matches") || !strings.Contains(out, "sub/b.go:\n  Line 2: // hello") {
		t.Fatalf("grep: %q", out)
	}

	out = h.ok("grep", map[string]any{"pattern": "hello", "caseSensitive": false})
	if !strings.Contains(out, "Found 3 matches") {
		t.Fatalf("grep insensitive: %q", out)
	}

	out = h.ok("grep", map[string]any{"pattern": "Hello()", "literal": true, "path": "a.go"})
	if !strings.Contains(out, "a.go:\n  Line 2: func Hello() {}") {
		t.Fatalf("grep literal: %q", out)
	}

	h.fail("grep", map[string]any{"pattern": "("}, "invalid pattern")
}

func TestEdit(t *testing.T) {
	h := newHarness(t)
	h.file("a.txt", "\ufeffalpha\r\nbeta\r\nbeta\r\n")

	h.fail("edit", map[string]any{"path": "a.txt", "oldString": "beta", "newString": "x"}, "found 2 matches")
	h.fail("edit", map[string]any{"path": "a.txt", "oldString": "gamma", "newString": "x"}, "could not find")
	h.fail("edit", map[string]any{"path": "a.txt", "oldString": "a", "newString": "a"}, "identical")
	h.fail("edit", map[string]any{"path": "a.txt", "oldString": "", "newString": "a"}, "must not be empty")

	out := h.ok("edit", map[string]any{"path": "a.txt", "oldString": "alpha\nbeta", "newString": "ALPHA\nBETA"})
	if !strings.Contains(out, "Edited a.txt") || !strings.Contains(out, "+ALPHA") {
		t.Fatalf("edit: %q", out)
	}

	if got := h.read("a.txt"); got != "\ufeffALPHA\r\nBETA\r\nbeta\r\n" {
		t.Fatalf("content: %q", got)
	}

	h.ok("edit", map[string]any{"path": "a.txt", "oldString": "A", "newString": "a", "replaceAll": true})

	if got := h.read("a.txt"); got != "\ufeffaLPHa\r\nBETa\r\nbeta\r\n" {
		t.Fatalf("replaceAll: %q", got)
	}
}

func TestWrite(t *testing.T) {
	h := newHarness(t)

	if out := h.ok("write", map[string]any{"path": "new/dir/f.txt", "content": "hi"}); !strings.Contains(out, "Created") {
		t.Fatalf("write: %q", out)
	}

	if out := h.ok("write", map[string]any{"path": "new/dir/f.txt", "content": "bye"}); !strings.Contains(out, "Wrote") {
		t.Fatalf("overwrite: %q", out)
	}

	if h.read("new/dir/f.txt") != "bye" {
		t.Fatal("content")
	}

	h.fail("write", map[string]any{"path": filepath.Join(h.outside, "x"), "content": ""}, "outside")
	h.fail("write", map[string]any{"path": "new", "content": ""}, "directory")
}

func TestPatch(t *testing.T) {
	h := newHarness(t)
	h.file("app.py", "import os\n\ndef greet():\n    print(\"Hi\")\n")
	h.file("old.txt", "bye\n")

	out := h.ok("patch", map[string]any{"patchText": `*** Begin Patch
*** Add File: docs/new.md
+# Title
*** Update File: app.py
*** Move to: src/main.py
@@ def greet():
-    print("Hi")
+    print("Hello")
*** Delete File: old.txt
*** End Patch`})

	for _, want := range []string{"A docs/new.md", "R app.py -> src/main.py", "D old.txt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("patch output missing %q: %s", want, out)
		}
	}

	if h.read("docs/new.md") != "# Title\n" {
		t.Fatal("add")
	}

	if got := h.read("src/main.py"); got != "import os\n\ndef greet():\n    print(\"Hello\")\n" {
		t.Fatalf("update: %q", got)
	}

	for _, gone := range []string{"app.py", "old.txt"} {
		if _, err := os.Stat(filepath.Join(h.root, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone", gone)
		}
	}

	// A failing hunk must leave every file untouched.
	h.fail("patch", map[string]any{"patchText": `*** Begin Patch
*** Add File: z.txt
+z
*** Update File: src/main.py
@@
-not there
+x
*** End Patch`}, "failed to find")

	if _, err := os.Stat(filepath.Join(h.root, "z.txt")); !os.IsNotExist(err) {
		t.Fatal("z.txt must not be created when validation fails")
	}

	h.fail("patch", map[string]any{"patchText": "*** Begin Patch\n*** Add File: ../x\n+x\n*** End Patch"}, "outside")
}

func TestShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell")
	}

	h := newHarness(t, func(c *config.Config) { c.Output.MaxLines = 50 })
	h.file("sub/f", "")

	out := h.ok("shell", map[string]any{"command": "pwd; echo err >&2", "workdir": "sub"})
	if !strings.Contains(out, "/sub\n") || !strings.Contains(out, "err") || !strings.Contains(out, "Exit code: 0") {
		t.Fatalf("shell: %q", out)
	}

	out, isErr := h.call("shell", map[string]any{"command": "exit 3"})
	if !isErr || !strings.Contains(out, "Exit code: 3") {
		t.Fatalf("exit code: %q", out)
	}

	start := time.Now()

	out, _ = h.call("shell", map[string]any{"command": "sleep 30 & sleep 30", "timeout": 300})
	if !strings.Contains(out, "exceeding the timeout") || time.Since(start) > 8*time.Second {
		t.Fatalf("timeout: %q after %s", out, time.Since(start))
	}

	out = h.ok("shell", map[string]any{"command": "seq 1 200"})
	if !strings.Contains(out, "Full output saved to:") || !strings.Contains(out, "\n200\n") {
		t.Fatalf("truncation: %q", out)
	}

	saved := strings.TrimSpace(strings.SplitN(strings.SplitN(out, "Full output saved to: ", 2)[1], "\n", 2)[0])
	if got := h.ok("read", map[string]any{"path": saved, "limit": 1}); !strings.HasPrefix(got, "1: 1") {
		t.Fatalf("saved output not readable: %q", got)
	}

	h.fail("shell", map[string]any{"command": "true", "workdir": h.outside}, "outside")
}

func TestShellBackground(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell")
	}

	h := newHarness(t)

	out := h.ok("shell", map[string]any{"command": "echo started; sleep 0.2; echo done", "background": true})
	if !strings.Contains(out, "job_1") {
		t.Fatalf("background: %q", out)
	}

	out = h.ok("shell_job", map[string]any{"action": "wait", "id": "job_1", "timeout": 5000})
	if !strings.Contains(out, "[exited]") || !strings.Contains(out, "exit=0") || !strings.Contains(out, "done") {
		t.Fatalf("wait: %q", out)
	}

	h.ok("shell", map[string]any{"command": "sleep 60", "background": true})

	out = h.ok("shell_job", map[string]any{"action": "kill", "id": "job_2"})
	if !strings.Contains(out, "[killed]") {
		t.Fatalf("kill: %q", out)
	}

	if out := h.ok("shell_job", map[string]any{"action": "list"}); strings.Count(out, "job_") != 2 {
		t.Fatalf("list: %q", out)
	}

	h.fail("shell_job", map[string]any{"action": "status", "id": "job_9"}, "unknown job")
}

func TestReadOnly(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.ReadOnly = true })

	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}

	if strings.Join(names, ",") != "glob,grep,read" {
		t.Fatalf("read-only tools: %v", names)
	}
}
