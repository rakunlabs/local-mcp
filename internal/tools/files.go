package tools

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aymanbagabas/go-udiff"
)

var bom = []byte{0xef, 0xbb, 0xbf}

// fileLocks serializes mutations of the same file across concurrent calls.
var fileLocks sync.Map

func lockFile(path string) func() {
	m, _ := fileLocks.LoadOrStore(path, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()

	return mu.Unlock
}

type textFile struct {
	bom  bool
	text string
}

func decodeText(data []byte) textFile {
	if bytes.HasPrefix(data, bom) {
		return textFile{bom: true, text: string(data[len(bom):])}
	}

	return textFile{text: string(data)}
}

func (t textFile) encode() []byte {
	if t.bom {
		return append(append([]byte(nil), bom...), t.text...)
	}

	return []byte(t.text)
}

func lineEnding(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}

	return "\n"
}

func toLineEnding(s, ending string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if ending == "\r\n" {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}

	return s
}

// writeFileAtomic writes through a temp file in the same directory so a
// failure never leaves a half-written file. The existing mode is kept.
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directories; %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

type diffStat struct {
	patch     string
	additions int
	deletions int
}

func diff(name, before, after string) diffStat {
	before = strings.ReplaceAll(before, "\r\n", "\n")
	after = strings.ReplaceAll(after, "\r\n", "\n")

	patch := udiff.Unified("a/"+name, "b/"+name, before, after)

	var st diffStat

	st.patch = patch

	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			st.additions++
		case strings.HasPrefix(line, "-"):
			st.deletions++
		}
	}

	return st
}

// previewDiff renders a diff for the model, bounded to keep responses small.
func previewDiff(patch string) string {
	const maxLines = 60

	lines := strings.Split(strings.TrimRight(patch, "\n"), "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("... (%d more diff lines)", len(lines)-maxLines))
	}

	return "```diff\n" + strings.Join(lines, "\n") + "\n```"
}
