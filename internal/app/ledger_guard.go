package app

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// realLedgerWritesAllowed 真钱路径才允许写 positions.json / settlements.jsonl。
func realLedgerWritesAllowed(live bool) bool {
	return live
}

const dryRunLedgerDirName = "dryrun"

var dryRunLedgerMu sync.Mutex

// appendDryRunEvent 将 dry-run 意图追加到隔离目录 records/dryrun/events.jsonl(绝不碰真实账本)。
func appendDryRunEvent(recordDir, kind string, payload any) {
	dir := strings.TrimSpace(recordDir)
	if dir == "" {
		dir = "records"
	}
	path := filepath.Join(dir, dryRunLedgerDirName, "events.jsonl")
	row := map[string]any{
		"at":   time.Now().UTC().Format(time.RFC3339Nano),
		"kind": kind,
		"data": payload,
	}
	data, err := json.Marshal(row)
	if err != nil {
		return
	}
	dryRunLedgerMu.Lock()
	defer dryRunLedgerMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
}

func logDryRunClose(logger *log.Logger, strategy, symbol, reason string) {
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("[dry-run] 本应平仓 %s %s 原因:%s", strategy, symbol, reason)
}

func logDryRunOpen(logger *log.Logger, strategy, symbol string, detail string) {
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("[dry-run] 本应开仓 %s %s %s", strategy, symbol, detail)
}

// ledgerFileEmpty 断言台账文件不存在或为空(测试用)。
func ledgerFileEmpty(path string) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if info.Size() == 0 {
		return true, nil
	}
	return false, fmt.Errorf("ledger file %s size=%d", path, info.Size())
}
