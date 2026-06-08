package app

import (
	"strings"
	"testing"
	"time"

	"lingxi/internal/core"
)

// positive_carry_floor_test.go 覆盖 B 方案(正 carry 解耦低门槛)与 A 方案(借币可借性感知选币)。

// baseStrategyCfg 构造一个盈亏平衡线 = 0.0024+0.0005+0.0005 = 0.0034 的配置,
// MinEstimatedFundingRate(负 carry 地板线)= 0.0055 高于平衡线,便于验证正 carry 的解耦下调。
func baseStrategyCfg() EngineConfig {
	cfg := EngineConfig{StrategyConfig: core.StrategyConfig{
		MinEstimatedFundingRate: 0.0055,
		MaxScanBasisRate:        0.08,
		FeeReserveRate:          0.0024,
		ExitSpreadReserveRate:   0.0005,
		MaxFundingRateJump:      1, // 不卡跳变
	}}
	cfg.RiskConfig.MinNetProfitRate = 0.0005
	return cfg
}

// TestPositiveCarryDecoupledFloor:正费率 0.40%(< 负地板线 0.55%,但 ≥ 平衡线 0.34%)。
// 关闭解耦(=0)时应被「正向费率不足」挡下;开启解耦(0.0034)后应放行。
func TestPositiveCarryDecoupledFloor(t *testing.T) {
	strategy := NewDefaultFundingStrategy()
	profile := symbolRiskProfile{MinEstimatedRateMultiplier: 1, MinNetProfitMultiplier: 1}
	snap := FundingSnapshot{
		Symbol:        "AAAUSDT",
		EstimatedRate: 0.0040,
		Direction:     fundingCarryDirectionPositive,
	}

	// 关闭解耦:正负共用 0.0055,0.40% 不足 → 拒。
	cfgOff := baseStrategyCfg()
	if action, reason := strategy.EvaluateScanCandidate(FundingCandidate{Symbol: "AAAUSDT"}, snap, profile, cfgOff); action != StrategyActionReject || !strings.Contains(reason, "正向费率不足") {
		t.Fatalf("关闭解耦时 0.40%% 正 carry 应被费率不足挡下, got action=%v reason=%q", action, reason)
	}

	// 开启解耦(正地板线 0.0034 = 平衡线):0.40% ≥ 0.34% → 放行。
	cfgOn := baseStrategyCfg()
	cfgOn.StrategyConfig.PositiveMinEstimatedFundingRate = 0.0034
	if action, reason := strategy.EvaluateScanCandidate(FundingCandidate{Symbol: "AAAUSDT"}, snap, profile, cfgOn); action != StrategyActionOpen {
		t.Fatalf("开启解耦后 0.40%% 正 carry 应放行, got action=%v reason=%q", action, reason)
	}
}

// TestPositiveCarryFloorNeverBelowBreakEven:即便把正地板线设得低于平衡线(0.001),
// 仍被平衡线 0.0034 硬兜底——0.30% 正费率(< 0.34% 平衡线)必须被拒,绝不放进结构性亏损单。
func TestPositiveCarryFloorNeverBelowBreakEven(t *testing.T) {
	strategy := NewDefaultFundingStrategy()
	profile := symbolRiskProfile{MinEstimatedRateMultiplier: 1, MinNetProfitMultiplier: 1}
	cfg := baseStrategyCfg()
	cfg.StrategyConfig.PositiveMinEstimatedFundingRate = 0.001 // 故意低于平衡线
	snap := FundingSnapshot{Symbol: "BBBUSDT", EstimatedRate: 0.0030, Direction: fundingCarryDirectionPositive}
	if action, reason := strategy.EvaluateScanCandidate(FundingCandidate{Symbol: "BBBUSDT"}, snap, profile, cfg); action != StrategyActionReject {
		t.Fatalf("低于平衡线的正费率应被硬兜底拒绝, got action=%v reason=%q", action, reason)
	}
}

// TestPositiveCarryDecoupleDoesNotAffectNegative:解耦只动正 carry,负 carry 仍走 0.0055。
// 负费率 -0.40%(绝对值 < 0.55%)即便开了正解耦也应被拒。
func TestPositiveCarryDecoupleDoesNotAffectNegative(t *testing.T) {
	strategy := NewDefaultFundingStrategy()
	profile := symbolRiskProfile{MinEstimatedRateMultiplier: 1, MinNetProfitMultiplier: 1}
	cfg := baseStrategyCfg()
	cfg.StrategyConfig.PositiveMinEstimatedFundingRate = 0.0034
	snap := FundingSnapshot{Symbol: "CCCUSDT", EstimatedRate: -0.0040, Direction: fundingCarryDirectionNegative}
	if action, reason := strategy.EvaluateScanCandidate(FundingCandidate{Symbol: "CCCUSDT"}, snap, profile, cfg); action != StrategyActionReject || !strings.Contains(reason, "负向费率绝对值不足") {
		t.Fatalf("负 carry 仍应走 0.0055 地板线被拒, got action=%v reason=%q", action, reason)
	}
}

// --- A 方案:借币可借性缓存 + 选币阶段剔除 ---

func newBorrowCacheTestEngine() *FundingRateSniperEngine {
	return &FundingRateSniperEngine{
		portfolioManager: portfolioManager{symbolCooldowns: make(map[string]symbolCooldownState)},
	}
}

