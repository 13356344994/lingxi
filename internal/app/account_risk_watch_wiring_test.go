package app

// account_risk_watch_wiring_test:回归保护 2026-07-02 接线的 Stage B 关键路径——
// ① settlements 加载失败必须告警(此前被 `_` 静默吞掉=风控静默失效);
// ② 一级硬停触发 haltFn+告警,且已 halt 态不重复推送(幂等收敛);
// ③ 二级 flattenFn 生产接线后:opt-in 开启 + 阈值触发 → flatten 执行且幂等。

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	risktruth "lingxi/internal/risk"
	"lingxi/internal/venue"
)

// newWiringTestWatcher 构造全 mock 数据源的 watcher(权益可信、无持仓、默认无结算)。
func newWiringTestWatcher(t *testing.T, cfg EngineConfig) (*accountRiskWatcher, *[]string) {
	t.Helper()
	var notified []string
	w := &accountRiskWatcher{
		cfg:       cfg,
		recordDir: t.TempDir(),
		logger:    log.New(&strings.Builder{}, "", 0),
		nowFn:     time.Now,
	}
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.equityFn = func(now time.Time) accountEquityObservation {
		return accountEquityObservation{
			TotalUSDT:     100,
			ByVenue:       map[string]float64{"okx": 100},
			RawDailyDelta: 0,
			ReadOK:        true,
		}
	}
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(string) (float64, float64, float64) { return 100, 100, 0 }
	w.realEquityQualityFn = func(string) realEquityQualityResult {
		return realEquityQualityResult{Passed: true, PeakConfirmed: true}
	}
	// lookup 恒返回 nil → 充提识别走 heuristic 路径(不触网)。
	w.fundFlowLookup = func(string) venue.ExternalFundFlowProvider { return nil }
	w.notifyFn = func(title, detail string) { notified = append(notified, title) }
	return w, &notified
}

func TestAccountRiskWatch_SettlementLoadErrorAlerts(t *testing.T) {
	w, notified := newWiringTestWatcher(t, DefaultEngineConfig())
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, errors.New("坏文件") }
	w.tickOnce()
	found := false
	for _, title := range *notified {
		if title == "账户风控数据源异常" {
			found = true
		}
	}
	if !found {
		t.Fatalf("settlements 加载失败未触发告警,收到: %v", *notified)
	}
}

func TestAccountRiskWatch_HardHaltIdempotent(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 5
	cfg.RiskConfig.MaxDailyLossPct = 0
	w, notified := newWiringTestWatcher(t, cfg)
	// 当日权益 delta -10 < -5 → 硬停。结算解释全部亏损,避免 heuristic 充提识别把 -10 当成提现平移掉。
	w.settlementFn = func() ([]strategySettlementRecord, error) {
		return []strategySettlementRecord{{
			StrategyType:       "directional_okx",
			Symbol:             "BTC-USDT",
			CloseTime:          time.Now().UTC().Add(-time.Minute), // 须早于聚合窗口上界(tick 的 now)
			NetPnL:             -10,
			FundingTruthStatus: "trusted",
		}}, nil
	}
	w.equityFn = func(now time.Time) accountEquityObservation {
		return accountEquityObservation{
			TotalUSDT:     90,
			ByVenue:       map[string]float64{"okx": 90},
			RawDailyDelta: -10,
			ReadOK:        true,
		}
	}
	haltCalls := 0
	halted := false
	w.haltFn = func(reason string) error { haltCalls++; halted = true; return nil }
	w.haltActiveFn = func() bool { return halted }

	w.tickOnce()
	if haltCalls != 1 {
		t.Fatalf("首次超标应触发 haltFn 一次,实际 %d", haltCalls)
	}
	firstNotify := len(*notified)
	if firstNotify == 0 {
		t.Fatal("首次硬停未告警")
	}
	// 第二个 tick:仍超标但已 halt → 不重复写盘/不重复推送
	w.tickOnce()
	if haltCalls != 1 {
		t.Fatalf("已 halt 态不应重复调用 haltFn,实际 %d", haltCalls)
	}
	if len(*notified) != firstNotify {
		t.Fatalf("已 halt 态不应重复告警,新增 %d 条", len(*notified)-firstNotify)
	}
}

