package personalvalidation

import (
	"context"
	"fmt"
	"math"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg10/executionpolicy"
	"lingxi/internal/routerg10/exclusionmap"
	"lingxi/internal/routerg10/personalbrain"
	"lingxi/internal/routerg10/personalplaybook"
)

// ScanConfig bounds one symbol validation stream for one personal playbook.
type ScanConfig struct {
	Context        context.Context
	Spec           eventmap.ProfileSpec
	ProfileID      string
	Boundaries     eventmap.Boundaries
	Thresholds     eventmap.FrozenThresholds
	BTCBars        []eventmap.Bar
	ShockClusterAt func(time.Time) string
	Funding        []profitmap.FundingPoint
	MaxLines       int
	Proofs         ProofBundle
	Partition      string // eventmap.PartitionValidation or PartitionWalkForward
	RequireProofObservations bool
}

// ScanPlaybookSymbol evaluates validation-partition triggers for one frozen personal playbook.
// All matching triggers are included — unfilled/missed carry NetPnLUSDT=0.
func ScanPlaybookSymbol(
	bars []eventmap.Bar,
	symbol string,
	pb personalplaybook.PersonalWildPlaybook,
	cfg ScanConfig,
	tracker *eventmap.WindowTracker,
) ([]TriggerEvent, error) {
	if len(bars) < 3 || tracker == nil {
		return nil, nil
	}
	horizon, ok := horizonSpec(cfg.Spec, pb.Horizon)
	if !ok {
		return nil, fmt.Errorf("personalvalidation: unknown horizon %q", pb.Horizon)
	}
	windowBars := rollingWindowBars(cfg.Spec)
	primaryBack := primaryReturnBars(cfg.Spec)
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	var events []TriggerEvent
	for i := primaryBack * 2; i < len(bars)-1; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if profitmap.RejectCurrentBarOpen(bars, i, cfg.Spec.BarDuration) {
			continue
		}
		decision := decisionTimeAt(bars, i, cfg.Spec.BarDuration)
		partition := cfg.Partition
		if partition == "" {
			partition = eventmap.PartitionValidation
		}
		if cfg.Boundaries.PartitionAt(decision) != partition {
			continue
		}
		entryIndex := i + 1
		if entryIndex >= len(bars) || !bars[entryIndex].OpenTime.Equal(decision) {
			continue
		}
		cluster := "none"
		if cfg.ShockClusterAt != nil {
			cluster = cfg.ShockClusterAt(decision)
			if cluster == "" {
				cluster = "none"
			}
		}
		feat := BuildDecisionVisibleFeatures(DecisionFeatureInput{
			Bars: bars, BarIndex: i, Spec: cfg.Spec, Boundaries: cfg.Boundaries,
			Thresholds: cfg.Thresholds, BTCBars: cfg.BTCBars, WindowBars: windowBars,
			PrimaryBack: primaryBack, Decision: decision, Symbol: symbol, ProfileID: cfg.ProfileID,
			Announcements: cfg.Proofs.Announcements, ShockCluster: cluster,
			CapitalUSDT: CapitalTiersUSDT[0], MinNotionalUSDT: pb.EntryNarrative.DynamicMinNotionalUSDT,
			WantEventFamily: pb.EventFamily,
		})
		matched := personalbrain.MatchPersonalPlaybooks([]personalplaybook.PersonalWildPlaybook{pb}, feat, i)
		if len(matched) == 0 {
			continue
		}
		if !tracker.AllowCard(symbol, pb.EventFamily, i) {
			continue
		}
		ev := buildTriggerEvent(matched[0], pb, feat, bars, i, horizon, cluster, decision, cfg)
		tracker.MarkCard(symbol, pb.EventFamily, i, eventmap.CooldownBarsForEvent(cfg.Spec, pb.Horizon, pb.DelayBars))
		events = append(events, ev)
	}
	return events, nil
}

