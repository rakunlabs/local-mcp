package search

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	files := map[string]string{
		".gitignore":        "ignored/\n*.log\n",
		"main.go":           "package main\n\nfunc main() {\n\tprintln(\"Hello\")\n}\n",
		"pkg/a/a.go":        "package a\n// TODO: hello\n",
		"pkg/a/a_test.go":   "package a\n",
		"web/app.ts":        "const hello = 1\n",
		"web/app.tsx":       "export {}\n",
		"ignored/x.go":      "package hello\n",
		"debug.log":         "hello\n",
		".hidden/secret.go": "package hello\n",
		"bin.dat":           "hel\x00lo",
		"pkg/a/.ignore":     "a_test.go\n",
		"pkg/b/nested/b.go": "package b // hello\n",
		".git/config":       "hello\n",
	}

	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func engines(t *testing.T) []Engine {
	out := []Engine{Native{}}
	if p, err := exec.LookPath("rg"); err == nil {
		out = append(out, Ripgrep{Binary: p})
	}

	return out
}

func TestGlob(t *testing.T) {
	dir := fixture(t)

	// --glob is an override in ripgrep: a matching file is listed even when an
	// ignore file or the hidden rule would skip it, but ignored directories
	// are still not entered.
	all := []string{"main.go", "pkg/a/a.go", "pkg/a/a_test.go", "pkg/b/nested/b.go"}
	cases := []struct {
		pattern string
		hidden  bool
		want    []string
	}{
		{"*.go", false, all},
		{"**/*.go", false, all},
		{"pkg/**/*.go", false, []string{"pkg/a/a.go", "pkg/a/a_test.go", "pkg/b/nested/b.go"}},
		{"*.{ts,tsx}", false, []string{"web/app.ts", "web/app.tsx"}},
		{"*.log", false, []string{"debug.log"}},
		{"!*.go", false, []string{"bin.dat", "web/app.ts", "web/app.tsx"}},
		{"*.go", true, append([]string{".hidden/secret.go"}, all...)},
	}

	for _, e := range engines(t) {
		for _, c := range cases {
			got, err := e.Glob(context.Background(), GlobInput{Dir: dir, Pattern: c.pattern, Hidden: c.hidden, Limit: 100})
			if err != nil {
				t.Fatalf("%s %s: %v", e.Name(), c.pattern, err)
			}

			slices.Sort(got)

			if !slices.Equal(got, c.want) {
				t.Errorf("%s glob %q hidden=%v: got %v want %v", e.Name(), c.pattern, c.hidden, got, c.want)
			}
		}
	}
}

func TestGrep(t *testing.T) {
	dir := fixture(t)

	for _, e := range engines(t) {
		got, err := e.Grep(context.Background(), GrepInput{Dir: dir, Pattern: "hello", CaseSensitive: true, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}

		var paths []string
		for _, m := range got {
			paths = append(paths, m.Path)
		}

		slices.Sort(paths)

		want := []string{".hidden/secret.go", "pkg/a/a.go", "pkg/b/nested/b.go", "web/app.ts"}
		if !slices.Equal(paths, want) {
			t.Errorf("%s grep: got %v want %v", e.Name(), paths, want)
		}

		got, err = e.Grep(context.Background(), GrepInput{Dir: dir, Pattern: "hello", CaseSensitive: false, Include: "*.go", Limit: 100})
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 4 {
			t.Errorf("%s insensitive include: got %v", e.Name(), got)
		}

		got, err = e.Grep(context.Background(), GrepInput{Dir: dir, File: "main.go", Pattern: `println("`, Literal: true, CaseSensitive: true, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 1 || got[0].Line != 4 || got[0].Text != "\tprintln(\"Hello\")" {
			t.Errorf("%s literal file: got %+v", e.Name(), got)
		}

		if _, err := e.Grep(context.Background(), GrepInput{Dir: dir, Pattern: "(", CaseSensitive: true, Limit: 10}); err == nil {
			t.Errorf("%s: expected invalid pattern error", e.Name())
		}

		got, _ = e.Grep(context.Background(), GrepInput{Dir: dir, Pattern: "e", CaseSensitive: true, Limit: 2})
		if len(got) != 3 {
			t.Errorf("%s limit: want limit+1=3 results, got %d", e.Name(), len(got))
		}
	}
}