func TestAccountRiskWatch_AutoFlattenWiredAndIdempotent(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled = true
	cfg.RiskConfig.AutoFlattenAccountDrawdownUSDT = 10
	cfg.RiskConfig.AutoFlattenAccountDrawdownPct = 0
	cfg.RiskConfig.AccountRiskMinFundFlowStepUSDT = 100
	// 抬高单 tick 骤降阈值,避免 100→80 被判为数据异常(本测试专测 flatten 路径)。
	cfg.RiskConfig.AccountRiskMaxEquityTickDropUSDT = 1000
	w, _ := newWiringTestWatcher(t, cfg)
	now := time.Now().UTC()
	w.nowFn = func() time.Time { return now }
	current := 100.0
	// 先建立干净前沿，再模拟无资金流解释的大额权益下降。
	w.realEquityFn = func(string) (float64, float64, float64) { return current, 100, 100 - current }
	w.positionFn = func() accountRiskPositionSummary {
		return accountRiskPositionSummary{TotalCount: 1, OpenNotional: 20}
	}
	w.equityFn = func(now time.Time) accountEquityObservation {
		return accountEquityObservation{
			TotalUSDT:     current,
			ByVenue:       map[string]float64{"okx": current},
			RawDailyDelta: 0,
			ReadOK:        true,
		}
	}
	flattenCalls := 0
	w.flattenFn = func(ctx context.Context) error { flattenCalls++; return nil }
	w.haltFn = func(string) error { return nil }

	w.tickOnce()
	current = 80
	now = now.Add(time.Minute)
	w.tickOnce()
	if flattenCalls != 0 {
		t.Fatalf("大额权益变化在资金流配对窗内不应立即全平,实际 %d", flattenCalls)
	}
	for elapsed := time.Minute; elapsed <= risktruth.EquityTruthMatchWindow+time.Minute; elapsed += time.Minute {
		now = now.Add(time.Minute)
		w.tickOnce()
	}
	if flattenCalls != 1 {
		t.Fatalf("待配对窗口过期后真实回撤应执行一次全平,实际 %d", flattenCalls)
	}
	w.tickOnce()
	if flattenCalls != 1 {
		t.Fatalf("autoFlattenDone 幂等失效,重复 flatten: %d", flattenCalls)
	}
}

