package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRequiresLiveSingleWriterLease(t *testing.T) {
	base := DefaultEngineConfig()
	base.CrossVenueScanner.Enabled = true

	cases := []struct {
		name string
		mut  func(*EngineConfig)
		want bool
	}{
		{
			name: "shadow 跳过租约",
			mut: func(c *EngineConfig) {
				c.ExecutionConfig.ShadowTrading = true
				c.CrossVenueExecutor.Enabled = true
				c.UnifiedRotation.Enabled = true
			},
			want: false,
		},
		{
			name: "仅扫描器不强制",
			mut: func(c *EngineConfig) {
				c.CrossVenueExecutor.Enabled = false
				c.UnifiedRotation.Enabled = false
			},
			want: false,
		},
		{
			name: "scanner+executor 需租约",
			mut: func(c *EngineConfig) {
				c.CrossVenueExecutor.Enabled = true
				c.UnifiedRotation.Enabled = false
			},
			want: true,
		},
		{
			name: "unified_rotation 需租约",
			mut: func(c *EngineConfig) {
				c.CrossVenueExecutor.Enabled = false
				c.UnifiedRotation.Enabled = true
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mut(&cfg)
			if got := requiresLiveSingleWriterLease(cfg); got != tc.want {
				t.Fatalf("requiresLiveSingleWriterLease()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestAcquireLiveStackSingleWriterLeaseSuccessAndRelease(t *testing.T) {
	dir := t.TempDir()
	cfg := liveStackLeaseTestConfig(dir)
	cfg.CrossVenueExecutor.Enabled = true

	first, err := acquireLiveStackSingleWriterLease(cfg)
	if err != nil {
		t.Fatalf("首次获取应成功: %v", err)
	}
	if first == nil {
		t.Fatal("真钱装配下租约不应为 nil")
	}

	second, err := acquireLiveStackSingleWriterLease(cfg)
	if err == nil {
		if second != nil {
			_ = second.Release()
		}
		t.Fatal("持锁期间第二次获取应失败")
	}
	if !strings.Contains(err.Error(), "已有 lingxi live 实例在运行,拒绝双开") {
		t.Fatalf("错误应说明拒绝双开, got %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("释放租约失败: %v", err)
	}

	third, err := acquireLiveStackSingleWriterLease(cfg)
	if err != nil {
		t.Fatalf("释放后应可再次获取: %v", err)
	}
	if third == nil {
		t.Fatal("释放后再次获取租约不应为 nil")
	}
	_ = third.Release()
}

func TestAcquireLiveStackSingleWriterLeaseSkipsWhenNotRequired(t *testing.T) {
	dir := t.TempDir()
	cfg := liveStackLeaseTestConfig(dir)
	cfg.ExecutionConfig.ShadowTrading = true
	cfg.CrossVenueExecutor.Enabled = true

	lease, err := acquireLiveStackSingleWriterLease(cfg)
	if err != nil {
		t.Fatalf("shadow 模式不应因租约失败: %v", err)
	}
	if lease != nil {
		t.Fatal("shadow 模式应跳过租约")
	}
}

func TestAcquireLiveStackSingleWriterLeaseStaleTakeover(t *testing.T) {
	dir := t.TempDir()
	cfg := liveStackLeaseTestConfig(dir)
	cfg.UnifiedRotation.Enabled = true

	// 模拟崩溃:持锁 fd 被 close 后 flock 由内核释放,新实例可接管(无 PID 检测,沿用 flock 语义)。
	stale, err := acquireSingleWriterLease(dir)
	if err != nil {
		t.Fatalf("首个租约应成功: %v", err)
	}
	if stale.file == nil {
		t.Fatal("stale lease 应有底层 fd")
	}
	if err := stale.file.Close(); err != nil {
		t.Fatalf("关闭陈旧持锁 fd 失败: %v", err)
	}
	stale.file = nil

	takeover, err := acquireLiveStackSingleWriterLease(cfg)
	if err != nil {
		t.Fatalf("陈旧锁释放后应可接管: %v", err)
	}
	if takeover == nil {
		t.Fatal("接管后租约不应为 nil")
	}
	_ = takeover.Release()
}

func TestStartLiveStackRejectsWhenLeaseHeld(t *testing.T) {
	dir := t.TempDir()
	holder, err := acquireSingleWriterLease(dir)
	if err != nil {
		t.Fatalf("预占租约失败: %v", err)
	}
	defer func() { _ = holder.Release() }()

	origAcquire := liveStackLeaseAcquire
	t.Cleanup(func() { liveStackLeaseAcquire = origAcquire })
	liveStackLeaseAcquire = acquireSingleWriterLease

	var tradingLoopsStarted int32
	origScannerRun := crossVenueScannerRunHook
	origRotationRun := unifiedRotationRunHook
	t.Cleanup(func() {
		crossVenueScannerRunHook = origScannerRun
		unifiedRotationRunHook = origRotationRun
	})
	crossVenueScannerRunHook = func(*CrossVenueScanner, context.Context) {
		atomic.AddInt32(&tradingLoopsStarted, 1)
	}
	unifiedRotationRunHook = func(*UnifiedRotationRunner, context.Context) {
		atomic.AddInt32(&tradingLoopsStarted, 1)
	}

	cfg := liveStackLeaseTestConfig(dir)
	cfg.CrossVenueExecutor.Enabled = true
	cfg.NetworkConfig.DashboardAddr = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = startLiveStack(ctx, cfg)
	if err == nil {
		t.Fatal("持锁时 startLiveStack 应 fail-fast")
	}
	if !strings.Contains(err.Error(), "已有 lingxi live 实例在运行,拒绝双开") {
		t.Fatalf("错误应说明拒绝双开, got %v", err)
	}
	if atomic.LoadInt32(&tradingLoopsStarted) != 0 {
		t.Fatalf("抢锁失败时不应启动交易循环, got %d", tradingLoopsStarted)
	}
}

func liveStackLeaseTestConfig(recordDir string) EngineConfig {
	cfg := DefaultEngineConfig()
	cfg.NetworkConfig.RecordDir = recordDir
	cfg.CrossVenueScanner.Enabled = true
	cfg.CrossVenueScanner.ScanIntervalMinutes = 60
	cfg.CrossVenueScanner.TopN = 5
	cfg.CrossVenueScanner.SpreadPersistenceScans = 2
	cfg.CrossVenueScanner.FundingAPRSanityCap = 5
	cfg.CrossVenueExecutor.LoopIntervalSeconds = 60
	cfg.CrossVenueExecutor.MaxConcurrent = 1
	cfg.CrossVenueExecutor.MaxPositionUSDT = 10
	cfg.CrossVenueExecutor.MaxEffectiveLeverage = 1
	cfg.CrossVenueExecutor.MinHoldHours = 1
	cfg.CrossVenueExecutor.EVMaxBreakevenHours = 1
	cfg.CrossVenueExecutor.EVSafetyFactor = 1
	cfg.CrossVenueExecutor.RealizedFundingPollMinutes = 60
	return cfg
}
