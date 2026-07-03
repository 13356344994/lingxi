package app

// account_risk_watch_wiring_test:回归保护 2026-07-02 接线的 Stage B 关键路径——
// ① settlements 加载失败必须告警(此前被 `_` 静默吞掉=风控静默失效);
// ② 一级硬停触发 haltFn+告警,且已 halt 态不重复推送(幂等收敛);
// ③ 二级 flattenFn 生产接线后:opt-in 开启 + 阈值触发 → flatten 执行且幂等。

import (
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

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
	// 抬高单 tick 骤降阈值,避免 100→80 被判为数据异常(本测试专测 flatten 路径)。
	cfg.RiskConfig.AccountRiskMaxEquityTickDropUSDT = 1000
	w, _ := newWiringTestWatcher(t, cfg)
	// 真实现金 peak=100 current=80 → 回撤 20 ≥ 10
	w.realEquityFn = func(string) (float64, float64, float64) { return 80, 100, 20 }
	w.equityFn = func(now time.Time) accountEquityObservation {
		return accountEquityObservation{
			TotalUSDT:     80,
			ByVenue:       map[string]float64{"okx": 80},
			RawDailyDelta: 0,
			ReadOK:        true,
		}
	}
	flattenCalls := 0
	w.flattenFn = func(ctx context.Context) error { flattenCalls++; return nil }
	w.haltFn = func(string) error { return nil }

	w.tickOnce()
	if flattenCalls != 1 {
		t.Fatalf("二级全平应执行一次,实际 %d", flattenCalls)
	}
	w.tickOnce()
	if flattenCalls != 1 {
		t.Fatalf("autoFlattenDone 幂等失效,重复 flatten: %d", flattenCalls)
	}
}
