package personalvalidation

import (
	"math"

	"lingxi/internal/routerg6/eventmap"
	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	"lingxi/internal/routerg10/exclusionmap"
	"lingxi/internal/routerg10/personalbrain"
	"lingxi/internal/routerg10/personalplaybook"
)

const (
	obsSpreadBPS           = 3.0
	obsFeeBPS              = 2.0
	obsMaxSlippageBPS      = 30.0
	obsMinRemainingEdgeBPS = 5.0
	obsTimeoutMS           = 3000
	obsMakerTimeoutBars    = 2
)

// observeExecution builds decision-time execution observations from bound proofs
// and bar/depth replay — never from playbook MakerQuadrant or outcome labels.
func observeExecution(
	bars []eventmap.Bar,
	i, delay int,
	direction string,
	conservative, stress float64,
	price float64,
	eventID string,
	pb personalplaybook.PersonalWildPlaybook,
	proofs ProofBundle,
	requireProofObservations bool,
) (personalbrain.ExecutionSimState, g10executionpolicy.TriggerRecord) {
	ep, hasEntry := proofs.entryProof(eventID)
	maxCap := frozenMaxPriceCap(price, direction, eventID, proofs)
	latencyMS := 50.0
	if hasEntry && ep.LatencyMS > 0 {
		latencyMS = ep.LatencyMS
	}
	remaining := conservative
	if remaining == 0 {
		remaining = math.Max(0, conservative)
	}
	sim := personalbrain.ExecutionSimState{
		ThesisStillValid:    stress >= 0,
		RemainingEdgeBPS:    remaining,
		SpreadBPS:           obsSpreadBPS,
		FeeBPS:              obsFeeBPS,
		LatencyMS:             latencyMS,
		MaxPriceCap:           maxCap,
		MaxSlippageBPS:        obsMaxSlippageBPS,
		MinRemainingEdgeBPS:   obsMinRemainingEdgeBPS,
		TimeoutMS:             obsTimeoutMS,
		MakerTimeoutBars:      obsMakerTimeoutBars,
	}
	if hasEntry {
		sim = observationsFromEntryProof(sim, ep)
	} else if requireProofObservations {
		// Formal validation: no OHLC heuristic fill without bound entry proof.
		sim.MakerFilled = false
		sim.MakerTouchedNotFilled = false
	} else {
		// Smoke/heuristic scan: never impersonate bound fills for tier gates.
		sim.MakerFilled = false
		sim.MakerTouchedNotFilled = false
	}
	tr := executionTriggerFromObservations(eventID, direction, price, sim, conservative, sim.MakerFilled)
	if fp, ok := proofs.executionPolicy(eventID); ok {
		tr = mergeExecutionPolicyObservations(tr, fp)
		if fp.MaxPriceCap > 0 {
			sim.MaxPriceCap = fp.MaxPriceCap
		}
		if fp.MaxSlippageBPS > 0 {
			sim.MaxSlippageBPS = fp.MaxSlippageBPS
		}
		if fp.MinRemainingEdgeBPS > 0 {
			sim.MinRemainingEdgeBPS = fp.MinRemainingEdgeBPS
		}
		if fp.TimeoutMS > 0 {
			sim.TimeoutMS = fp.TimeoutMS
		}
		if fp.SpreadBPS > 0 {
			sim.SpreadBPS = fp.SpreadBPS
		}
		if fp.FeeBPS > 0 {
			sim.FeeBPS = fp.FeeBPS
		}
		if fp.LatencyMS > 0 {
			sim.LatencyMS = fp.LatencyMS
		}
	}
	return sim, tr
}

func observationsFromEntryProof(sim personalbrain.ExecutionSimState, ep g10entryproof.EntryProof) personalbrain.ExecutionSimState {
	if ep.LatencyMS > 0 {
		sim.LatencyMS = ep.LatencyMS
	}
	if ep.PartialFill {
		sim.PartialFill = true
	}
	switch ep.EvidenceGrade {
	case g10entryproof.GradeRealFillOrWAL:
		if ep.CertificationOK {
			sim.MakerFilled = true
			sim.MakerTouchedNotFilled = false
		}
	case g10entryproof.GradeMakerAttemptAckCancel:
		sim.MakerFilled = ep.PartialFill
		sim.MakerTouchedNotFilled = !sim.MakerFilled && ep.CertificationOK
	case g10entryproof.GradeExecutableShadowOrTrade:
		sim.MakerFilled = false
		sim.MakerTouchedNotFilled = false
	default:
		sim.MakerFilled = false
		sim.MakerTouchedNotFilled = false
	}
	if sim.MakerTouchedNotFilled && !sim.MakerFilled {
		sim.SubsequentlyProfitable = false
	}
	return sim
}

