package replay

import "testing"

func TestBestActionMakerWins(t *testing.T) {
	in := ActionInputs{
		NotionalUSDT:     1000,
		EdgeLowerBps:     8,
		MakerFillProbLow: 0.4, MakerFillProbHigh: 0.7,
		MakerFeeBps: 1, MakerToxicityBps: 2, MakerUnfilledDragBps: 1,
		TakerFeeBps: 5, HalfSpreadBps: 2.5, TakerSlipWorstBps: 2,
	}
	best := BestAction(in)
	if best.Style != ExecMaker {
		t.Fatalf("want MAKER got %s (%+v)", best.Style, CompareActions(in))
	}
	// cond = 8-2-1 = 5 ≥0 → 0.4×5=2.0;再减未成交拖累 (1-0.4)×1=0.6 → 1.4 bps。
	if !almostEqual(best.LowerNetBps, 1.4, 1e-9) {
		t.Fatalf("maker 下界 want 1.4bps got %v", best.LowerNetBps)
	}
	if !almostEqual(best.LowerNetUSDT, 0.14, 1e-9) {
		t.Fatalf("maker 下界 USDT want 0.14 got %v", best.LowerNetUSDT)
	}
}

func TestBestActionNoOrderWhenEdgeTooSmall(t *testing.T) {
	in := ActionInputs{
		NotionalUSDT:     1000,
		EdgeLowerBps:     2,
		MakerFillProbLow: 0.4, MakerFillProbHigh: 0.9,
		MakerFeeBps: 1, MakerToxicityBps: 3, MakerUnfilledDragBps: 1,
		TakerFeeBps: 5, HalfSpreadBps: 2.5, TakerSlipWorstBps: 2,
	}
	bounds := CompareActions(in)
	if bounds[0].Style != ExecNoOrder {
		t.Fatalf("边际不足时应空仓,got %s", bounds[0].Style)
	}
	// maker cond = 2-3-1 = -2 <0 → 保守用 probHigh:0.9×(-2)=-1.8;减拖累 0.6 → -2.4。
	for _, b := range bounds {
		if b.Style == ExecMaker && !almostEqual(b.LowerNetBps, -2.4, 1e-9) {
			t.Fatalf("负条件收益应按成交概率上界计,want -2.4 got %v", b.LowerNetBps)
		}
	}
}

func TestBestActionTakerWinsOnBigEdge(t *testing.T) {
	in := ActionInputs{
		NotionalUSDT:     1000,
		EdgeLowerBps:     30,
		MakerFillProbLow: 0.2, MakerFillProbHigh: 0.5,
		MakerFeeBps: 1, MakerToxicityBps: 10,
		TakerFeeBps: 5, HalfSpreadBps: 2, TakerSlipWorstBps: 3,
	}
	best := BestAction(in)
	if best.Style != ExecTaker {
		t.Fatalf("大边际低成交率时应吃单,got %s", best.Style)
	}
	if !almostEqual(best.LowerNetBps, 20, 1e-9) {
		t.Fatalf("taker 下界 want 20bps got %v", best.LowerNetBps)
	}
}

func TestBestActionTiePrefersConservative(t *testing.T) {
	// 全零输入:三个动作下界都为 0,应偏向 NO_ORDER。
	best := BestAction(ActionInputs{NotionalUSDT: 1000})
	if best.Style != ExecNoOrder {
		t.Fatalf("并列时应偏保守 NO_ORDER,got %s", best.Style)
	}
}
