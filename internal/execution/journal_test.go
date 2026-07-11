package execution

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWALAppendAndReplayOrderAndSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.jsonl")

	w, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL 失败: %v", err)
	}
	kinds := []RecordKind{RecordIntentCreated, RecordClaimAcquired, RecordOrderSubmitted, RecordFillApplied, RecordClaimReleased}
	for _, k := range kinds {
		if _, err := w.Append(Record{Kind: k, Symbol: "BTC-USDT"}); err != nil {
			t.Fatalf("Append(%s) 失败: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	records, err := ReplayWAL(path)
	if err != nil {
		t.Fatalf("ReplayWAL 失败: %v", err)
	}
	if len(records) != len(kinds) {
		t.Fatalf("重放记录数=%d, want %d", len(records), len(kinds))
	}
	for i, rec := range records {
		if rec.Seq != uint64(i+1) {
			t.Fatalf("第 %d 条 seq=%d, want %d", i, rec.Seq, i+1)
		}
		if rec.Kind != kinds[i] {
			t.Fatalf("第 %d 条 kind=%s, want %s", i, rec.Kind, kinds[i])
		}
	}
}

func TestWALReplayMissingFileReturnsEmptyNoError(t *testing.T) {
	records, err := ReplayWAL(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("文件不存在应返回 nil error, got %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("文件不存在应返回空切片, got %d 条", len(records))
	}
}

// TestWALCrashRecoveryTruncatedTail 模拟「崩溃在写最后一条记录中途」:合法记录必须全部保留,
// 截断的尾部行必须被丢弃而不是导致重放报错;重新 OpenWAL 后续写入必须从正确的 seq 续接,不重号。
func TestWALCrashRecoveryTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.jsonl")

	w, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL 失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := w.Append(Record{Kind: RecordFillApplied, Symbol: "BTC-USDT"}); err != nil {
			t.Fatalf("Append 失败: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	// 模拟崩溃:直接向文件追加一段不完整的 JSON(没有闭合、没有换行),
	// 代表「fsync 之前进程被杀」留下的半截写入。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开文件模拟崩溃写失败: %v", err)
	}
	if _, err := f.WriteString(`{"seq":4,"kind":"fill_applie`); err != nil {
		t.Fatalf("写入半截记录失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭文件失败: %v", err)
	}

	records, err := ReplayWAL(path)
	if err != nil {
		t.Fatalf("崩溃截断的尾部行不应导致重放报错: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("应恢复出崩溃前的 3 条完整记录, got %d", len(records))
	}
	if records[len(records)-1].Seq != 3 {
		t.Fatalf("最后一条完整记录 seq 应为 3, got %d", records[len(records)-1].Seq)
	}

	// 重新打开后继续写入,seq 必须从 4 续接,不能因为半截行导致重号或跳号。
	w2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("崩溃后重新 OpenWAL 失败: %v", err)
	}
	rec, err := w2.Append(Record{Kind: RecordFillApplied, Symbol: "BTC-USDT"})
	if err != nil {
		t.Fatalf("崩溃后续写失败: %v", err)
	}
	if rec.Seq != 4 {
		t.Fatalf("续写后的 seq 应为 4, got %d", rec.Seq)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	recordsAfterRestart, err := ReplayWAL(path)
	if err != nil {
		t.Fatalf("续写后再次重放失败: %v", err)
	}
	if len(recordsAfterRestart) != 4 || recordsAfterRestart[3].Seq != 4 {
		t.Fatalf("修复半截尾行后续写应完整可重放, got %+v", recordsAfterRestart)
	}
}

func TestOpenWALAddsDelimiterAfterValidRecordWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.jsonl")
	if err := os.WriteFile(path, []byte(`{"seq":1,"written_at":"2026-07-11T00:00:00Z","kind":"intent_created"}`), 0o644); err != nil {
		t.Fatalf("写 fixture 失败: %v", err)
	}
	w, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL 失败: %v", err)
	}
	if _, err := w.Append(Record{Kind: RecordClaimAcquired}); err != nil {
		t.Fatalf("Append 失败: %v", err)
	}
	_ = w.Close()
	records, err := ReplayWAL(path)
	if err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("合法无换行尾记录与新记录都应保留, got %d", len(records))
	}
}

// TestWALReplayMiddleCorruptionErrors 确认「中间行损坏」(非崩溃尾部)必须报错,不能被静默吞掉,
// 否则会把真正的磁盘损坏误判成正常崩溃截断。
func TestWALReplayMiddleCorruptionErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.jsonl")

	w, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL 失败: %v", err)
	}
	if _, err := w.Append(Record{Kind: RecordIntentCreated, Symbol: "BTC-USDT"}); err != nil {
		t.Fatalf("Append 失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开文件失败: %v", err)
	}
	// 中间插入一整行损坏 JSON,后面再跟一行合法记录,模拟磁盘位翻转而不是崩溃截断。
	if _, err := f.WriteString("not-json-at-all\n"); err != nil {
		t.Fatalf("写入损坏行失败: %v", err)
	}
	if _, err := f.WriteString(`{"seq":99,"kind":"fill_applied"}` + "\n"); err != nil {
		t.Fatalf("写入尾部合法行失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭文件失败: %v", err)
	}

	if _, err := ReplayWAL(path); err == nil {
		t.Fatal("中间行损坏(非末尾)应报错,不能被当成崩溃截断静默丢弃")
	}
}