func observeMakerPathHeuristic(bars []eventmap.Bar, i, delay int, direction string, conservative float64) (touched, filled, partial, subsequentlyProf bool) {
	entryIdx := i + 1 + delay
	if entryIdx >= len(bars) {
		return false, false, false, false
	}
	makerPrice := bars[entryIdx].Open
	if makerPrice <= 0 {
		return false, false, false, false
	}
	endWin := entryIdx + obsMakerTimeoutBars
	if endWin >= len(bars) {
		endWin = len(bars) - 1
	}
	dir := direction
	if dir == "" {
		dir = "long"
	}
	for b := entryIdx; b <= endWin; b++ {
		bar := bars[b]
		if dir == "short" {
			if bar.High >= makerPrice {
				touched = true
			}
			if touched && bar.Close <= makerPrice*0.999 {
				filled = true
			}
		} else {
			if bar.Low <= makerPrice {
				touched = true
			}
			if touched && bar.Close >= makerPrice*1.001 {
				filled = true
			}
		}
	}
	if touched && !filled {
		subsequentlyProf = conservative > obsSpreadBPS+obsFeeBPS
	}
	partial = filled && touched && subsequentlyProf
	return touched, filled, partial, subsequentlyProf
}

func mergeExecutionPolicyObservations(tr g10executionpolicy.TriggerRecord, fp g10executionpolicy.FrozenPolicy) g10executionpolicy.TriggerRecord {
	tr.ToxicFastFullFill = fp.ToxicFillStatus == g10executionpolicy.ToxicFillExitStatus ||
		fp.ToxicFillStatus == g10executionpolicy.ToxicFillRevalidateStatus
	if fp.Side != "" {
		tr.Side = fp.Side
	}
	if fp.Price > 0 {
		tr.Price = fp.Price
	}
	if fp.MaxPriceCap > 0 {
		tr.MaxPriceCap = fp.MaxPriceCap
	}
	if fp.MaxSlippageBPS > 0 {
		tr.MaxSlippageBPS = fp.MaxSlippageBPS
	}
	if fp.MinRemainingEdgeBPS > 0 {
		tr.MinRemainingEdgeBPS = fp.MinRemainingEdgeBPS
	}
	if fp.TimeoutMS > 0 {
		tr.TimeoutMS = fp.TimeoutMS
	}
	if fp.SpreadBPS > 0 {
		tr.SpreadBPS = fp.SpreadBPS
	}
	if fp.FeeBPS > 0 {
		tr.FeeBPS = fp.FeeBPS
	}
	if fp.LatencyMS > 0 {
		tr.LatencyMS = fp.LatencyMS
	}
	if fp.MakerGate.ForbidMaker {
		_ = fp.MakerGate
	}
	return tr
}

func executionTriggerFromObservations(
	eventID, direction string,
	price float64,
	sim personalbrain.ExecutionSimState,
	conservative float64,
	filled bool,
) g10executionpolicy.TriggerRecord {
	side := direction
	if side == "" {
		side = "long"
	}
	tr := g10executionpolicy.TriggerRecord{
		TriggerID:              eventID,
		Symbol:                 "",
		Side:                   side,
		Price:                  price,
		MakerTouched:           sim.MakerTouchedNotFilled,
		MakerFilled:            sim.MakerFilled,
		SubsequentlyProfitable: sim.SubsequentlyProfitable,
		PartialFill:            sim.PartialFill,
		ThroughPrice:           sim.ToxicThroughPrice,
		RemainingEdgeBPS:       sim.RemainingEdgeBPS,
		SpreadBPS:              sim.SpreadBPS,
		FeeBPS:                 sim.FeeBPS,
		LatencyMS:              sim.LatencyMS,
		MaxPriceCap:            sim.MaxPriceCap,
		MaxSlippageBPS:         sim.MaxSlippageBPS,
		MinRemainingEdgeBPS:    sim.MinRemainingEdgeBPS,
		TimeoutMS:              sim.TimeoutMS,
		ToxicFastFullFill:      sim.ToxicFastFullFill,
		ToxicThroughPrice:      sim.ToxicThroughPrice,
		ToxicBookSyncBad:       sim.ToxicBookSyncBad,
		ThesisStillValid:       sim.ThesisStillValid,
	}
	if filled {
		if conservative > 0 {
			tr.NetPnLUSDT = conservative
		} else {
			tr.NetPnLUSDT = -math.Abs(conservative)
			if tr.NetPnLUSDT == 0 {
				tr.NetPnLUSDT = -1
			}
		}
		if sim.LatencyMS > 0 {
			tr.FillWaitMS = int(sim.LatencyMS)
		}
	}
	return tr
}

