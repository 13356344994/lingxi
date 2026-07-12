package sorter

import (
	"strings"

	"lingxi/internal/routerg2/protocol"
)

type DataTruthStatus string

const (
	DataTruthValid   DataTruthStatus = "valid"
	DataTruthInvalid DataTruthStatus = "invalid"
	DataTruthUnknown DataTruthStatus = "unknown"
)

type ExecutabilityStatus string

const (
	Executable   ExecutabilityStatus = "executable"
	Unexecutable ExecutabilityStatus = "unexecutable"
	ExecUnknown  ExecutabilityStatus = "unknown"
)

type GateStatus string

const (
	GatePass    GateStatus = "pass"
	GateFail    GateStatus = "fail"
	GateUnknown GateStatus = "unknown"
)

type CoarseReason string

const (
	ReasonEligible                  CoarseReason = "ELIGIBLE"
	ReasonOODRejected               CoarseReason = "OOD_REJECTED"
	ReasonAmbiguousRejected         CoarseReason = "AMBIGUOUS_REJECTED"
	ReasonUnexecutableRejected      CoarseReason = "UNEXECUTABLE_REJECTED"
	ReasonExecutabilityUnknown      CoarseReason = "EXECUTABILITY_UNKNOWN"
	ReasonDataTruthInvalid          CoarseReason = "DATA_TRUTH_INVALID"
	ReasonDataTruthUnknown          CoarseReason = "DATA_TRUTH_UNKNOWN"
	ReasonHistoryCoverageInsuff     CoarseReason = "HISTORY_COVERAGE_INSUFFICIENT"
	ReasonHistoryCoverageUnknown    CoarseReason = "HISTORY_COVERAGE_UNKNOWN"
	ReasonPreDecisionLiquidityInsuf CoarseReason = "PREDECISION_LIQUIDITY_INSUFFICIENT"
	ReasonPreDecisionLiquidityUnk   CoarseReason = "PREDECISION_LIQUIDITY_UNKNOWN"
	ReasonVolatilityCostRatioLow    CoarseReason = "VOLATILITY_COST_RATIO_LOW"
	ReasonVolatilityCostRatioUnk    CoarseReason = "VOLATILITY_COST_RATIO_UNKNOWN"
	ReasonSingleSymbolCapacityFull  CoarseReason = "SINGLE_SYMBOL_CAPACITY_EXCEEDED"
	ReasonSingleSymbolCapacityUnk   CoarseReason = "SINGLE_SYMBOL_CAPACITY_UNKNOWN"
	ReasonClusterCapacityFull       CoarseReason = "CORRELATED_CLUSTER_CAPACITY_EXCEEDED"
	ReasonClusterCapacityUnk        CoarseReason = "CORRELATED_CLUSTER_CAPACITY_UNKNOWN"
	ReasonRequiredFieldsMissing     CoarseReason = "MECHANISM_REQUIRED_FIELDS_MISSING"
)

type GlobalSignals struct {
	DataTruth     DataTruthStatus
	OOD           bool
	Ambiguous     bool
	Executability ExecutabilityStatus
	AvailableData map[string]bool
}

type LeafInput struct {
	LeafID                     string
	SorterDomain               string
	HistoryCoverage            GateStatus
	PreDecisionLiquidity       GateStatus
	VolatilityCostRatio        float64
	VolatilityCostRatioKnown   bool
	MinVolatilityCostRatio     float64
	SingleSymbolCapacity       GateStatus
	CorrelatedClusterCapacity  GateStatus
	MechanismRequiredDataNames []string
}

type Result struct {
	LeafID              string
	SorterDomain        string
	Eligible            bool
	CoarseReason        CoarseReason
	MissingRequiredData []string
}

func EvaluateLeaves(global GlobalSignals, leaves []LeafInput) []Result {
	out := make([]Result, 0, len(leaves))
	for _, leaf := range leaves {
		out = append(out, EvaluateLeaf(global, leaf))
	}
	return out
}

func EvaluateLeaf(global GlobalSignals, leaf LeafInput) Result {
	result := Result{
		LeafID:       strings.TrimSpace(leaf.LeafID),
		SorterDomain: normalizeDomain(leaf.SorterDomain),
	}
	if global.OOD {
		return rejected(result, ReasonOODRejected)
	}
	if global.Ambiguous {
		return rejected(result, ReasonAmbiguousRejected)
	}
	switch global.Executability {
	case Executable:
	case Unexecutable:
		return rejected(result, ReasonUnexecutableRejected)
	default:
		return rejected(result, ReasonExecutabilityUnknown)
	}
	switch global.DataTruth {
	case DataTruthValid:
	case DataTruthInvalid:
		return rejected(result, ReasonDataTruthInvalid)
	default:
		return rejected(result, ReasonDataTruthUnknown)
	}
	if reason, rejectedNow := gateReason(
		leaf.HistoryCoverage,
		ReasonHistoryCoverageInsuff,
		ReasonHistoryCoverageUnknown,
	); rejectedNow {
		return rejected(result, reason)
	}
	if reason, rejectedNow := gateReason(
		leaf.PreDecisionLiquidity,
		ReasonPreDecisionLiquidityInsuf,
		ReasonPreDecisionLiquidityUnk,
	); rejectedNow {
		return rejected(result, reason)
	}
	if !leaf.VolatilityCostRatioKnown ||
		(leaf.MinVolatilityCostRatio != 0 && leaf.MinVolatilityCostRatio != protocol.SorterMinVolatilityCostRatio) {
		return rejected(result, ReasonVolatilityCostRatioUnk)
	}
	if leaf.VolatilityCostRatio < protocol.SorterMinVolatilityCostRatio {
		return rejected(result, ReasonVolatilityCostRatioLow)
	}
	if reason, rejectedNow := gateReason(
		leaf.SingleSymbolCapacity,
		ReasonSingleSymbolCapacityFull,
		ReasonSingleSymbolCapacityUnk,
	); rejectedNow {
		return rejected(result, reason)
	}
	if reason, rejectedNow := gateReason(
		leaf.CorrelatedClusterCapacity,
		ReasonClusterCapacityFull,
		ReasonClusterCapacityUnk,
	); rejectedNow {
		return rejected(result, reason)
	}
	missing := missingRequired(leaf.MechanismRequiredDataNames, global.AvailableData)
	if len(missing) > 0 {
		result.MissingRequiredData = missing
		return rejected(result, ReasonRequiredFieldsMissing)
	}
	result.Eligible = true
	result.CoarseReason = ReasonEligible
	return result
}

func missingRequired(required []string, available map[string]bool) []string {
	normalized := make(map[string]bool, len(available))
	for name, ok := range available {
		normalized[normalizeDataName(name)] = ok
	}
	missing := make([]string, 0, len(required))
	for _, name := range required {
		key := normalizeDataName(name)
		if key == "" {
			continue
		}
		if !normalized[key] {
			missing = append(missing, key)
		}
	}
	return missing
}

func rejected(result Result, reason CoarseReason) Result {
	result.Eligible = false
	result.CoarseReason = reason
	return result
}

func gateReason(status GateStatus, failReason, unknownReason CoarseReason) (CoarseReason, bool) {
	switch status {
	case GatePass:
		return "", false
	case GateFail:
		return failReason, true
	default:
		return unknownReason, true
	}
}

func normalizeDomain(domain string) string {
	normalized := strings.ToLower(strings.TrimSpace(domain))
	if normalized == "" {
		return "unknown"
	}
	return normalized
}

func normalizeDataName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
