package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue/binance"
)

// autoStartLiveDelay 是 opt-in 自动启动实盘前的就绪等待：给面板/控制器/单写租约一点时间起来再触发。
const autoStartLiveDelay = 3 * time.Second

// autoStartLiveEnv 控制「进程启动后自动恢复实盘交易」。默认关闭：不设该环境变量时仅起面板，需人工点启动
// （最安全，适合首次实盘校准）。设为 1/true/yes 时配合 systemd/watchdog，可让崩溃重启后自动续上交易
// （单写租约 single_writer_lease 已保证不会双开）。
const autoStartLiveEnv = "LINGXI_AUTOSTART_LIVE"

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
	// 进程级心跳:与各所引擎/跨所执行器是否启动无关,只要进程活着就写 records/heartbeat.json,
	// 供外部 watchdog 判活(避免 cross-venue-only 形态下心跳永不更新→误重启循环)。
	startProcessHeartbeatWriter(ctx, cfg.NetworkConfig.RecordDir, log.Default())

	// P3a/P3b/P3f:用 Orchestrator 组装控制面,每所经 buildVenueRuntime 拿到独立的 (ReportStore, RuntimeController)。
	// 无 `venues:` 段(现网 config.yaml 即如此)→ 只注册唯一 venue("binance",recordDir 沿用 records/),
	// 行为与改造前逐字节一致。配置了 `venues:` 段 → 按 enabled 计划逐所注册(records/<venue>/ 隔离),
	// 并启用**启动隔离**:某所失败仅禁用该所、不阻塞其它所。
	orch := NewOrchestrator()
	allowUnknown := strings.EqualFold(strings.TrimSpace(os.Getenv(allowUnknownConfigEnv)), "true")
	plans, err := loadVenuePlans(cfg, allowUnknown)
	if err != nil {
		return fmt.Errorf("解析 venues 配置失败: %w", err)
	}
	enabled := enabledVenuePlans(plans)
	if len(enabled) == 0 {
		// 单所旧路径:无 venues 段。
		binanceRT, berr := buildVenueRuntime(ctx, cfg, binance.VenueName, log.Default())
		if berr != nil {
			return fmt.Errorf("创建 binance 运行时失败: %w", berr)
		}
		orch.Register(binanceRT)
	} else {
		// 多所路径:逐所构建 + 启动隔离(一所失败不连累其它所)。
		registered := registerEnabledVenues(orch, enabled, func(p VenuePlan) (*venueRuntime, error) {
			return buildRegisteredVenueRuntime(ctx, p, log.Default())
		}, log.Default())
		if registered == 0 || orch.Primary() == nil {
			return fmt.Errorf("所有启用的 venue 均启动失败,无可用交易所运行时")
		}
	}
	dashboard, err := NewDashboardServer(cfg.NetworkConfig.DashboardAddr, orch, log.Default())
	if err != nil {
		return fmt.Errorf("创建 Web 面板失败: %w", err)
	}
	// 控制令牌的固定镜像目录(全局 records/):Primary 所随注册顺序漂移时主令牌文件路径会变,
	// 镜像保证 `cat records/.dashboard_control_token` 永远是当前有效令牌。
	dashboard.SetTokenMirrorDir(cfg.NetworkConfig.RecordDir)
	// 重启后把新控制令牌经 Telegram 推给用户(用户明确要求;凭据与交易告警同源 secret.yaml,
	// 未配置 Telegram 时静默跳过)。
	if tgToken, tgChat := loadAlertingSecretsOrEmpty("secret.yaml"); tgToken != "" && tgChat != "" {
		ac := cfg.AlertingConfig
		ac.Enabled = true
		ac.TelegramBotToken = tgToken
		ac.TelegramChatID = tgChat
		ac.MinSeverity = "info"
		tokenDispatcher := newAlertDispatcher(ac, log.Default())
		dashboard.SetTokenNotifier(func(token string) {
			tokenDispatcher.NotifyTrade(Alert{
				Level:      EventLevelInfo,
				Title:      "面板控制令牌已更新",
				Detail:     fmt.Sprintf("服务已重启,新控制令牌:\n%s", token),
				OccurredAt: time.Now().UTC(),
			})
		})
	}
	secretPath := strings.TrimSpace(os.Getenv("LINGXI_SECRET_PATH"))
	if secretPath == "" {
		secretPath = "secret.yaml"
	}
	if cfg.CrossVenueScanner.Enabled {
		scanner := NewCrossVenueScanner(cfg, orch, secretPath, log.Default())
		dashboard.SetCrossVenueScanner(scanner)
		// OKX/Gate funding 执行器选币读扫描器快照,不现场拉全量资金费。
		for _, rt := range orch.All() {
			if rt != nil && rt.controller != nil && isFundingExecutorVenue(rt.name) {
				rt.controller.SetCrossVenueScanner(scanner)
			}
		}
		go scanner.Run(ctx)
		if cfg.CrossVenueExecutor.Enabled {
			exec, err := NewCrossVenueExecutor(cfg, orch, scanner, secretPath, log.Default())
			if err != nil {
				log.Default().Printf("[跨所执行] 构造失败,跳过: %v", err)
			} else {
				dashboard.SetCrossVenueExecutor(exec)
				go exec.Run(ctx)
				go exec.RunEquitySampler(ctx)
			}
		}
		// 五策略统一轮动 runner(默认关闭;unified_rotation.enabled=true 才启动,
		// live_trading_enabled=false 时全程影子:不下单、不真划转)。perp-perp 仍走上方独立执行器。
		if cfg.UnifiedRotation.Enabled {
			rotVenues := buildCrossVenueExecutorAdapters(orch, cfg, secretPath, log.Default())
			rd := cfg.NetworkConfig.RecordDir
			svsp := NewSingleVenueSpotPerpExecutor(cfg, rotVenues, rd, log.Default())
			cvsp := NewCrossVenueSpotPerpExecutor(cfg, rotVenues, rd, log.Default())
			svps := NewSingleVenuePerpSpotExecutor(cfg, rotVenues, rd, log.Default())
			cvps := NewCrossVenuePerpSpotExecutor(cfg, rotVenues, rd, log.Default())
			runner := NewUnifiedRotationRunner(
				cfg.CrossVenueExecutor, DefaultRotationConfig(), DefaultCapitalRouterConfig(),
				svsp, cvsp, svps, cvps, scanner, rotVenues, rd, log.Default(),
			)
			dashboard.SetRotationExecutors(svsp, cvsp, svps, cvps)
			log.Default().Printf("[统一轮动] runner 启动 (live=%v)", cfg.UnifiedRotation.LiveTradingEnabled)
			go runner.Run(ctx, cfg.UnifiedRotation.LiveTradingEnabled)
		}
	} else if cfg.CrossVenueExecutor.Enabled {
		log.Default().Printf("[跨所执行] 已启用但扫描器未启用,跳过(需 cross_venue_scanner.enabled=true)")
	}
	// 单所实盘引擎已退役(de-wire 2026-06-17):此处原有的 maybeAutoStartLiveTrading /
	// maybeAutoStartVenueTasks 单所自启已从装配链摘除,实盘统一经跨所执行器(上方 CrossVenueExecutor)。
	if err := dashboard.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("Web 面板异常退出: %w", err)
	}
	return nil
}

