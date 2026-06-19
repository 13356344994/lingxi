package venue

// conformance.go 提供「接新所」的离线契约一致性套件(MULTI_VENUE_REFACTOR.md P4 前置)。
//
// 目的:把「一个合格的 venue.Venue 适配器必须满足的契约」固化成一组可复用断言,任何新所
// (bybit/okx/gate)接入时只需 `venue.RunConformance(t, adapter, cfg)` 即可在 CI 里先过契约,
// 再进 OFF / 小额实盘。本套件**只跑离线/纯函数检查**:能力自洽、符号映射往返、调仓分配不变式、
// 错误分类、以及「声明不支持的能力必须返回 ErrUnsupported」。它**绝不触网、不下单、不划转、不借还**,
// 因此对真实适配器也安全。网络/下单/流式方法由各所在线测或 mock 另行覆盖。
//
// 设计:用最小 TB 接口接住 *testing.T,使本文件留在生产包(可被各所测试 import)而不依赖 testing。

import (
	"context"
	"math"
)

// TB 是 *testing.T 的最小子集,使一致性套件无需 import testing 即可被各所测试复用。
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// ConformanceConfig 描述被测适配器的样例输入与可选检查项。
type ConformanceConfig struct {
	// SampleSymbols 用于符号映射往返(双市场各跑一遍),如 {"BTC-USDT","ETH-USDT"}。为空则跳过映射检查。
	SampleSymbols []Symbol
	// SkipSymbolMapping:适配器映射依赖在线 instruments 表时置 true(改由该所在线测覆盖),纯字符串映射可留 false。
	SkipSymbolMapping bool
	// CheckOrderBudget:仅当传入「持有可用内核(如真实/离线 client)」的适配器时开启;否则部分适配器
	// 的 ReserveOrderBudget 可能解引用 nil 内核而 panic,故默认关闭。
	CheckOrderBudget bool
}

// validFundingIntervals 是允许的资金费周期(小时)。币安 8/4,部分所有 1/2。
var validFundingIntervals = map[int]bool{1: true, 2: true, 4: true, 8: true}

// RunConformance 对一个已构造的 Venue 跑离线契约一致性套件。所有断言均为离线/纯函数,
// 不触网、不下单、不划转、不借还,可在 CI 与「接新所」时复用。
func RunConformance(t TB, v Venue, cfg ConformanceConfig) {
	t.Helper()
	if v == nil {
		t.Fatalf("conformance: venue 为 nil")
		return
	}

	if v.Name() == "" {
		t.Errorf("conformance: Name() 不应为空")
	}

	caps := v.Capabilities()
	checkCapabilities(t, v, caps)

	if !cfg.SkipSymbolMapping {
		checkSymbolMapping(t, v, cfg.SampleSymbols)
	}

	checkCapitalAllocation(t, v, caps)
	checkErrorClassification(t, v)
	checkUnsupportedContract(t, v, caps)
	checkBorrowQuoteContract(t, v, caps)

	if cfg.CheckOrderBudget {
		// 预留 0 单位应为安全无副作用的探针;随后归还,确保不污染预算账本。
		_ = v.ReserveOrderBudget(0)
		v.ReleaseOrderBudget(0)
	}
}

// checkCapabilities 校验能力声明自洽:资金费周期合法去重、批量上限合理、调仓能力与钱包集一致。
func checkCapabilities(t TB, v Venue, caps Capabilities) {
	t.Helper()
	if len(caps.FundingIntervalsHours) == 0 {
		t.Errorf("conformance[%s]: FundingIntervalsHours 不应为空(至少声明一个支持周期)", v.Name())
	}
	seen := make(map[int]bool, len(caps.FundingIntervalsHours))
	for _, h := range caps.FundingIntervalsHours {
		if !validFundingIntervals[h] {
			t.Errorf("conformance[%s]: 非法资金费周期 %dh(允许 1/2/4/8)", v.Name(), h)
		}
		if seen[h] {
			t.Errorf("conformance[%s]: 资金费周期 %dh 重复声明", v.Name(), h)
		}
		seen[h] = true
	}
	if caps.MaxBatchOrders < 1 {
		t.Errorf("conformance[%s]: MaxBatchOrders=%d 应 ≥ 1(单笔也算一批)", v.Name(), caps.MaxBatchOrders)
	}
	if caps.ContractBasedPerp {
		// 按张下单的所必须在 Instrument.ContractSize 里给面值,这里只能提示由在线测进一步校验。
		// 契约层先确保该所未把 QtyUnit 留空的隐含假设带进引擎(留待 instruments 在线测)。
		_ = caps
	}
	if caps.SupportsCapitalRebalance && len(caps.CapitalWallets) == 0 {
		t.Errorf("conformance[%s]: 声明支持自动调仓但 CapitalWallets 为空(无钱包可分配)", v.Name())
	}
}