func buildTriggerEvent(
	me personalbrain.MatchedPersonalEvent,
	pb personalplaybook.PersonalWildPlaybook,
	feat personalbrain.BarFeatures,
	bars []eventmap.Bar,
	i int,
	horizon profitmap.HorizonSpec,
	cluster string,
	decision time.Time,
	cfg ScanConfig,
) TriggerEvent {
	delay := pb.DelayBars
	if delay < 0 {
		delay = 0
	}
	exitIndex := i + 1 + delay + horizon.BarCount
	_ = exitIndex
	eventID := CanonicalEventID(feat.Symbol, decision, pb.EventFamily)
	me.EventID = eventID
	conservative, stress, capFail, cliff := estimateOutcome(bars, i, horizon, me.Direction, delay, pb)
	sim, tr := observeExecution(bars, i, delay, me.Direction, conservative, stress, feat.Price, eventID, pb, cfg.Proofs, cfg.RequireProofObservations)
	exitOK := resolveExitProofOK(eventID, bars, i, delay, horizon.BarCount, pb, cfg.Proofs)
	crowdingLevel, crowdingPass := crowdingFromProofs(eventID, conservative, pb, cfg.Proofs)
	var crowdingStress exclusionmap.CrowdingStress
	if cs, ok := cfg.Proofs.crowdingStress(eventID); ok {
		crowdingStress = cs
	}

	me = enrichMatchedEvent(me, pb, cfg.Proofs, eventID, exitOK, crowdingPass, crowdingStress, pb.ExecutionNarrative.FrozenExecutionAction)

	barInput := personalbrain.BarInput{
		BarIndex: i, DecisionTime: decision,
		Features: personalbrain.BarFeatures{
			Symbol: feat.Symbol, ProfileID: cfg.ProfileID,
			ReturnBPS: feat.ReturnBPS, PriorReturnBPS: feat.PriorReturnBPS,
			BTCRelativeBPS: feat.BTCRelativeBPS, BarRangeBPS: feat.BarRangeBPS, Price: feat.Price,
		},
		Prices:       map[string]float64{feat.Symbol: feat.Price},
		ExecutionSim: sim,
		Events:       []personalbrain.MatchedPersonalEvent{me},
	}
	exec := personalbrain.CompareExecutionActions(me, barInput)
	frozenAction := exec.SelectedAction
	if !actionAllowedForReplay(exec) {
		frozenAction = executionpolicy.ActionSkip
	}
	barInput.Events = []personalbrain.MatchedPersonalEvent{me}

	cfgBrain := personalbrain.Config{
		InitialEquityUSDT: CapitalTiersUSDT[0],
		ConstitutionHash:  pb.ProofBinding.ConstitutionHash,
		MinNotionalUSDT:   pb.EntryNarrative.DynamicMinNotionalUSDT,
		ProtocolHash:      ProtocolHash,
		TradeAllowed:      true,
	}
	brain := personalbrain.NewBrain(cfgBrain)
	out, _ := brain.DecideBar(barInput)

	quadrant := executionpolicy.ClassifyMakerQuadrant(tr)
	netPnL := executionpolicy.MissedProfitablePnL(tr)
	enterGate := out.EnterGate.Status
	fillReason := out.FillOrMissReason
	pitPub, pitFS := evaluatePITFlags(cfg.Proofs.Announcements, decision)
	entryGrade := me.Proofs.EntryGrade
	_, entryBound := cfg.Proofs.entryProof(eventID)
	_, execBound := cfg.Proofs.executionPolicy(eventID, pb.ProofBinding.ExecutionPolicyHash)
	_, exitBound := cfg.Proofs.exitProof(eventID)
	_, exclBound := cfg.Proofs.exclusionProof(eventID)
	return TriggerEvent{
		EventID:            eventID,
		PlaybookHash:       pb.PlaybookHash,
		Symbol:             feat.Symbol,
		ProfileID:          cfg.ProfileID,
		Direction:          me.Direction,
		EventFamily:        pb.EventFamily,
		PersonalTapeFamily: pb.PersonalTapeFamily,
		DecisionTime:       decision,
		DecisionBarIndex:   i,
		Day:                decision.UTC().Format("2006-01-02"),
		ShockCluster:       cluster,
		HorizonBarCount:    horizon.BarCount,
		DelayBars:          delay,
		EnterGate:          enterGate,
		FillOrMissReason:   fillReason,
		FrozenAction:       frozenAction,
		MakerQuadrant:      quadrant,
		NetPnLUSDT:         netPnL,
		ConservativeBPS:    conservative,
		StressBPS:          stress,
		CapacityFail:       capFail,
		CapacityCliffUSDT:  cliff,
		ReferenceNotional:  pb.RiskNarrative.MaxSizeUSDT,
		MakerFilled:        sim.MakerFilled,
		MakerTouchedNotFilled: sim.MakerTouchedNotFilled,
		SubsequentlyProfitable: sim.SubsequentlyProfitable,
		PartialFill:        sim.PartialFill,
		ThroughPrice:       sim.ToxicThroughPrice,
		FastFill:           sim.MakerFilled && sim.LatencyMS <= float64(executionpolicy.FastFillThresholdMS),
		ToxicRevalidate:    sim.ToxicFastFullFill || sim.ToxicBookSyncBad,
		ToxicExit:          sim.ToxicThroughPrice && !sim.ThesisStillValid,
		ExitProofOK:        exitOK,
		PITPublishedContaminated: pitPub,
		PITFirstSeenContaminated: pitFS,
		CrowdingLevel:      crowdingLevel,
		CrowdingPassed:     crowdingPass,
		CapitalExpansionStop: capitalExpansionWouldStop(pb, pb.RiskNarrative.MaxSizeUSDT),
		ReturnBPS:          feat.ReturnBPS,
		PriorReturnBPS:     feat.PriorReturnBPS,
		NoTrade:            enterGate == personalbrain.EnterCannotEnter || frozenAction == executionpolicy.ActionSkip,
		BrainAction:        mapFrozenAction(frozenAction, enterGate, me.Direction),
		EntryGrade:         entryGrade,
		EntryProofBound:    entryBound,
		ExecutionProofBound: execBound,
		ExitProofBound:     exitBound,
		ExclusionProofBound: exclBound,
	}
}

