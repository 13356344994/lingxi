package app

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// Run 是应用编排入口：进程级信号处理由调用方（cmd/lingxi）负责，本函数仅依据命令行参数选择
// 实盘/回测模式并完成依赖装配。把它从 func main 退化为可导出的编排函数后，main 包仅剩 cmd/lingxi
// 一层薄壳，满足 Gate 3「main 退化为编排」的目标。
func Run(ctx context.Context) {
	log.Printf("启动 lingxi: %s", buildVersionSummary())

	if len(os.Args) > 1 && (os.Args[1] == "backtest" || os.Args[1] == "backtest-replay" || os.Args[1] == "backtest-timeline" || os.Args[1] == "backtest-monthly") {
		runBacktestMode(ctx)
		return
	}

	cfg, err := loadEngineConfig()
	if err != nil {
		log.Fatalf("加载主配置失败: %v", err)
	}

	// 新增：每日冷启动健康检查
	if err := PerformColdStartCheck(ctx, cfg); err != nil {
		log.Fatalf("冷启动检查未通过，拒绝启动: %v", err)
	}

	store, err := NewReportStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		log.Fatalf("初始化记录存储失败: %v", err)
	}
	controller, err := NewRuntimeController(ctx, store, cfg, log.Default())
	if err != nil {
		log.Fatalf("创建后台控制器失败: %v", err)
	}
	dashboard, err := NewDashboardServer(cfg.NetworkConfig.DashboardAddr, store, controller, log.Default())
	if err != nil {
		log.Fatalf("创建 Web 面板失败: %v", err)
	}
	if err := dashboard.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Web 面板异常退出: %v", err)
	}
}

func runBacktestMode(ctx context.Context) {
	log.Printf("启动回测/模拟模式: %s", buildVersionSummary())
	cfg, err := loadEngineConfig()
	if err != nil {
		log.Fatalf("加载主配置失败: %v", err)
	}
	store, err := NewReportStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		log.Fatalf("初始化回测记录存储失败: %v", err)
	}

	runner := NewBacktestRunner(store, log.Default(), cfg)
	mode := "aggregate"
	inputPath := filepath.Join("backtest", "sample_backtest_trades.csv")
	secretPath := "secret.yaml"
	lookbackDays := cfg.HistoricalConfig.DefaultLookbackDays

	switch os.Args[1] {
	case "backtest-monthly":
		mode = "monthly"
		if len(os.Args) > 2 {
			secretPath = os.Args[2]
		}
		if len(os.Args) > 3 {
			lookbackDays = parseLookbackDays(os.Args[3], lookbackDays)
		}
	case "backtest-replay":
		mode = "replay"
		inputPath = filepath.Join("backtest", "sample_backtest_replay.csv")
		if len(os.Args) > 2 {
			inputPath = os.Args[2]
		}
	case "backtest-timeline":
		mode = "timeline"
		inputPath = filepath.Join("backtest", "sample_backtest_timeline.csv")
		if len(os.Args) > 2 {
			inputPath = os.Args[2]
		}
	default:
		if len(os.Args) > 2 && os.Args[2] == "replay" {
			mode = "replay"
			inputPath = filepath.Join("backtest", "sample_backtest_replay.csv")
			if len(os.Args) > 3 {
				inputPath = os.Args[3]
			}
		} else if len(os.Args) > 2 && os.Args[2] == "timeline" {
			mode = "timeline"
			inputPath = filepath.Join("backtest", "sample_backtest_timeline.csv")
			if len(os.Args) > 3 {
				inputPath = os.Args[3]
			}
		} else if len(os.Args) > 2 && os.Args[2] == "monthly" {
			mode = "monthly"
			if len(os.Args) > 3 {
				secretPath = os.Args[3]
			}
			if len(os.Args) > 4 {
				lookbackDays = parseLookbackDays(os.Args[4], lookbackDays)
			}
		} else if len(os.Args) > 2 {
			inputPath = os.Args[2]
		}
	}

	var records []BacktestCSVRecord
	switch mode {
	case "replay":
		records, err = runner.RunReplayFromCSV(inputPath)
	case "timeline":
		records, err = runner.RunTimelineFromCSV(inputPath)
	case "monthly":
		records, err = runner.RunMonthlyMarketSimulation(ctx, secretPath, lookbackDays)
	default:
		records, err = runner.RunFromCSV(inputPath)
	}
	if err != nil {
		log.Fatalf("执行回测失败: %v", err)
	}

	if mode == "monthly" {
		log.Printf("月度真实行情模拟完成，lookbackDays=%d secret=%s 已写入 %d 条汇总记录到 %s，并输出详细日志到 %s",
			lookbackDays, secretPath, len(records), filepath.Join(cfg.NetworkConfig.RecordDir, backtestCSVName), filepath.Join(cfg.NetworkConfig.RecordDir, monthlySimulationLogCSVName))
		return
	}
	log.Printf("回测模式完成，模式=%s 已写入 %d 条汇总记录到 %s", mode, len(records), filepath.Join(cfg.NetworkConfig.RecordDir, backtestCSVName))
}

func parseLookbackDays(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
