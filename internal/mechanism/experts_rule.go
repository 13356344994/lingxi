package mechanism

// RuleEventExpert 处理规则事件（下架、保证金档位调整、指数纳入 / 剔除等）造成的被迫流。
// 规则事件的共同结构：存在一个明确的规则生效时刻（= 制度期限），在此之前被迫方必须行动
// （下架前被迫平仓、指数调整前跟踪资金被迫调仓）。方向 = 被迫流施加的价格压力方向。
//
// 这是首批里最骨架化的专家：它只把规则事件的被迫结构翻译成论点，不做接力 / 竭尽的细分判定。
type RuleEventExpert struct{}

// Name 实现 Expert。
func (RuleEventExpert) Name() string { return "rule_event" }

// Evaluate 实现 Expert。
func (e RuleEventExpert) Evaluate(card MechanismEventCard) Evaluation {
	if card.Regime.Kind != RegimeRuleEvent {
		return reject(RejectRegimeMismatch)
	}
	if card.Quality.OutOfDistribution {
		return reject(RejectOutOfDistribution)
	}
	// 规则事件必须带明确生效时刻，否则「规则」无期限、不成其为被迫流。
	if card.Regime.DeadlineUnixMs <= 0 {
		return reject(RejectNoDeadline)
	}
	ff := card.ForcedFlow
	if ff.Payer == PayerUnknown || !ff.Side.IsDirectional() {
		return reject(RejectNoPayer)
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
		ForcedAction:   "rule_deadline_forces_position_adjustment",
		Direction:      ff.Side,
		DeadlineUnixMs: card.Regime.DeadlineUnixMs,
		Evidence:       []string{"rule_event_regime", card.Regime.Descriptor},
		Invalidators: []Invalidator{
			{Metric: "rule_deadline", Condition: "rule postponed / cancelled (deadline changes)", Observable: true},
			{Metric: "remaining_forced_notional", Condition: "forced adjustment flow fully absorbed", Observable: true},
		},
		Extremeness:      clampUnit(card.Transmission.AbsorptionRatio),
		HeavenConfidence: baseObservationConfidence(card),
		Capacity:         capacity,
		Cluster:          card.Transmission.Cluster,
		ExitThesis:       "forced_adjustment_complete_or_rule_cancelled",
	})
}
