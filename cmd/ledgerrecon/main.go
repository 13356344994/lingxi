package main

// ledgerrecon:只读账本对账工具(settlements vs 四所权益快照)。
// 用法: go run ./cmd/ledgerrecon [recordDir]
// 默认 recordDir=records → records/crossvenue/{settlements,equity_snapshots}.jsonl

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/app"
)

func main() {
	recordDir := "records"
	if len(os.Args) > 1 {
		recordDir = os.Args[1]
	}
	if !filepath.IsAbs(recordDir) {
		if wd, err := os.Getwd(); err == nil {
			recordDir = filepath.Join(wd, recordDir)
		}
	}
	settlementsPath, equityPath := app.DefaultLedgerReconPaths(recordDir)
	report, err := app.RunLedgerRecon(settlementsPath, equityPath, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledgerrecon: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(app.FormatLedgerReconReport(report))
}