func TestBorrowUnavailableCacheMarkAndExpire(t *testing.T) {
	e := newBorrowCacheTestEngine()

	if e.isBorrowAssetKnownUnavailable("HOME") {
		t.Fatal("初始不应已知借不到")
	}
	// markBorrowAssetUnavailable 写入 serverNow()+TTL,标记后应在 TTL 内有效。
	e.markBorrowAssetUnavailable("home") // 大小写归一
	if !e.isBorrowAssetKnownUnavailable("HOME") {
		t.Fatal("标记后应已知借不到")
	}
	// 直接置一个已过期的项,验证过期自动失效并清理。
	e.borrowUnavailableUntil["HOME"] = e.serverNow().Add(-time.Minute)
	if e.isBorrowAssetKnownUnavailable("HOME") {
		t.Fatal("过期后应失效")
	}
	if _, ok := e.borrowUnavailableUntil["HOME"]; ok {
		t.Fatal("过期项应被清理")
	}
}

func TestDropBorrowUnavailableNegativeCarry(t *testing.T) {
	e := newBorrowCacheTestEngine()
	e.markBorrowAssetUnavailable("HOME")

	candidates := []FundingCandidate{
		{Symbol: "HOMEUSDT", SpotBaseAsset: "HOME", Direction: fundingCarryDirectionNegative, EstimatedRate: -0.01}, // 借不到的负 carry → 剔除
		{Symbol: "PORTALUSDT", SpotBaseAsset: "PORTAL", Direction: fundingCarryDirectionNegative, EstimatedRate: -0.006}, // 可借的负 carry → 保留
		{Symbol: "HOMEUSDT-P", SpotBaseAsset: "HOME", Direction: fundingCarryDirectionPositive, EstimatedRate: 0.006}, // 正 carry 免借币 → 保留(即便 base 借不到)
	}
	kept, dropped := e.dropBorrowUnavailableNegativeCarry(candidates)
	if dropped != 1 {
		t.Fatalf("应剔除 1 个借不到的负 carry, got dropped=%d", dropped)
	}
	if len(kept) != 2 {
		t.Fatalf("应保留 2 个候选, got %d", len(kept))
	}
	for _, c := range kept {
		if c.Symbol == "HOMEUSDT" {
			t.Fatal("借不到的负 carry HOMEUSDT 不应保留")
		}
	}
}

// loadAllowlist 直接把可借白名单注入缓存(测试用,绕过网络)。
func (e *FundingRateSniperEngine) loadAllowlistForTest(assets map[string]bool) {
	e.borrowableAllowlist.mu.Lock()
	e.borrowableAllowlist.assets = assets
	e.borrowableAllowlist.fetchedAt = e.serverNow()
	e.borrowableAllowlist.loaded = true
	e.borrowableAllowlist.mu.Unlock()
}

func TestIsAssetBorrowableByAllowlistFailOpen(t *testing.T) {
	e := newBorrowCacheTestEngine()
	// 未加载 → known=false(fail-open)。
	if _, known := e.isAssetBorrowableByAllowlist("HOME"); known {
		t.Fatal("白名单未加载时 known 应为 false(fail-open)")
	}
	e.loadAllowlistForTest(map[string]bool{"BTC": true, "HOME": false})
	if b, known := e.isAssetBorrowableByAllowlist("BTC"); !known || !b {
		t.Fatalf("BTC 应已知且可借, got borrowable=%v known=%v", b, known)
	}
	if b, known := e.isAssetBorrowableByAllowlist("HOME"); !known || b {
		t.Fatalf("HOME 应已知且不可借, got borrowable=%v known=%v", b, known)
	}
	// 未列出的资产:已加载时视为不可借(known=true, borrowable=false)。
	if b, known := e.isAssetBorrowableByAllowlist("XYZ"); !known || b {
		t.Fatalf("未列出资产应视为不可借, got borrowable=%v known=%v", b, known)
	}
}

func TestBorrowExclusionReason(t *testing.T) {
	e := newBorrowCacheTestEngine()
	// 白名单未加载 + 反应式缓存空 → 放行(fail-open)。
	if r := e.borrowExclusionReason("HOME"); r != "" {
		t.Fatalf("无任何信息时应放行, got %q", r)
	}
	// 白名单标记不可借 → 排除。
	e.loadAllowlistForTest(map[string]bool{"BTC": true, "HOME": false})
	if r := e.borrowExclusionReason("HOME"); r == "" {
		t.Fatal("白名单不可借应给出排除原因")
	}
	if r := e.borrowExclusionReason("BTC"); r != "" {
		t.Fatalf("白名单可借应放行, got %q", r)
	}
	// 白名单可借但反应式缓存命中(库存被借空)→ 排除。
	e.markBorrowAssetUnavailable("BTC")
	if r := e.borrowExclusionReason("BTC"); r == "" {
		t.Fatal("反应式缓存命中应给出排除原因(库存被借空)")
	}
}

// TestDropUsesAllowlist:白名单把 HOME 标记不可借,负 carry HOME 应在选币阶段被剔除,可借的 BTC 负 carry 保留。
func TestDropUsesAllowlist(t *testing.T) {
	e := newBorrowCacheTestEngine()
	e.loadAllowlistForTest(map[string]bool{"BTC": true, "HOME": false})
	candidates := []FundingCandidate{
		{Symbol: "HOMEUSDT", SpotBaseAsset: "HOME", Direction: fundingCarryDirectionNegative, EstimatedRate: -0.01},
		{Symbol: "BTCUSDT", SpotBaseAsset: "BTC", Direction: fundingCarryDirectionNegative, EstimatedRate: -0.006},
	}
	kept, dropped := e.dropBorrowUnavailableNegativeCarry(candidates)
	if dropped != 1 || len(kept) != 1 || kept[0].Symbol != "BTCUSDT" {
		t.Fatalf("白名单应只剔除 HOME 保留 BTC, got dropped=%d kept=%v", dropped, kept)
	}
}