func actionAllowedForReplay(exec personalbrain.ExecutionDecision) bool {
	for _, a := range exec.ActionsCompared {
		if a.Action == exec.SelectedAction {
			return a.Allowed
		}
	}
	return false
}

func mapFrozenAction(action, enterGate, direction string) string {
	if enterGate == personalbrain.EnterMissedCorrectly {
		return "NO_TRADE"
	}
	switch action {
	case executionpolicy.ActionBoundedMaker, executionpolicy.ActionConditionalMarketableLimit:
		if direction == "short" {
			return "SHORT"
		}
		return "LONG"
	case executionpolicy.ActionSkip:
		return "NO_TRADE"
	default:
		return "NO_TRADE"
	}
}
func estimateOutcome(bars []eventmap.Bar, i int, horizon profitmap.HorizonSpec, direction string, delay int, pb personalplaybook.PersonalWildPlaybook) (conservative, stress float64, capFail bool, cliff float64) {
	entry := i + 1 + delay
	exit := entry + horizon.BarCount
	if entry >= len(bars) || exit >= len(bars) {
		return 0, 0, true, 0
	}
	entryPx := bars[entry].Open
	exitPx := bars[exit].Close
	if entryPx <= 0 {
		return 0, 0, true, 0
	}
	grossBPS := (exitPx - entryPx) / entryPx * 10000
	if direction == "short" {
		grossBPS = -grossBPS
	}
	costBPS := 8.0
	conservative = grossBPS - costBPS
	stress = grossBPS - costBPS*CostStressMultiplier
	cliff = pb.RiskNarrative.MaxCapacityUSDT
	capFail = cliff <= 0 || pb.RiskNarrative.MaxSizeUSDT <= 0
	return conservative, stress, capFail, cliff
}

func capitalExpansionWouldStop(pb personalplaybook.PersonalWildPlaybook, tier float64) bool {
	if pb.StopNarrative.CapitalExpansionStopRule == "exceeds_certified_tier" && tier > 200 {
		return true
	}
	return false
}

func horizonSpec(spec eventmap.ProfileSpec, label string) (profitmap.HorizonSpec, bool) {
	for _, h := range spec.Horizons {
		if h.Label == label {
			return h, true
		}
	}
	return profitmap.HorizonSpec{}, false
}

func rollingWindowBars(spec eventmap.ProfileSpec) int {
	return profitmap.RollingWindowBars(spec)
}

func primaryReturnBars(spec eventmap.ProfileSpec) int {
	if spec.BarDuration <= time.Hour {
		return 1
	}
	return 1
}

func decisionTimeAt(bars []eventmap.Bar, idx int, barDuration time.Duration) time.Time {
	return profitmap.DecisionTime(bars, idx, barDuration)
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func sign(v float64) int {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}

func netReturnFraction(initial, final float64) float64 {
	if initial <= 0 {
		return 0
	}
	return (final - initial) / initial
}

func maxDayContribution(dayPositive map[string]float64) float64 {
	total := 0.0
	maxDay := 0.0
	for _, pnl := range dayPositive {
		total += pnl
	}
	if total <= 0 {
		return 0
	}
	for _, pnl := range dayPositive {
		share := pnl / total
		if share > maxDay {
			maxDay = share
		}
	}
	return maxDay
}

func shockBlockSigns(shockPnL map[string]float64) (nonzero, positive int) {
	for _, pnl := range shockPnL {
		if math.Abs(pnl) < 1e-9 {
			continue
		}
		nonzero++
		if pnl > 0 {
			positive++
		}
	}
	return nonzero, positive
}
