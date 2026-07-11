package mechanism

// 逐档强平判定信号与阈值（骨架级可调常量）。
const (
	// signalTiersRemaining 是本机制读取的命名信号：当前价位之外仍处于「已武装」状态的强平档数。
	signalTiersRemaining = "tiers_remaining"
	// tieredExhaustionAbsorption 最后一档竭尽反转所需的最低对手盘吸收比例。
	tieredExhaustionAbsorption = 0.55
)

// TieredLiquidationExpert 处理逐档强平梯队，按「还剩几档已武装」做接力 / 竭尽判定：
//   - tiers_remaining >= 1：仍有下游强平档待触发 → 接力延续，顺被迫流；
//   - tiers_remaining == 0（最后一档）且吸收充分：竭尽反转，逆被迫流。
//
// 与恐慌专家的区别：这里的判据是明确的强平档计数，而非广义抛压衰减。
type TieredLiquidationExpert struct{}

// Name 实现 Expert。
func (TieredLiquidationExpert) Name() string { return "tiered_liquidation" }

// Evaluate 实现 Expert。
func (e TieredLiquidationExpert) Evaluate(card MechanismEventCard) Evaluation {
	if card.Regime.Kind != RegimeTieredLiquidation {
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

	tiersRemaining, ok := finiteSignal(card, signalTiersRemaining)
	if !ok || tiersRemaining < 0 {
		return reject(RejectMissingEvidence)
	}
	lastTier := tiersRemaining < 1
	exhausted := lastTier && card.Transmission.AbsorptionRatio >= tieredExhaustionAbsorption

	var (
		dir          Direction
		forcedAction string
		exitThesis   string
		invalidators []Invalidator
	)
	if exhausted {
		dir = ff.Side.Opposite()
		forcedAction = "last_liquidation_tier_absorbed"
		exitThesis = "cascade_complete_or_new_tier_arms"
		invalidators = []Invalidator{
			{Metric: signalTiersRemaining, Condition: "a new liquidation tier arms below", Observable: true},
			{Metric: "absorption_ratio", Condition: "absorption collapses at last tier", Observable: true},
		}
	} else {
		dir = ff.Side
		forcedAction = "liquidation_cascade_relaying_through_tiers"
		exitThesis = "tiers_exhausted_or_absorption_turns_high"
		invalidators = []Invalidator{
			{Metric: signalTiersRemaining, Condition: "armed tiers drain to zero", Observable: true},
			{Metric: "absorption_ratio", Condition: "absorption exceeds exhaustion threshold", Observable: true},
		}
	}

	return accept(Thesis{
		Expert:           e.Name(),
		CardID:           card.ID,
		Payer:            ff.Payer,
		ForcedAction:     forcedAction,
		Direction:        dir,
		DeadlineUnixMs:   card.Regime.DeadlineUnixMs,
		Evidence:         []string{"tiered_liquidation_regime", forcedAction},
		Invalidators:     invalidators,
		Extremeness:      clampUnit(card.Transmission.AbsorptionRatio),
		HeavenConfidence: baseObservationConfidence(card),
		Capacity:         capacity,
		Cluster:          card.Transmission.Cluster,
		ExitThesis:       exitThesis,
	})
}
