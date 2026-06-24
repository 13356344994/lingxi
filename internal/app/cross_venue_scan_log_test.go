package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRotateJSONLBySize 验证按大小滚动:超限改名 .1、未超不动、不存在不报错。
func TestRotateJSONLBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scan_snapshots.jsonl")

	// 不存在 → 不滚动、不报错。
	if rotated, err := rotateJSONLBySize(path, 10); err != nil || rotated {
		t.Fatalf("不存在文件应 (false,nil),实得 (%v,%v)", rotated, err)
	}

	// 写 20 字节,阈值 100 → 未超,不滚动。
	if err := os.WriteFile(path, make([]byte, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	if rotated, err := rotateJSONLBySize(path, 100); err != nil || rotated {
		t.Fatalf("未超限不应滚动,实得 (%v,%v)", rotated, err)
	}

	// 写 200 字节,阈值 100 → 超限,滚动到 .1,原路径消失。
	if err := os.WriteFile(path, make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	rotated, err := rotateJSONLBySize(path, 100)
	if err != nil || !rotated {
		t.Fatalf("超限应滚动,实得 (%v,%v)", rotated, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("滚动后原文件应已改名消失")
	}
	bak := path + ".1"
	if info, err := os.Stat(bak); err != nil || info.Size() != 200 {
		t.Fatalf("归档 .1 应存在且为 200 字节,实得 err=%v", err)
	}

	// 再次超限滚动 → 覆盖旧 .1(只保留一代)。
	if err := os.WriteFile(path, make([]byte, 300), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rotateJSONLBySize(path, 100); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(bak); err != nil || info.Size() != 300 {
		t.Fatalf("第二次滚动应覆盖旧 .1 为 300 字节,实得 err=%v", err)
	}
}
