package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"lingxi/internal/mechanismresearch"
)

func main() {
	input := flag.String("input", "", "机制面板中性 JSONL 输入")
	features := flag.String("features", "", "机制面板 entry features JSONL")
	labels := flag.String("labels", "", "机制面板 future labels JSONL")
	horizonMS := flag.Int64("horizon-ms", 60000, "研究标签前瞻窗口毫秒")
	mode := flag.String("mode", "research", "输出模式: research 或 exam")
	output := flag.String("output", "", "JSON 输出路径(空=stdout)")
	maxLines := flag.Int("max-lines", 0, "最多读取行数(0=流式读完)")
	costBPS := flag.Float64("cost-bps", 0, "缺失逐 episode 成本时使用的固定成本 bps")
	seed := flag.Int64("seed", 20260711, "bootstrap 固定随机种子")
	bootstrapSamples := flag.Int("bootstrap-samples", 500, "bootstrap 抽样次数")
	folds := flag.Int("walk-forward-folds", 3, "anchored walk-forward 折数")
	purge := flag.Int("purge-episodes", 1, "walk-forward train/test 间 purge episode 数")
	embargo := flag.Int("embargo-episodes", 1, "walk-forward train/test 间 embargo episode 数")
	progressEvery := flag.Int("progress-every", 1000, "每 N 行向 stderr 显示进度")
	flag.Parse()

	report, err := mechanismresearch.Run(mechanismresearch.Options{
		InputPath: *input, FeaturePath: *features, LabelPath: *labels, LabelHorizonMS: *horizonMS,
		MaxLines: *maxLines, CostBPS: *costBPS, Seed: *seed,
		BootstrapSamples: *bootstrapSamples, WalkForwardFolds: *folds,
		PurgeEpisodes: *purge, EmbargoEpisodes: *embargo, ProgressEvery: *progressEvery,
		Progress: os.Stderr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var payload any
	switch *mode {
	case "research":
		payload = mechanismresearch.ResearchReport(report)
	case "exam":
		payload = mechanismresearch.ExamScorecard(report)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", *mode)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
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