func frozenMaxPriceCap(price float64, direction string, eventID string, proofs ProofBundle) float64 {
	if price <= 0 {
		return 0
	}
	if fp, ok := proofs.executionPolicy(eventID); ok && fp.MaxPriceCap > 0 {
		return fp.MaxPriceCap
	}
	slipFrac := obsMaxSlippageBPS / 10000
	if direction == "short" {
		return price * (1 - slipFrac)
	}
	return price * (1 + slipFrac)
}

func resolveExitProofOK(
	eventID string,
	proofs ProofBundle,
	requireBoundProofs bool,
) bool {
	if xp, ok := proofs.exitProof(eventID); ok {
		return xp.CertificationOK
	}
	if requireBoundProofs {
		return false
	}
	return false
}

func crowdingFromProofs(eventID string, conservative float64, pb personalplaybook.PersonalWildPlaybook, proofs ProofBundle) (level int, passed bool) {
	if cs, ok := proofs.crowdingStress(eventID); ok {
		passed = cs.Passed
		if !passed {
			for _, pt := range cs.Levels {
				if pt.QueueCrowdingDetected {
					level = pt.Competitors
				}
			}
			if level == 0 {
				level = 5
			}
			return level, false
		}
		return 1, true
	}
	for _, lvl := range CrowdingLevels {
		if EvaluateCrowdingAtLevel(conservative, lvl) {
			return lvl, false
		}
	}
	if pb.StopNarrative.CrowdingStopRule == "crowding_failed" {
		return 5, false
	}
	return 1, true
}

func enrichMatchedEvent(
	me personalbrain.MatchedPersonalEvent,
	pb personalplaybook.PersonalWildPlaybook,
	proofs ProofBundle,
	eventID string,
	exitOK bool,
	crowdingPassed bool,
	crowding exclusionmap.CrowdingStress,
	frozenAction string,
	requireBoundProofs bool,
) personalbrain.MatchedPersonalEvent {
	me.Proofs.ConstitutionHash = pb.ProofBinding.ConstitutionHash
	me.Proofs.ExclusionProofHash = pb.ProofBinding.ExclusionProofHash
	me.Proofs.InstitutionAxisHash = pb.ProofBinding.InstitutionAxisHash
	me.Proofs.RetailAxisHash = pb.ProofBinding.RetailAxisHash
	me.Proofs.BotAxisHash = pb.ProofBinding.BotAxisHash
	me.Proofs.EntryProofHash = pb.ProofBinding.EntryProofHash
	me.Proofs.ExecutionPolicyHash = pb.ProofBinding.ExecutionPolicyHash
	me.Proofs.ExitProofHash = pb.ProofBinding.ExitProofHash
	me.Proofs.AnnouncementTapeHash = pb.ProofBinding.AnnouncementTapeHash
	me.Proofs.ExclusionAccepted = false
	if ex, ok := proofs.exclusionProof(eventID); ok {
		me.Proofs.ExclusionAccepted = ValidateExclusionFromProof(pb.ProofBinding, ex) == nil
	}
	me.Proofs.ExitCertified = exitOK
	me.Proofs.CrowdingPassed = crowdingPassed
	me.Proofs.CrowdingStress = crowding
	me.Proofs.EntryCertified = false
	me.Proofs.EntryGrade = g10entryproof.GradePublicConservativeBoundary
	if ep, ok := proofs.entryProof(eventID); ok {
		me.Proofs.EntryGrade = ep.EvidenceGrade
		me.Proofs.EntryCertified = ep.CertificationOK && g10entryproof.CertificationAllowed(ep.EvidenceGrade)
		me.Proofs.DynamicMinNotional = ep.DynamicMinNotional
		me.Proofs.ExecutableDepthUSDT = ep.ExecutableDepth
	}
	if fp, ok := proofs.executionPolicy(eventID); ok {
		me.Proofs.MakerGateForbid = fp.MakerGate.ForbidMaker
	}
	_ = frozenAction
	return me
}
