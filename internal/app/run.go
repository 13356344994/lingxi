package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
)

// beijingLogLocation 固定北京时区(UTC+8,无 DST);用 FixedZone 避免依赖系统 tzdata。
var beijingLogLocation = time.FixedZone("UTC+8", 8*3600)

// beijingLogWriter 在每条日志行前注入北京时间戳。仅改日志显示口径,不触碰 time.Local 与任何业务逻辑
// (业务时刻仍走 time.Now().UTC());与全局改时区相比,隔离且无副作用。
type beijingLogWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *beijingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ts := time.Now().In(beijingLogLocation).Format("2006/01/02 15:04:05")
	if _, err := w.out.Write(append([]byte(ts+" "), p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// setupBeijingLogging 把标准库默认 logger 的时间戳改为北京时间:关掉自带 Ldate|Ltime(本地=UTC),
// 改由 beijingLogWriter 统一前缀。子 logger 经 log.Default().Writer()/Flags() 继承,亦为北京时间。
func setupBeijingLogging() {
	log.SetFlags(0)
	log.SetOutput(&beijingLogWriter{out: os.Stderr})
}

// Run 是应用编排入口：进程级信号处理由调用方（cmd/lingxi）负责，本函数仅依据命令行参数选择
// 实盘/回测模式并完成依赖装配，并把失败以 error 上抛由 main 决定退出策略（R20：编排层不再 log.Fatalf，
// 便于宿主复用、集成测试与 supervisor 接管）。
func Run(ctx context.Context) error {
	setupBeijingLogging()
	log.Printf("启动 lingxi: %s", buildVersionSummary())

	// config-sample 导出一份自文档化的样例配置(列出全部可配项及默认值),不读取配置文件、不触碰实盘。
	// 用法: lingxi config-sample [输出路径]; 省略路径则打印到 stdout。
	if len(os.Args) > 1 && os.Args[1] == "config-sample" {
		return runConfigSample()
	}

	cfg, err := loadEngineConfig()
	if err != nil {
		return fmt.Errorf("加载主配置失败: %w", err)
	}

	return startLiveStack(ctx, cfg)
}

// startLiveStack 装配并启动实盘技术栈(冷启动检查 → 记录存储 → 后台控制器 → Web 面板)。
// 运行三态 OFF/CANARY_LIVE/LIVE 由控制面 rotation_live_enabled + 部署闸决定。
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
	dashboard.SetRecordDir(cfg.NetworkConfig.RecordDir)
	controlPlane := NewControlPlane(cfg.NetworkConfig.RecordDir, emergencyStopPaths(cfg), log.Default())
	dashboard.SetControlPlane(controlPlane)
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
	// S7-A:方向引擎真钱下单前抢单写租约,防双开双写同一账户。flock 随进程退出由内核释放。
	liveLease, leaseErr := acquireLiveStackSingleWriterLease(cfg)
	if leaseErr != nil {
		return leaseErr
	}
	if liveLease != nil {
		defer func() { _ = liveLease.Release() }()
	}
	// 账户级共享服务(与策略引擎解耦):权益采样 / 充提核算 / 账户硬风控,覆盖全部四所。
	accountVenues := buildAccountVenueAdapters(orch, cfg, secretPath, log.Default())
	equitySvc := NewAccountEquityService(accountVenues, cfg.NetworkConfig.RecordDir, log.Default())
	dashboard.SetAccountEquityService(equitySvc)
	go equitySvc.RunEquitySampler(ctx) // 账户级权益采样:常驻(收益面板「北京0点基准」数据源)
	// Stage B 账户级硬风控:常驻(实盘方向引擎也需要账户级硬止损)。一级硬停默认启用(非破坏性)。
	go startAccountRiskWatcher(ctx, accountRiskWatchConfig{
		Cfg:          cfg,
		RecordDir:    cfg.NetworkConfig.RecordDir,
		Logger:       log.Default(),
		ControlPlane: controlPlane,
		Equity:       equitySvc,
	})
	if cfg.OKXDirectional.Enabled {
		if eng, err := NewOKXDirectionalEngine(cfg, orch, cfg.NetworkConfig.RecordDir, log.Default()); err != nil {
			log.Default().Printf("[OKX方向] 构造失败,跳过: %v", err)
		} else {
			eng.SetControlPlane(controlPlane)
			dashboard.SetOKXDirectionalEngine(eng)
			go eng.Run(ctx)
		}
	}
	// 其余方向所各起一个独立方向引擎,默认停用(需面板点开始)。bybit/gate 适配器已注入 orchestrator。
	for _, vn := range []string{bybit.VenueName, gate.VenueName} {
		if eng, derr := NewDirectionalEngineForVenue(vn, cfg, orch, cfg.NetworkConfig.RecordDir, log.Default()); derr != nil {
			log.Default().Printf("[方向] %s 引擎构造失败,跳过: %v", vn, derr)
		} else {
			eng.SetControlPlane(controlPlane)
			dashboard.SetOKXDirectionalEngine(eng)
			go eng.Run(ctx)
			log.Default().Printf("[方向] %s 引擎已接线(默认停用,需面板点开始)", vn)
		}
	}
	// binance 方向引擎:适配器构造特殊(需 networkCfg + 认证客户端),单独构建后接线;默认停用。
	if badp, berr := buildCrossVenueReadOnlyAdapter(binance.VenueName, cfg, secretPath); berr != nil {
		log.Default().Printf("[方向] binance 适配器构造失败,跳过: %v", berr)
	} else if bdv, ok := badp.(DirectionalVenue); !ok {
		log.Default().Printf("[方向] binance 适配器未实现 DirectionalVenue,跳过")
	} else if eng, derr := NewDirectionalEngine(binance.VenueName, bdv, buildDirectionalPeers(orch, binance.VenueName, log.Default()), cfg, cfg.NetworkConfig.RecordDir, log.Default()); derr != nil {
		log.Default().Printf("[方向] binance 引擎构造失败,跳过: %v", derr)
	} else {
		eng.SetControlPlane(controlPlane)
		dashboard.SetOKXDirectionalEngine(eng)
		go eng.Run(ctx)
		log.Default().Printf("[方向] binance 引擎已接线(默认停用,需面板点开始)")
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
