package patch

import (
	"strings"
	"testing"
)

func TestParseAndDerive(t *testing.T) {
	text := `*** Begin Patch
*** Add File: new.txt
+hello
+world
*** Update File: app.py
*** Move to: main.py
@@ def greet():
-    print("Hi")
+    print("Hello")
*** Delete File: old.txt
*** End Patch`

	hunks, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}

	if len(hunks) != 3 {
		t.Fatalf("got %d hunks", len(hunks))
	}

	if hunks[0].Kind != Add || hunks[0].Contents != "hello\nworld" {
		t.Fatalf("add hunk: %+v", hunks[0])
	}

	if hunks[1].Kind != Update || hunks[1].MovePath != "main.py" || hunks[1].Chunks[0].ChangeContext != "def greet():" {
		t.Fatalf("update hunk: %+v", hunks[1])
	}

	if hunks[2].Kind != Delete || hunks[2].Path != "old.txt" {
		t.Fatalf("delete hunk: %+v", hunks[2])
	}

	got, err := Derive("app.py", hunks[1].Chunks, "x = 1\ndef greet():\n    print(\"Hi\")\n")
	if err != nil {
		t.Fatal(err)
	}

	if want := "x = 1\ndef greet():\n    print(\"Hello\")\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDeriveKeepsCRLFAndFuzzyMatch(t *testing.T) {
	chunks := []Chunk{{OldLines: []string{"a = “x”", "b"}, NewLines: []string{"a = 1", "b"}}}

	got, err := Derive("f", chunks, "a = \"x\"  \r\nb\r\n")
	if err != nil {
		t.Fatal(err)
	}

	if got != "a = 1\r\nb\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestDeriveMissing(t *testing.T) {
	_, err := Derive("f", []Chunk{{OldLines: []string{"nope"}, NewLines: []string{"x"}}}, "a\n")
	if err == nil || !strings.Contains(err.Error(), "failed to find") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	for _, text := range []string{
		"no markers",
		"*** Begin Patch\n*** Add File: a\nmissing plus\n*** End Patch",
		"*** Begin Patch\n*** Update File: a\n*** End Patch",
		"*** Begin Patch\ngarbage\n*** End Patch",
	} {
		if _, err := Parse(text); err == nil {
			t.Errorf("expected error for %q", text)
		}
	}
}

func TestHeredoc(t *testing.T) {
	hunks, err := Parse("cat <<'EOF'\n*** Begin Patch\n*** Delete File: a\n*** End Patch\nEOF")
	if err != nil || len(hunks) != 1 {
		t.Fatalf("hunks=%v err=%v", hunks, err)
	}
}
