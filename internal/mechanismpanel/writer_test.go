package mechanismpanel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestJSONLWriterResumeAtTruncatesUnconfirmedTail 覆盖红队#12"崩溃恢复不得重复
// append":模拟一次崩溃——输出文件里已经比 checkpoint 记录的行数多出两行未被确认的尾巴
// (进程在 checkpoint 落盘之前又写出了两条记录就被杀掉)。用 checkpoint 记录的行数
// (keepLines) 重新以续写模式打开,必须先剪掉那条尾巴,再写入与原来等价的记录,结果不能
// 出现重复行。
func TestJSONLWriterResumeAtTruncatesUnconfirmedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "features.jsonl")

	w, err := NewJSONLWriter(path)
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := w.Write(map[string]int{"line": i}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Checkpoint only confirmed 3 lines; lines 3,4 are the "crashed before checkpoint" tail.
	const confirmed = 3

	w2, err := NewJSONLWriterResumeAt(path, confirmed)
	if err != nil {
		t.Fatalf("NewJSONLWriterResumeAt: %v", err)
	}
	// Re-emit what would have been recomputed for lines 3 and 4 after resume.
	if err := w2.Write(map[string]int{"line": 3}); err != nil {
		t.Fatalf("Write resumed 3: %v", err)
	}
	if err := w2.Write(map[string]int{"line": 4}); err != nil {
		t.Fatalf("Write resumed 4: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close resumed: %v", err)
	}

	lines := readLinesForTest(t, path)
	if len(lines) != 5 {
		t.Fatalf("expected exactly 5 lines after resume (no duplicates), got %d: %v", len(lines), lines)
	}
	for i, want := range []string{`{"line":0}`, `{"line":1}`, `{"line":2}`, `{"line":3}`, `{"line":4}`} {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

// TestJSONLWriterResumeAtNoOpWhenAlreadyShortEnough covers the common case where the
// file already has exactly (or fewer than, e.g. a brand-new empty output) keepLines
// lines — nothing to cut, resume just appends.
func TestJSONLWriterResumeAtNoOpWhenAlreadyShortEnough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "features.jsonl")

	w, err := NewJSONLWriter(path)
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	if err := w.Write(map[string]int{"line": 0}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w2, err := NewJSONLWriterResumeAt(path, 1)
	if err != nil {
		t.Fatalf("NewJSONLWriterResumeAt: %v", err)
	}
	if err := w2.Write(map[string]int{"line": 1}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readLinesForTest(t, path)
	if len(lines) != 2 || lines[0] != `{"line":0}` || lines[1] != `{"line":1}` {
		t.Fatalf("unexpected lines: %v", lines)
	}
}

// TestJSONLWriterResumeAtFreshEmptyFile covers the very first run: no file exists yet,
// keepLines=0, resume-open must behave exactly like NewJSONLWriter on an empty file.
func TestJSONLWriterResumeAtFreshEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "features.jsonl")

	w, err := NewJSONLWriterResumeAt(path, 0)
	if err != nil {
		t.Fatalf("NewJSONLWriterResumeAt: %v", err)
	}
	if err := w.Write(map[string]int{"line": 0}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	lines := readLinesForTest(t, path)
	if len(lines) != 1 || lines[0] != `{"line":0}` {
		t.Fatalf("unexpected lines: %v", lines)
	}
}

// TestJSONLWriterResumeAtRejectsOutOfSyncFile covers fail-closed behavior at the writer
// level: if the checkpoint claims more confirmed lines than the output file actually
// contains (wrong path, corrupted/truncated file, or a stale checkpoint pointed at a
// fresh file), NewJSONLWriterResumeAt must error out instead of silently proceeding.
func TestJSONLWriterResumeAtRejectsOutOfSyncFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "features.jsonl")
	if err := os.WriteFile(path, []byte("{\"line\":0}\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := NewJSONLWriterResumeAt(path, 3); err == nil {
		t.Fatalf("expected error when checkpoint claims 3 confirmed lines but file only has 1")
	}
}

// TestJSONLWriterResumeAtRejectsPartialTrailingLine covers the mid-write-crash edge
// case: the file's last "line" has no trailing newline (bufio flushed mid-record), so
// even byte-counting it as present it is not a valid confirmed record.
func TestJSONLWriterResumeAtRejectsPartialTrailingLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "features.jsonl")
	// One full confirmed line + a partial, unterminated second line.
	if err := os.WriteFile(path, []byte("{\"line\":0}\n{\"line\":1"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := NewJSONLWriterResumeAt(path, 2); err == nil {
		t.Fatalf("expected error when the 2nd confirmed line is an unterminated partial write")
	}
	// But claiming only the first (complete) line is confirmed must still work; the
	// dangling partial second line beyond it gets discarded by the truncation.
	w, err := NewJSONLWriterResumeAt(path, 1)
	if err != nil {
		t.Fatalf("NewJSONLWriterResumeAt(keep=1): %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	lines := readLinesForTest(t, path)
	if len(lines) != 1 || lines[0] != `{"line":0}` {
		t.Fatalf("expected partial tail discarded, got %v", lines)
	}
}

func readLinesForTest(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := string(b)
	if s == "" {
		return nil
	}
	if s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