// envFlagEnabled 解析布尔型环境变量（1/true/yes/on 视为真，大小写无关）。
func envFlagEnabled(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// maybeAutoStartLiveTrading 在 opt-in（LINGXI_AUTOSTART_LIVE 为真）时，于面板就绪后异步触发实盘交易，
// 等价于面板上点"启动实盘"。默认关闭。失败仅记日志、不影响面板存活（用户仍可手动启动）。
// 配合 systemd Restart=always + watchdog，可让进程崩溃重启后自动续上交易；单写租约保证不会双开。
func maybeAutoStartLiveTrading(ctx context.Context, controller *RuntimeController) {
	if controller == nil || !envFlagEnabled(autoStartLiveEnv) {
		return
	}
	secretPath := strings.TrimSpace(os.Getenv("LINGXI_SECRET_PATH"))
	if secretPath == "" {
		secretPath = "secret.yaml"
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(autoStartLiveDelay):
		}
		log.Printf("%s 已启用：自动触发实盘交易启动(secret=%s)", autoStartLiveEnv, redactSecretPath(secretPath))
		if err := controller.Start(RuntimeStartRequest{Mode: string(RuntimeModeLive), SecretPath: secretPath}); err != nil {
			log.Printf("自动启动实盘交易失败(可在面板手动启动): %v", err)
		}
	}()
}

// maybeAutoStartVenueTasks 自动拉起 venues.<name>.autostart=true 的非 primary venue 执行任务
// (等价面板对该所点"启动")。primary(币安)由 LINGXI_AUTOSTART_LIVE 单独控制,这里跳过避免双触发。
// 失败仅记日志,可在面板手动启动;就绪等待与 primary 同节奏。
func maybeAutoStartVenueTasks(ctx context.Context, orch *Orchestrator, plans []VenuePlan) {
	if orch == nil {
		return
	}
	primary := orch.Primary()
	secretPath := strings.TrimSpace(os.Getenv("LINGXI_SECRET_PATH"))
	if secretPath == "" {
		secretPath = "secret.yaml"
	}
	for _, plan := range plans {
		if !plan.Autostart {
			continue
		}
		rt, ok := orch.Get(plan.Name)
		if !ok || rt == nil || rt == primary {
			continue
		}
		name := plan.Name
		ctrl := rt.controller
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(autoStartLiveDelay):
			}
			log.Printf("[编排] venue=%s autostart=true:自动启动执行任务(secret=%s)", name, redactSecretPath(secretPath))
			if err := ctrl.Start(RuntimeStartRequest{Mode: string(RuntimeModeLive), SecretPath: secretPath}); err != nil {
				log.Printf("[编排] venue=%s 自动启动失败(可在面板手动启动): %v", name, err)
			}
		}()
	}
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
