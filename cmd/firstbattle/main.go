package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"lingxi/internal/app"
	"lingxi/internal/scorecard"
)

func main() {
	recordDir := flag.String("record-dir", "records", "运行记录目录")
	examState := flag.String("exam-state", "", "密封考试 registry 路径")
	maxLines := flag.Int("max-lines", 0, "每个 JSONL 最多读取行数(0=流式读完)")
	output := flag.String("output", "", "scorecard JSON 输出路径(空=stdout)")
	backfillInput := flag.String("backfill-input", "", "从旧 executable shadow 文件尾部回填首战影子")
	backfillLines := flag.Int("backfill-lines", 500, "回填最近行数，不扫描整份大文件")
	truncateBackfill := flag.Bool("truncate-backfill", false, "回填前清空首战影子输出")
	flag.Parse()

	if *backfillInput != "" {
		if _, err := app.BackfillRecentFirstBattleShadow(*recordDir, *backfillInput, *backfillLines, *truncateBackfill, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	report, err := scorecard.Build(scorecard.Options{
		RecordDir: *recordDir, ExamState: *examState, MaxLines: *maxLines, Progress: os.Stderr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data = append(data, '\n')
	if *output == "" {
		_, _ = os.Stdout.Write(data)
		return
	}
	if err := os.WriteFile(*output, data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
