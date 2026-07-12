package sorter

import (
	"testing"

	"lingxi/internal/routerg2/protocol"
)

func passingLeaf(id, domain string) LeafInput {
	return LeafInput{
		LeafID:                     id,
		SorterDomain:               domain,
		HistoryCoverage:            GatePass,
		PreDecisionLiquidity:       GatePass,
		VolatilityCostRatio:        3.0,
		VolatilityCostRatioKnown:   true,
		MinVolatilityCostRatio:     protocol.SorterMinVolatilityCostRatio,
		SingleSymbolCapacity:       GatePass,
		CorrelatedClusterCapacity:  GatePass,
		MechanismRequiredDataNames: []string{"ohlcv", "funding"},
	}
}

func passingGlobal() GlobalSignals {
	return GlobalSignals{
		DataTruth:     DataTruthValid,
		OOD:           false,
		Ambiguous:     false,
		Executability: Executable,
		AvailableData: map[string]bool{
			"ohlcv":   true,
			"funding": true,
			"depth":   false,
		},
	}
}

func TestLeafIsolationDepthMissingOnlyBlocksDepthLeaf(t *testing.T) {
	global := passingGlobal()
	leaves := []LeafInput{
		passingLeaf("ohlcv_funding_leaf", "carry"),
		passingLeaf("needs_depth_leaf", "carry"),
	}
	leaves[1].MechanismRequiredDataNames = []string{"ohlcv", "funding", "depth"}

	results := EvaluateLeaves(global, leaves)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	if !results[0].Eligible || results[0].CoarseReason != ReasonEligible {
		t.Fatalf("leaf without depth requirement should pass, got %+v", results[0])
	}
	if results[0].SorterDomain != "carry" {
		t.Fatalf("sorter domain should be stable, got %q", results[0].SorterDomain)
	}

	if results[1].Eligible || results[1].CoarseReason != ReasonRequiredFieldsMissing {
		t.Fatalf("depth-required leaf should fail on missing required fields, got %+v", results[1])
	}
	if len(results[1].MissingRequiredData) != 1 || results[1].MissingRequiredData[0] != "depth" {
		t.Fatalf("missing required fields = %v, want [depth]", results[1].MissingRequiredData)
	}
}

func TestFailClosedGlobalRejects(t *testing.T) {
	baseLeaf := passingLeaf("leaf", "momentum")
	cases := []struct {
		name   string
		global GlobalSignals
		reason CoarseReason
	}{
		{
			name: "ood",
			global: GlobalSignals{
				DataTruth:     DataTruthValid,
				OOD:           true,
				Ambiguous:     false,
				Executability: Executable,
				AvailableData: map[string]bool{"ohlcv": true, "funding": true},
			},
			reason: ReasonOODRejected,
		},
		{
			name: "ambiguous",
			global: GlobalSignals{
				DataTruth:     DataTruthValid,
				OOD:           false,
				Ambiguous:     true,
				Executability: Executable,
				AvailableData: map[string]bool{"ohlcv": true, "funding": true},
			},
			reason: ReasonAmbiguousRejected,
		},
		{
			name: "unexecutable",
			global: GlobalSignals{
				DataTruth:     DataTruthValid,
				OOD:           false,
				Ambiguous:     false,
				Executability: Unexecutable,
				AvailableData: map[string]bool{"ohlcv": true, "funding": true},
			},
			reason: ReasonUnexecutableRejected,
		},
		{
			name: "executability unknown",
			global: GlobalSignals{
				DataTruth:     DataTruthValid,
				OOD:           false,
				Ambiguous:     false,
				Executability: ExecUnknown,
				AvailableData: map[string]bool{"ohlcv": true, "funding": true},
			},
			reason: ReasonExecutabilityUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateLeaf(tc.global, baseLeaf)
			if got.Eligible {
				t.Fatalf("expected fail-closed reject, got %+v", got)
			}
			if got.CoarseReason != tc.reason {
				t.Fatalf("reason = %q, want %q", got.CoarseReason, tc.reason)
			}
			if got.SorterDomain != "momentum" {
				t.Fatalf("sorter domain changed unexpectedly: %q", got.SorterDomain)
			}
		})
	}
}

func TestFailClosedPerLeafGateUnknowns(t *testing.T) {
	baseGlobal := passingGlobal()
	baseLeaf := passingLeaf("leaf", "carry")

	cases := []struct {
		name      string
		globalMut func(*GlobalSignals)
		leafMut   func(*LeafInput)
		reason    CoarseReason
	}{
		{
			name: "data truth unknown",
			globalMut: func(g *GlobalSignals) {
				g.DataTruth = DataTruthUnknown
			},
			leafMut: nil,
			reason:  ReasonDataTruthUnknown,
		},
		{
			name:      "history coverage unknown",
			globalMut: nil,
			leafMut: func(l *LeafInput) {
				l.HistoryCoverage = GateUnknown
			},
			reason: ReasonHistoryCoverageUnknown,
		},
		{
			name:      "pre-decision liquidity unknown",
			globalMut: nil,
			leafMut: func(l *LeafInput) {
				l.PreDecisionLiquidity = GateUnknown
			},
			reason: ReasonPreDecisionLiquidityUnk,
		},
		{
			name:      "volatility-cost ratio unknown",
			globalMut: nil,
			leafMut: func(l *LeafInput) {
				l.VolatilityCostRatioKnown = false
			},
			reason: ReasonVolatilityCostRatioUnk,
		},
		{
			name:      "single-symbol capacity unknown",
			globalMut: nil,
			leafMut: func(l *LeafInput) {
				l.SingleSymbolCapacity = GateUnknown
			},
			reason: ReasonSingleSymbolCapacityUnk,
		},
		{
			name:      "cluster capacity unknown",
			globalMut: nil,
			leafMut: func(l *LeafInput) {
				l.CorrelatedClusterCapacity = GateUnknown
			},
			reason: ReasonClusterCapacityUnk,
		},
		{
			name: "required data missing",
			globalMut: func(g *GlobalSignals) {
				g.AvailableData = map[string]bool{"ohlcv": true}
			},
			leafMut: nil,
			reason:  ReasonRequiredFieldsMissing,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			global := baseGlobal
			leaf := baseLeaf
			if tc.globalMut != nil {
				tc.globalMut(&global)
			}
			if tc.leafMut != nil {
				tc.leafMut(&leaf)
			}
			got := EvaluateLeaf(global, leaf)
			if got.Eligible {
				t.Fatalf("expected fail-closed reject, got %+v", got)
			}
			if got.CoarseReason != tc.reason {
				t.Fatalf("reason = %q, want %q", got.CoarseReason, tc.reason)
			}
		})
	}
}

func TestGateOrderStableReason(t *testing.T) {
	global := passingGlobal()
	global.DataTruth = DataTruthInvalid
	leaf := passingLeaf("leaf", "carry")
	leaf.HistoryCoverage = GateFail

	got := EvaluateLeaf(global, leaf)
	if got.Eligible {
		t.Fatalf("expected reject, got %+v", got)
	}
	if got.CoarseReason != ReasonDataTruthInvalid {
		t.Fatalf("reason = %q, want %q", got.CoarseReason, ReasonDataTruthInvalid)
	}
}
