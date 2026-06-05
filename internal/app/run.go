package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"lingxi/internal/core"
)

// Run 是应用编排入口：进程级信号处理由调用方（cmd/lingxi）负责，本函数仅依据命令行参数选择
// 实盘/回测模式并完成依赖装配，并把失败以 error 上抛由 main 决定退出策略（R20：编排层不再 log.Fatalf，
// 便于宿主复用、集成测试与 supervisor 接管）。
func Run(ctx context.Context) error {
	log.Printf("启动 lingxi: %s", buildVersionSummary())

	if len(os.Args) > 1 && (os.Args[1] == "backtest" || os.Args[1] == "backtest-replay" || os.Args[1] == "backtest-timeline" || os.Args[1] == "backtest-monthly") {
		return runBacktestMode(ctx)
	}

	// reconcile 是只读对账子命令：把已落盘结算记录与模型预期逐周期对账并打印报告，不触碰下单/实盘路径。
	if len(os.Args) > 1 && os.Args[1] == "reconcile" {
		return runReconcileMode(ctx)
	}

	// flatten/emergency-flatten 是应急强平子命令：补全交易所侧 dead-man 的盲区(已成交裸仓)。供外部看门狗在
	// 确认主进程已死、本地未平仓台账非空时触发——从台账重建裸仓并方向感知双腿强平。这是会动用资金的实盘动作,
	// 故独立成命令、不在常规启动路径上,且必须在主进程确已退出后触发(详见 emergency_flatten.go)。
	if len(os.Args) > 1 && (os.Args[1] == "flatten" || os.Args[1] == "emergency-flatten") {
		return runEmergencyFlatten(ctx)
	}

	// config-sample 导出一份自文档化的样例配置(列出全部可配项及默认值),不读取配置文件、不触碰实盘。
	// 用法: lingxi config-sample [输出路径]; 省略路径则打印到 stdout。
	if len(os.Args) > 1 && os.Args[1] == "config-sample" {
		return runConfigSample()
	}

	cfg, err := loadEngineConfig()
	if err != nil {
		return fmt.Errorf("加载主配置失败: %w", err)
	}

	// shadow 是影子(只读冒烟)子命令：照常对接真实行情/账户、照常决策，但任何会动用资金或改变交易所
	// 状态的签名请求都会在 signedRequest 咽喉点被硬拦截、绝不触达交易所，只把意图落日志。用于实盘前抓
	// 灾难性 bug，而非校准阈值。这里强制把开关打开，避免依赖配置文件遗漏。
	if len(os.Args) > 1 && os.Args[1] == "shadow" {
		cfg.ExecutionConfig.ShadowTrading = true
		log.Printf("============================================================")
		log.Printf("  SHADOW MODE 已启用：不会有任何真实订单/资金动作触达交易所")
		log.Printf("  所有下单意图将以 SHADOW_BLOCKED 日志行记录，供人工核查")
		log.Printf("============================================================")
	}

	return startLiveStack(ctx, cfg)
}

// startLiveStack 装配并启动实盘技术栈(冷启动检查 → 记录存储 → 后台控制器 → Web 面板)。
// 实盘与影子模式共用同一套装配，唯一差别是 cfg.ExecutionConfig.ShadowTrading：影子模式下引擎跑的
// 是完全相同的真实流程，只是签名下单在咽喉点被拦截，从而保证「真实输入 + 零真实副作用」。
// 失败以 error 上抛（context.Canceled 视为正常退出，返回 nil）。
func startLiveStack(ctx context.Context, cfg EngineConfig) error {
	if err := PerformColdStartCheck(ctx, cfg); err != nil {
		return fmt.Errorf("冷启动检查未通过，拒绝启动: %w", err)
	}

	store, err := NewReportStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return fmt.Errorf("初始化记录存储失败: %w", err)
	}
	controller, err := NewRuntimeController(ctx, store, cfg, log.Default())
	if err != nil {
		return fmt.Errorf("创建后台控制器失败: %w", err)
	}
	dashboard, err := NewDashboardServer(cfg.NetworkConfig.DashboardAddr, store, controller, log.Default())
	if err != nil {
		return fmt.Errorf("创建 Web 面板失败: %w", err)
	}
	if err := dashboard.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("Web 面板异常退出: %w", err)
	}
	return nil
}

// runConfigSample 输出/写出由结构体 tag 反射生成的全字段样例配置。
func runConfigSample() error {
	sample := core.SampleConfigYAML()
	if len(os.Args) > 2 {
		path := os.Args[2]
		if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
			return fmt.Errorf("写出样例配置失败: %w", err)
		}
		log.Printf("已写出样例配置到 %s(共 %d 个可配置项)", path, len(core.ConfigKeys()))
		return nil
	}
	fmt.Print(sample)
	return nil
}

func runBacktestMode(ctx context.Context) error {
	log.Printf("启动回测/模拟模式: %s", buildVersionSummary())
	cfg, err := loadEngineConfig()
	if err != nil {
		return fmt.Errorf("加载主配置失败: %w", err)
	}
	store, err := NewReportStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return fmt.Errorf("初始化回测记录存储失败: %w", err)
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
		return fmt.Errorf("执行回测失败: %w", err)
	}

	if mode == "monthly" {
		log.Printf("月度真实行情模拟完成，lookbackDays=%d secret=%s 已写入 %d 条汇总记录到 %s，并输出详细日志到 %s",
			lookbackDays, redactSecretPath(secretPath), len(records), filepath.Join(cfg.NetworkConfig.RecordDir, backtestCSVName), filepath.Join(cfg.NetworkConfig.RecordDir, monthlySimulationLogCSVName))
		return nil
	}
	log.Printf("回测模式完成，模式=%s 已写入 %d 条汇总记录到 %s", mode, len(records), filepath.Join(cfg.NetworkConfig.RecordDir, backtestCSVName))
	return nil
}

func parseLookbackDays(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
