package app

import (
	"math"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/decision"
	"lingxi/internal/execution"
	"lingxi/internal/mechanism"
	"lingxi/internal/money"
)

const firstBattleShadowFile = "firstbattle_mechanism_shadow.jsonl"

type firstBattleExpertVerdict struct {
	Expert       string           `json:"expert"`
	Accepted     bool             `json:"accepted"`
	RejectReason string           `json:"reject_reason,omitempty"`
	Thesis       mechanism.Thesis `json:"thesis,omitempty"`
}

type firstBattleShadowRecord struct {
	RecordedAt time.Time                    `json:"recorded_at"`
	DecisionID string                       `json:"decision_id"`
	Card       mechanism.MechanismEventCard `json:"card"`
	OOD        decision.OODVerdict          `json:"ood"`
	Verdicts   []firstBattleExpertVerdict   `json:"verdicts"`
	Legacy     fastExecShadowEvent          `json:"legacy_event"`
}

// firstBattleRuntime 是新主线的最小 wiring 壳；旧巨型引擎只负责把事件交给它，
// 专家注册和 OOD 规则不再散落在 fast 各策略文件。
type firstBattleRuntime struct {
	experts []mechanism.Expert
	ood     decision.OODConfig
}

func newFirstBattleRuntime() *firstBattleRuntime {
	return &firstBattleRuntime{
		experts: []mechanism.Expert{
			mechanism.PanicLiquidationExpert{},
			mechanism.FundingSettlementUnwindExpert{},
			mechanism.TieredLiquidationExpert{},
			mechanism.RuleEventExpert{},
		},
		ood: decision.DefaultOODConfig(),
	}
}

func (r *firstBattleRuntime) evaluate(card mechanism.MechanismEventCard) (decision.OODVerdict, []firstBattleExpertVerdict) {
	if r == nil {
		r = newFirstBattleRuntime()
	}
	ood := decision.AssessOOD(card, r.ood)
	verdicts := make([]firstBattleExpertVerdict, 0, len(r.experts))
	for _, expert := range r.experts {
		result := expert.Evaluate(card)
		verdicts = append(verdicts, firstBattleExpertVerdict{
			Expert: expert.Name(), Accepted: result.Accepted,
			RejectReason: result.RejectReason, Thesis: result.Thesis,
		})
	}
	return ood, verdicts
}

func recordFastExecutableShadow(e *OKXDirectionalEngine, ev *fastExecShadowEvent) error {
	if e == nil || ev == nil {
		return nil
	}
	if strings.TrimSpace(ev.SnapshotID) == "" {
		ev.SnapshotID = execution.NewDecisionID(e.nowFn().UTC())
	}
	if err := appendJSONLRecord(filepath.Join(e.recordDir, fastExecShadowFile), *ev); err != nil {
		return err
	}
	recordFirstBattleShadow(e, ev)
	return nil
}

func recordFirstBattleShadow(e *OKXDirectionalEngine, ev *fastExecShadowEvent) {
	card := mechanismCardFromLegacyEvent(e, ev)
	if strings.TrimSpace(card.ID) == "" {
		card.ID = execution.NewDecisionID(e.nowFn().UTC())
	}
	runtime := e.firstBattle
	if runtime == nil {
		runtime = newFirstBattleRuntime()
	}
	ood, verdicts := runtime.evaluate(card)
	_ = appendJSONLRecord(filepath.Join(e.recordDir, firstBattleShadowFile), firstBattleShadowRecord{
		RecordedAt: e.nowFn().UTC(), DecisionID: card.ID,
		Card: card, OOD: ood, Verdicts: verdicts, Legacy: *ev,
	})
}

func mechanismCardFromLegacyEvent(e *OKXDirectionalEngine, ev *fastExecShadowEvent) mechanism.MechanismEventCard {
	now := ev.RecordedAt
	if now.IsZero() && e != nil {
		now = e.nowFn().UTC()
	}
	card := mechanism.MechanismEventCard{
		ID: ev.SnapshotID, Venue: ev.Venue, Instrument: ev.Symbol,
		EventTimeUnixMs: now.UnixMilli(), ReceiveTimeUnixMs: now.UnixMilli(),
		Regime:       mechanism.Regime{Kind: mechanism.RegimeUnknown, DeadlineUnixMs: now.Add(15 * time.Minute).UnixMilli()},
		Transmission: mechanism.Transmission{Cluster: "unknown"},
		Environment: mechanism.Environment{
			Volatility: math.Abs(ev.MidImpulse60sBps),
			TrendSign:  signOf(ev.MidImpulse60sBps),
		},
		Executability: mechanism.Executability{
			SpreadBps: ev.SpreadBps, TopDepthNotional: money.FromUSDT(ev.DepthNotional10bps),
			ExpectedSlippageBps: math.Max(0, ev.SpreadBps/2),
			MinNotional:         money.FromUSDT(5), LotNotional: money.FromUSDT(5),
			MakerFeasible: ev.MakerPlaceable, TakerFeasible: ev.SpreadBps > 0 && ev.SpreadBps <= 12,
		},
		Quality: mechanism.Quality{
			DataAgeMs: ev.EventAgeMs, ObservationConfidence: 0.25,
		},
		Signals: map[string]float64{
			"market_liquidations_5m":       ev.MarketLiq5m,
			"cross_venue_liquidations_30s": float64(ev.XliqN30s),
		},
	}
	if e != nil {
		card.Account.SolvencyEquity = money.FromUSDT(e.fastEquityBaseUSDT())
		card.Account.FreeMargin = card.Account.SolvencyEquity
	}
	source := strings.TrimSpace(ev.CandidateSource)
	switch source {
	case "key_casc_fade", "liq_follow":
		card.Regime.Kind = mechanism.RegimePanicLiquidation
		card.Regime.Descriptor = "legacy forced-long liquidation observation; shadow only"
		card.ForcedFlow.Payer = mechanism.PayerLongLiquidations
		card.ForcedFlow.Side = mechanism.DirectionShort
	case "key_sqz_fade":
		card.Regime.Kind = mechanism.RegimePanicLiquidation
		card.Regime.Descriptor = "legacy forced-short liquidation observation; shadow only"
		card.ForcedFlow.Payer = mechanism.PayerShortLiquidations
		card.ForcedFlow.Side = mechanism.DirectionLong
	}
	observed := math.Abs(ev.LiqQtySum * ev.Mid)
	card.ForcedFlow.ObservedNotional = money.FromUSDT(observed)
	card.ForcedFlow.Decaying = math.Abs(ev.MidImpulse15sBps) < math.Abs(ev.MidImpulse60sBps)
	if observed > 0 {
		remainingFrac := math.Min(1, math.Abs(ev.MidImpulse15sBps)/(math.Abs(ev.MidImpulse60sBps)+1e-9))
		card.ForcedFlow.RemainingNotional = money.FromUSDT(observed * remainingFrac)
	}
	imbalance := math.Max(-1, math.Min(1, ev.OrderbookImbalance))
	if card.ForcedFlow.Side == mechanism.DirectionShort {
		card.Transmission.AbsorptionRatio = (imbalance + 1) / 2
	} else if card.ForcedFlow.Side == mechanism.DirectionLong {
		card.Transmission.AbsorptionRatio = (1 - imbalance) / 2
	}
	if source != "" {
		card.Transmission.Cluster = source
	}
	if card.Regime.Kind != mechanism.RegimeUnknown && observed > 0 && ev.EventAgeMs >= 0 {
		card.Quality.ObservationConfidence = 0.5
	}
	return card
}

func signOf(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}
