package replay

import "testing"

func testAccount() StressAccount {
	return StressAccount{
		EquityUSDT:         1000,
		PendingOutflowUSDT: 900,
		Positions: []StressPosition{
			{Symbol: "BTC-USDT-SWAP", Side: SideLong, NotionalUSDT: 2000, Beta: 1},
			{Symbol: "ETH-USDT-SWAP", Side: SideShort, NotionalUSDT: 1000, Beta: 1},
		},
	}
}

func stressByName(t *testing.T, results []StressResult, name string) StressResult {
	t.Helper()
	for _, r := range results {
		if r.Scenario == name {
			return r
		}
	}
	t.Fatalf("场景 %s 缺失: %+v", name, results)
	return StressResult{}
}

func TestRunStressDefaultScenarios(t *testing.T) {
	results := RunStress(testAccount(), DefaultStressScenarios())
	if len(results) != 6 {
		t.Fatalf("默认压力集应有 6 个场景,got %d", len(results))
	}

	// 相关性=1 暴跌 30%:多头 -600、空头 +300 → 净损 300。
	crash := stressByName(t, results, "corr1_crash_30")
	if !almostEqual(crash.LossUSDT, 300, 1e-6) || crash.Ruined {
		t.Fatalf("corr1_crash_30: %+v", crash)
	}
	// 全多暴跌 20%:只打多头 → 损 400。
	allLong := stressByName(t, results, "all_long_crash_20")
	if !almostEqual(allLong.LossUSDT, 400, 1e-6) {
		t.Fatalf("all_long_crash_20: %+v", allLong)
	}
	// 全空暴涨 20%:只打空头 → 损 200。
	allShort := stressByName(t, results, "all_short_squeeze_20")
	if !almostEqual(allShort.LossUSDT, 200, 1e-6) {
		t.Fatalf("all_short_squeeze_20: %+v", allShort)
	}
	// 流动性蒸发:-10% 净损 100 + 全名义 5% 退出摩擦 150 = 250。
	liq := stressByName(t, results, "liquidity_evaporation")
	if !almostEqual(liq.LossUSDT, 250, 1e-6) {
		t.Fatalf("liquidity_evaporation: %+v", liq)
	}
	// API 断流:-10% 追加同号 10% → -20% 净损 200;pending 900 占用后缓冲 -100 → 破产。
	outage := stressByName(t, results, "api_outage_pending_outflow")
	if !almostEqual(outage.LossUSDT, 200, 1e-6) || !outage.Ruined {
		t.Fatalf("api_outage_pending_outflow 应判破产: %+v", outage)
	}

	worst := WorstStress(results)
	if worst.Scenario != "api_outage_pending_outflow" {
		t.Fatalf("最差场景应是 api 断流+pending 转出,got %s", worst.Scenario)
	}
}

func TestRunStressBetaScaling(t *testing.T) {
	acct := StressAccount{
		EquityUSDT: 1000,
		Positions:  []StressPosition{{Symbol: "ALT-USDT-SWAP", Side: SideLong, NotionalUSDT: 500, Beta: 2}},
	}
	results := RunStress(acct, []StressScenario{{Name: "crash10", MarketMovePct: -0.10}})
	// beta=2:冲击 -10% → 仓位 -20% × 500 = -100。
	if !almostEqual(results[0].LossUSDT, 100, 1e-6) {
		t.Fatalf("beta 放大未生效: %+v", results[0])
	}
	if !almostEqual(results[0].SurvivingBufferPct, 0.9, 1e-9) {
		t.Fatalf("剩余缓冲 want 0.9 got %v", results[0].SurvivingBufferPct)
	}
}