// checkSymbolMapping 校验符号映射往返稳定:ToVenue 非空,且 ToCanonical(raw) 还原回规范符号;
// ToCanonical 幂等(对已规范符号再映射不变)。
func checkSymbolMapping(t TB, v Venue, samples []Symbol) {
	t.Helper()
	if len(samples) == 0 {
		return
	}
	for _, m := range []Market{MarketSpot, MarketPerp} {
		for _, canonical := range samples {
			raw, err := v.ToVenue(canonical, m)
			if err != nil {
				t.Errorf("conformance[%s]: ToVenue(%s,%s) 报错 %v", v.Name(), canonical, m, err)
				continue
			}
			if raw == "" {
				t.Errorf("conformance[%s]: ToVenue(%s,%s) 返回空原始符号", v.Name(), canonical, m)
				continue
			}
			back, err := v.ToCanonical(raw, m)
			if err != nil {
				t.Errorf("conformance[%s]: ToCanonical(%q,%s) 报错 %v", v.Name(), raw, m, err)
				continue
			}
			if back != canonical {
				t.Errorf("conformance[%s]: 映射往返不稳定 %s -> %q -> %s(应还原)", v.Name(), canonical, raw, back)
			}
			// 幂等:对已规范符号再 ToCanonical 应不变。
			if again, err := v.ToCanonical(string(canonical), m); err == nil && again != canonical {
				t.Errorf("conformance[%s]: ToCanonical 对已规范符号非幂等 %s -> %s", v.Name(), canonical, again)
			}
		}
	}
}

// checkCapitalAllocation 校验 PlanAllocation 的纯函数不变式:总额回填、目标非负、不超分配、钱包归属合法。
func checkCapitalAllocation(t TB, v Venue, caps Capabilities) {
	t.Helper()
	walletSet := make(map[Wallet]bool, len(caps.CapitalWallets))
	for _, w := range caps.CapitalWallets {
		walletSet[w] = true
	}
	for _, total := range []float64{0, 100, 1_000_000} {
		plan := v.PlanAllocation(total, caps)
		if !approxEqualFloat(plan.TotalTradingUSDT, total) {
			t.Errorf("conformance[%s]: PlanAllocation(%.0f).TotalTradingUSDT=%.6f 应等于入参", v.Name(), total, plan.TotalTradingUSDT)
		}
		sum := 0.0
		for w, amt := range plan.Targets {
			if amt < 0 {
				t.Errorf("conformance[%s]: PlanAllocation(%.0f) 钱包 %q 分配为负 %.6f", v.Name(), total, w, amt)
			}
			if len(walletSet) > 0 && !walletSet[w] && w != plan.RemainderWallet {
				t.Errorf("conformance[%s]: PlanAllocation(%.0f) 目标钱包 %q 不在 CapitalWallets 内", v.Name(), total, w)
			}
			sum += amt
		}
		if sum > total+1e-6 {
			t.Errorf("conformance[%s]: PlanAllocation(%.0f) 分配总和 %.6f 超过总额(凭空多分)", v.Name(), total, sum)
		}
	}
}

// checkErrorClassification 校验 Classify 契约:nil → 空类目;非 nil → 非空类目与等级。
func checkErrorClassification(t TB, v Venue) {
	t.Helper()
	if cat, sev := v.Classify(nil); cat != "" || sev != "" {
		t.Errorf("conformance[%s]: Classify(nil) 应返回空类目/等级, got (%q,%q)", v.Name(), cat, sev)
	}
	cat, sev := v.Classify(ErrUnsupported)
	if cat == "" || sev == "" {
		t.Errorf("conformance[%s]: Classify(非nil) 应返回非空类目与等级, got (%q,%q)", v.Name(), cat, sev)
	}
}

// checkUnsupportedContract 校验「声明不支持的能力必须返回 ErrUnsupported」(负向能力一致性)。
// 这些调用对「不支持」的所是纯返回,不触网;对「支持」的所则跳过(不在契约层触发真实写动作)。
func checkUnsupportedContract(t TB, v Venue, caps Capabilities) {
	t.Helper()
	ctx := context.Background()
	if !caps.SupportsNegativeCarry {
		if err := v.BorrowRepay(ctx, "USDT", 0, BorrowDirBorrow); err != ErrUnsupported {
			t.Errorf("conformance[%s]: 不支持负 carry 时 BorrowRepay 应返回 ErrUnsupported, got %v", v.Name(), err)
		}
	}
	if !caps.HasNativeDeadMan {
		if err := v.ArmDeadMansSwitch(ctx, 0); err != ErrUnsupported {
			t.Errorf("conformance[%s]: 无原生 Kill-Switch 时 ArmDeadMansSwitch 应返回 ErrUnsupported, got %v", v.Name(), err)
		}
	}
	if !caps.SupportsCapitalRebalance {
		if _, err := v.ExecuteRebalance(ctx, CapitalAllocationPlan{}); err != ErrUnsupported {
			t.Errorf("conformance[%s]: 不支持自动调仓时 ExecuteRebalance 应返回 ErrUnsupported, got %v", v.Name(), err)
		}
	}
}

// checkBorrowQuoteContract 校验未声明 SupportsBorrowQuote 的适配器须对 FetchBorrowQuote 返回 ErrUnsupported。
func checkBorrowQuoteContract(t TB, v Venue, caps Capabilities) {
	t.Helper()
	if caps.SupportsBorrowQuote {
		return
	}
	ctx := context.Background()
	if _, err := v.FetchBorrowQuote(ctx, "BTC"); err != ErrUnsupported {
		t.Errorf("conformance[%s]: 不支持借币行情探测时 FetchBorrowQuote 应返回 ErrUnsupported, got %v", v.Name(), err)
	}
}

func approxEqualFloat(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}