func TestAccountRiskPositionSummaryIncludesFast(t *testing.T) {
	root := t.TempDir()
	dir := directionalRecordDir(root, "okx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, okxDirectionalPositionFile), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fast := `[{"symbol":"CAP-USDT","notional_usdt":7.5}]`
	if err := os.WriteFile(filepath.Join(dir, fastLivePositionFile), []byte(fast), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &accountRiskWatcher{recordDir: root}
	got := w.collectPositionSummary()
	if got.TotalCount != 1 || got.OpenNotional != 7.5 || got.ByStrategy["fast_directional_okx"] != 1 {
		t.Fatalf("fast 仓未纳入账户风控汇总: %+v", got)
	}
}

// TestAccountDailyLossCorroboration 双账本佐证制(2026-07-08,42U 划转误停事故根治):
// 权益口径日亏触线时,须能在交易账本(在场仓/当日已实现)找到踪迹才许硬停;
// 无仓且已实现可忽略 → 降级「未解释资金流出」告警,不硬停。
func TestAccountDailyLossCorroboration(t *testing.T) {
	const lossLimit = 35.0
	cases := []struct {
		name        string
		snap        accountRiskSnapshot
		corroborate bool
	}{
		{"42U划转型:无仓+已实现0+权益-43", accountRiskSnapshot{
			EquityDataTrusted: true, DailyEquityDeltaForRiskUSDT: -43.4, DailyRealizedPnLUSDT: 0,
		}, false},
		{"真亏损型:无仓但已实现-30佐证", accountRiskSnapshot{
			EquityDataTrusted: true, DailyEquityDeltaForRiskUSDT: -40, DailyRealizedPnLUSDT: -30,
		}, true},
		{"持仓扛亏型:有在场仓即佐证(未实现亏损无需查已实现)", accountRiskSnapshot{
			EquityDataTrusted: true, DailyEquityDeltaForRiskUSDT: -40, DailyRealizedPnLUSDT: 0,
			OpenPositionCount: 2, OpenNotionalUSDT: 15,
		}, true},
		{"边界:已实现亏损恰为触发线两成即佐证", accountRiskSnapshot{
			EquityDataTrusted: true, DailyEquityDeltaForRiskUSDT: -36, DailyRealizedPnLUSDT: -0.2 * lossLimit,
		}, true},
	}
	cfg := RiskConfig{MaxDailyLossUSDT: lossLimit}
	for _, tc := range cases {
		got := accountDailyLossCorroboratedByTrading(tc.snap, lossLimit)
		if got != tc.corroborate {
			t.Fatalf("%s: 佐证=%v 期望 %v", tc.name, got, tc.corroborate)
		}
		th := evaluateAccountRiskThresholds(tc.snap, cfg)
		halt := evaluateAccountHardHaltIntent(tc.snap, cfg)
		if tc.corroborate {
			if !th.DailyLossBreached || !halt.DailyLossBreached {
				t.Fatalf("%s: 佐证成立应照常触发日亏闸(th=%v halt=%v)", tc.name, th.DailyLossBreached, halt.DailyLossBreached)
			}
			if th.UnexplainedOutflow {
				t.Fatalf("%s: 佐证成立不应标未解释流出", tc.name)
			}
		} else {
			if th.DailyLossBreached || halt.DailyLossBreached || halt.ShouldHalt {
				t.Fatalf("%s: 佐证失败不应硬停(th=%v halt=%v)", tc.name, th.DailyLossBreached, halt.ShouldHalt)
			}
			if !th.UnexplainedOutflow || th.UnexplainedOutflowReason == "" {
				t.Fatalf("%s: 佐证失败必须标「未解释资金流出」并给原因", tc.name)
			}
			intents := accountRiskAlertIntents(th)
			if len(intents) == 0 || intents[0].Title != "未解释资金流出(对账告警,未停机)" {
				t.Fatalf("%s: 应产出未解释资金流出告警,实际 %+v", tc.name, intents)
			}
		}
	}
}

// TestAccountRiskWatch_UnexplainedOutflowNoHalt 接线级:42U 场景全链路——权益骤降但
// 无仓无已实现亏损 → haltFn 不调用、告警必须发出(用户能看见,但机器不停)。
func TestAccountRiskWatch_UnexplainedOutflowNoHalt(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 35
	cfg.RiskConfig.MaxDailyLossPct = 0
	// 抬高单 tick 骤降阈值,绕过 equity trust 闸,专测日亏佐证路径。
	cfg.RiskConfig.AccountRiskMaxEquityTickDropUSDT = 1000
	// 抬高充提识别步长,避免 heuristic 把 -43 先平移掉(专测佐证降级路径)。
	cfg.RiskConfig.AccountRiskMinFundFlowStepUSDT = 500
	w, notified := newWiringTestWatcher(t, cfg)
	w.equityFn = func(now time.Time) accountEquityObservation {
		return accountEquityObservation{
			TotalUSDT:     57,
			ByVenue:       map[string]float64{"okx": 57},
			RawDailyDelta: -43.4,
			ReadOK:        true,
		}
	}
	haltCalls := 0
	w.haltFn = func(reason string) error { haltCalls++; return nil }
	w.haltActiveFn = func() bool { return false }
	w.tickOnce()
	if haltCalls != 0 {
		t.Fatalf("未佐证的权益流出不应硬停,haltFn 被调用 %d 次", haltCalls)
	}
	found := false
	for _, title := range *notified {
		if title == "未解释资金流出(对账告警,未停机)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应发出未解释资金流出告警,收到: %v", *notified)
	}
}
