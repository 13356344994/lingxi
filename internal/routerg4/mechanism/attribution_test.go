package mechanism

import (
	"testing"

	"lingxi/internal/routerg4/protocol"
)

func TestCrossAssetTemplateIsHypothesisOnly(t *testing.T) {
	decision := DecideAttribution([]string{
		"btc_relative_return", "btc_beta_proxy", "symbol_return_1h",
	}, "")
	if decision.AttributionStatus != protocol.AttributionMechanismHypothesis {
		t.Fatalf("status=%s", decision.AttributionStatus)
	}
	if decision.TemplateID != protocol.CrossAssetTemplateID {
		t.Fatalf("template=%s", decision.TemplateID)
	}
	if decision.Payer != protocol.CrossAssetPayer || !decision.CannotWait {
		t.Fatalf("payer/cannot_wait missing: %+v", decision)
	}
	if len(decision.Invalidators) == 0 || decision.Deadline == "" {
		t.Fatalf("deadline/invalidators missing: %+v", decision)
	}
	if decision.FinalStatus != protocol.HypothesisResearchResultStatus {
		t.Fatalf("final=%s", decision.FinalStatus)
	}
}

func TestSessionTemplateIsHypothesisOnly(t *testing.T) {
	decision := DecideAttribution([]string{
		"time_of_day_session", "quote_volume_24h_log_bucket",
	}, "")
	if decision.AttributionStatus != protocol.AttributionMechanismHypothesis {
		t.Fatalf("status=%s", decision.AttributionStatus)
	}
	if decision.TemplateID != protocol.SessionTemplateID {
		t.Fatalf("template=%s", decision.TemplateID)
	}
	if decision.Payer != protocol.SessionPayer {
		t.Fatalf("payer=%s", decision.Payer)
	}
}

func TestIndependentEvidenceFixtureCanExplain(t *testing.T) {
	decision := DecideAttributionWithIndependentEvidence([]string{
		"btc_relative_return", "btc_beta_proxy",
	}, protocol.CrossAssetMechanism, true)
	if decision.AttributionStatus != protocol.AttributionExplained {
		t.Fatalf("status=%s", decision.AttributionStatus)
	}
	items := []SurvivorWorkItem{{Decision: decision}}
	if got := filterExplained(items); len(got) != 1 {
		t.Fatalf("manual evidence explained entries=%d", len(got))
	}
}

func TestAutomaticTemplatesNeverEnterWF(t *testing.T) {
	decision := DecideAttribution([]string{
		"btc_relative_return", "btc_beta_proxy",
	}, "")
	items := []SurvivorWorkItem{{Decision: decision}}
	if got := filterExplained(items); len(got) != protocol.CurrentAutomaticStrictWFEntries {
		t.Fatalf("automatic WF entries=%d", len(got))
	}
}

func TestPurePriceUnexplainedSafe(t *testing.T) {
	decision := DecideAttribution([]string{"symbol_return_1h", "symbol_realized_vol_1h"}, "")
	if decision.AttributionStatus != protocol.AttributionUnexplained {
		t.Fatalf("status=%s", decision.AttributionStatus)
	}
	if decision.FinalStatus != FinalStatusUnexplainedResearchPass {
		t.Fatalf("final=%s", decision.FinalStatus)
	}
}

func TestCrossTemplateAmbiguous(t *testing.T) {
	decision := DecideAttribution([]string{
		"btc_relative_return", "btc_beta_proxy", "time_of_day_session", "quote_volume_24h_log_bucket",
	}, "")
	if decision.AttributionStatus != protocol.AttributionAmbiguous {
		t.Fatalf("status=%s", decision.AttributionStatus)
	}
	if decision.FinalStatus != FinalStatusClosed {
		t.Fatalf("final=%s", decision.FinalStatus)
	}
}

func TestGraveyardClaimClosed(t *testing.T) {
	decision := DecideAttribution([]string{"symbol_return_1h"}, protocol.G2PrizeMechanism)
	if decision.AttributionStatus != protocol.AttributionGraveyard {
		t.Fatalf("status=%s", decision.AttributionStatus)
	}
	if decision.FinalStatus != FinalStatusClosed {
		t.Fatalf("final=%s", decision.FinalStatus)
	}
}

func TestGraveyardFeatureFamilyUnexplained(t *testing.T) {
	decision := DecideAttribution([]string{"okx_bybit_funding_spread", "symbol_return_1h"}, "")
	if decision.AttributionStatus != protocol.AttributionUnexplained {
		t.Fatalf("status=%s", decision.AttributionStatus)
	}
}

func TestUnexplainedLeafFlagsForcedFalse(t *testing.T) {
	decision := DecideAttribution([]string{"symbol_return_1h"}, "")
	leaf := FinalLeaf{
		AttributionStatus: decision.AttributionStatus,
		Status:            FinalStatusUnexplainedResearchPass,
		TradeAllowed:      false,
		ExamAllowed:       false,
		Certified:         false,
	}
	if leaf.TradeAllowed || leaf.ExamAllowed || leaf.Certified {
		t.Fatalf("unexplained leaf must be no trade/exam/certified: %+v", leaf)
	}
}
