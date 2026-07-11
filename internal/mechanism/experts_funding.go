package mechanism

import "math"

// 资金费结算卸仓判定阈值（骨架级可调常量）。
const (
	// fundingMinAbsRate 触发结算卸仓所需的最小资金费绝对值：费率过小则收款方无强动机了结。
	fundingMinAbsRate = 0.0005
	// signalFundingRate / signalUnwindWindowActive 是本机制读取的命名信号。
	signalFundingRate        = "funding_rate"
	signalUnwindWindowActive = "unwind_window_active"
)

// FundingSettlementUnwindExpert 处理资金费结算后的收款方卸仓：
// 当资金费长期极端、一侧持续收款时，结算兑现后收款方了结拥挤头寸，形成与其持仓相反的被迫流。
// 收款方是被迫行动方（收完即走），方向 = 其平仓施加的价格压力方向（= 被迫流 Side）。
//
// 正资费双腿 carry 只是低相关基准，不在本专家出方向；本专家只捕捉结算后的单边卸仓。
type FundingSettlementUnwindExpert struct{}

// Name 实现 Expert。
func (FundingSettlementUnwindExpert) Name() string { return "funding_settlement_unwind" }

// Evaluate 实现 Expert。
func (e FundingSettlementUnwindExpert) Evaluate(card MechanismEventCard) Evaluation {
	if card.Regime.Kind != RegimeFundingSettlement {
		return reject(RejectRegimeMismatch)
	}
	if card.Quality.OutOfDistribution {
		return reject(RejectOutOfDistribution)
	}
	ff := card.ForcedFlow
	if ff.Payer == PayerUnknown || !ff.Side.IsDirectional() {
		return reject(RejectNoPayer)
	}
	// 卸仓窗口必须处于激活态，否则没有运行时期限可言。
	window, windowOK := finiteSignal(card, signalUnwindWindowActive)
	if !windowOK {
		return reject(RejectMissingEvidence)
	}
	if window <= 0 || card.Regime.DeadlineUnixMs <= 0 {
		return reject(RejectNoDeadline)
	}
	rawRate, rateOK := finiteSignal(card, signalFundingRate)
	if !rateOK {
		return reject(RejectMissingEvidence)
	}
	rate := math.Abs(rawRate)
	if rate < fundingMinAbsRate {
		// 费率不够极端 → 没有足够被迫动机，淘汰（不是方向，是缺被迫性）。
		return reject(RejectNoCapacity)
	}
	if ok, reason := executable(card); !ok {
		return reject(reason)
	}
	capacity := mechanismCapacity(card)
	if capacity <= 0 {
		return reject(RejectNoCapacity)
	}

	return accept(Thesis{
		Expert:         e.Name(),
		CardID:         card.ID,
		Payer:          ff.Payer,
		ForcedAction:   "funding_receivers_unwind_crowded_leg_after_settlement",
		Direction:      ff.Side,
		DeadlineUnixMs: card.Regime.DeadlineUnixMs,
		Evidence:       []string{"funding_settlement_regime", "extreme_one_sided_funding"},
		Invalidators: []Invalidator{
			{Metric: "unwind_window_active", Condition: "unwind window closes before flow released", Observable: true},
			{Metric: "funding_rate", Condition: "funding rate normalizes / flips sign", Observable: true},
			{Metric: "remaining_forced_notional", Condition: "unwind flow fully absorbed", Observable: true},
		},
		Extremeness:      clampUnit(rate / (fundingMinAbsRate * 20)),
		HeavenConfidence: baseObservationConfidence(card),
		Capacity:         capacity,
		Cluster:          card.Transmission.Cluster,
		ExitThesis:       "crowded_leg_flat_or_funding_normalized",
	})
}
