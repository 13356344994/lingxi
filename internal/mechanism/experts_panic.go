package mechanism

// 恐慌强平接力 vs 竭尽判定阈值（骨架级可调常量）。
const (
	// panicExhaustionAbsorption 竭尽反转所需的最低对手盘吸收比例：吸收足够高才敢逆被迫流。
	panicExhaustionAbsorption = 0.60
	// panicExhaustionRemainingFrac 竭尽反转所需的「剩余被迫流 / 已观测被迫流」上限：
	// 剩余不足两成才认为被迫抛压接近枯竭。
	panicExhaustionRemainingFrac = 0.20
)

// PanicLiquidationExpert 处理系统性恐慌中的被迫卖出，严格区分：
//   - 接力延续（relay）：被迫流仍充沛、对手盘吸收不足 → 顺被迫流方向交易；
//   - 竭尽反转（exhaustion）：被迫流在衰减、剩余极少、对手盘大量吸收 → 逆被迫流方向交易。
//
// 它绝不因「跌得多」本身给方向：方向永远由被迫流状态与吸收结构决定，且必须带运行时证伪器。
type PanicLiquidationExpert struct{}

// Name 实现 Expert。
func (PanicLiquidationExpert) Name() string { return "panic_liquidation" }

// Evaluate 实现 Expert。
func (e PanicLiquidationExpert) Evaluate(card MechanismEventCard) Evaluation {
	if card.Regime.Kind != RegimePanicLiquidation {
		return reject(RejectRegimeMismatch)
	}
	if card.Quality.OutOfDistribution {
		return reject(RejectOutOfDistribution)
	}
	if !validRatio(card.Transmission.AbsorptionRatio) {
		return reject(RejectMissingEvidence)
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

	exhausted := ff.Decaying && ff.ObservedNotional > 0 &&
		card.Transmission.AbsorptionRatio >= panicExhaustionAbsorption &&
		int64(ff.RemainingNotional) <= int64(ff.ObservedNotional.MulRate(panicExhaustionRemainingFrac))

	var (
		dir          Direction
		forcedAction string
		exitThesis   string
		invalidators []Invalidator
	)
	if exhausted {
		// 竭尽反转：最后一批被迫卖盘被吸收，逆势承接。
		dir = ff.Side.Opposite()
		forcedAction = "forced_sellers_exhausted_absorption_high"
		exitThesis = "reprice_complete_or_new_forced_flow_reappears"
		invalidators = []Invalidator{
			{Metric: "remaining_forced_notional", Condition: "remaining_forced_notional rises again above exhaustion band", Observable: true},
			{Metric: "absorption_ratio", Condition: "absorption_ratio collapses below exhaustion threshold", Observable: true},
		}
	} else {
		// 接力延续：被迫抛压仍在释放，顺势。
		dir = ff.Side
		forcedAction = "forced_sellers_still_unwinding"
		exitThesis = "forced_flow_drains_or_absorption_turns_high"
		invalidators = []Invalidator{
			{Metric: "remaining_forced_notional", Condition: "remaining_forced_notional drains toward zero", Observable: true},
			{Metric: "absorption_ratio", Condition: "absorption_ratio exceeds exhaustion threshold", Observable: true},
		}
	}

	return accept(Thesis{
		Expert:           e.Name(),
		CardID:           card.ID,
		Payer:            ff.Payer,
		ForcedAction:     forcedAction,
		Direction:        dir,
		DeadlineUnixMs:   card.Regime.DeadlineUnixMs,
		Evidence:         []string{"panic_liquidation_regime", forcedAction},
		Invalidators:     invalidators,
		Extremeness:      clampUnit(card.Transmission.AbsorptionRatio),
		HeavenConfidence: baseObservationConfidence(card),
		Capacity:         capacity,
		Cluster:          card.Transmission.Cluster,
		ExitThesis:       exitThesis,
	})
}
