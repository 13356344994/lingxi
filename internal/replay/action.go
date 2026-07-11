package replay

import "sort"

// ActionInputs 一次决策点上比较 NO_ORDER / MAKER / TAKER 所需的保守输入。
// 所有 *Bps 相对名义;概率∈[0,1]。口径:优化「条件净收益下界」,不优化成交率
// (计划文档第 3 节)。因此不利项一律取最坏值,有利项一律取下界值。
type ActionInputs struct {
	NotionalUSDT float64

	// EdgeLowerBps 若能成交、方向论点兑现时毛边际的保守下界
	// (调用方应给统计下界,如 bootstrap 置信下界,而非均值)。
	EdgeLowerBps float64

	// maker 路径 ---------------------------------------------------------
	MakerFillProbLow  float64 // 成交概率下界
	MakerFillProbHigh float64 // 成交概率上界(条件收益为负时用它,更保守)
	MakerFeeBps       float64 // maker 费率(负=返佣)
	// MakerToxicityBps 毒性成交惩罚:成交是有条件的——越是逆着你的快市越容易成交,
	// 成交样本的条件边际比无条件边际差,这里按保守上界直接从 edge 里扣掉。
	MakerToxicityBps float64
	// MakerUnfilledDragBps 未成交反事实成本下不为正收益:挂单未成交期间错过的对冲/
	// 撤单竞态等损耗上界(>=0,按未成交概率计提)。
	MakerUnfilledDragBps float64

	// taker 路径 ---------------------------------------------------------
	TakerFeeBps       float64 // taker 费率
	HalfSpreadBps     float64 // 穿越半价差
	TakerSlipWorstBps float64 // 冲击/滑点最坏值(入场即计提)
}

// ActionBound 单个动作的条件净收益下界。
type ActionBound struct {
	Style        ExecStyle
	LowerNetBps  float64
	LowerNetUSDT float64
}

// CompareActions 计算三个动作的条件净收益下界,按下界降序返回;
// BestAction 取下界最大者,且并列时偏向风险更小的动作(NO_ORDER > MAKER > TAKER)。
//
// 下界构造:
//   - NO_ORDER: 恒为 0(空仓没有条件收益,也没有损耗);
//   - MAKER:    条件成交净收益 = edge_lo - toxicity - makerFee;对成交概率取
//     最坏方向(净收益≥0 用 probLow,<0 用 probHigh),再减未成交拖累
//     (1-probLow)×unfilledDrag;
//   - TAKER:    edge_lo - takerFee - halfSpread - slipWorst(成交概率视为 1)。
func CompareActions(in ActionInputs) []ActionBound {
	pLo := clamp01(in.MakerFillProbLow)
	pHi := clamp01(in.MakerFillProbHigh)
	if pHi < pLo {
		pHi = pLo
	}

	makerCond := in.EdgeLowerBps - in.MakerToxicityBps - in.MakerFeeBps
	var makerBps float64
	if makerCond >= 0 {
		makerBps = pLo * makerCond
	} else {
		makerBps = pHi * makerCond
	}
	drag := in.MakerUnfilledDragBps
	if drag < 0 {
		drag = 0
	}
	makerBps -= (1 - pLo) * drag

	takerBps := in.EdgeLowerBps - in.TakerFeeBps - in.HalfSpreadBps - in.TakerSlipWorstBps

	toUSDT := func(bps float64) float64 { return bps / 10000 * in.NotionalUSDT }
	bounds := []ActionBound{
		{Style: ExecNoOrder, LowerNetBps: 0, LowerNetUSDT: 0},
		{Style: ExecMaker, LowerNetBps: makerBps, LowerNetUSDT: toUSDT(makerBps)},
		{Style: ExecTaker, LowerNetBps: takerBps, LowerNetUSDT: toUSDT(takerBps)},
	}
	// riskRank 越小越保守;下界相同取更保守者。
	riskRank := map[ExecStyle]int{ExecNoOrder: 0, ExecMaker: 1, ExecTaker: 2}
	sort.SliceStable(bounds, func(i, j int) bool {
		if bounds[i].LowerNetBps != bounds[j].LowerNetBps {
			return bounds[i].LowerNetBps > bounds[j].LowerNetBps
		}
		return riskRank[bounds[i].Style] < riskRank[bounds[j].Style]
	})
	return bounds
}

// BestAction 返回条件净收益下界最大的动作(并列时偏保守)。
func BestAction(in ActionInputs) ActionBound {
	return CompareActions(in)[0]
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
