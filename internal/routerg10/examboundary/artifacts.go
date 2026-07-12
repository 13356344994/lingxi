package examboundary

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/walkforward"
)

var (
	ErrArtifactHashMismatch = errors.New("artifact hash mismatch")
	ErrArtifactBinding      = errors.New("artifact binding mismatch")
)

// VerifyBoundaryInput binds public artifacts for boundary verification.
type VerifyBoundaryInput struct {
	WFReportPath        string
	MechanismReportPath string
	StageReceiptPath    string
	CommitmentsPath     string
	StatePath           string
}

func validateResearchArtifacts(wf walkforward.WFReport, mech walkforward.MechanismReport, receipt walkforward.StageReceipt, receiptOK bool) error {
	if err := walkforward.ValidateWFReport(wf); err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactHashMismatch, err)
	}
	if err := walkforward.ValidateMechanismReport(mech); err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactHashMismatch, err)
	}
	if wf.Generation != protocol.Generation || wf.ProtocolHash != protocol.RouterProtocolHash {
		return fmt.Errorf("%w: wf envelope drift", ErrArtifactBinding)
	}
	if mech.Generation != protocol.Generation || mech.ProtocolHash != protocol.RouterProtocolHash {
		return fmt.Errorf("%w: mechanism envelope drift", ErrArtifactBinding)
	}
	if mech.WFReportHash != wf.ReportHash {
		return fmt.Errorf("%w: mechanism wf_report_hash", ErrArtifactBinding)
	}
	if receiptOK {
		if err := walkforward.ValidateStageReceipt(receipt); err != nil {
			return fmt.Errorf("%w: %v", ErrArtifactHashMismatch, err)
		}
		if receipt.WFReportHash != wf.ReportHash || receipt.MechanismReportHash != mech.ReportHash {
			return fmt.Errorf("%w: stage receipt report hashes", ErrArtifactBinding)
		}
		if receipt.ProtocolHash != protocol.RouterProtocolHash || receipt.Generation != protocol.Generation {
			return fmt.Errorf("%w: stage receipt envelope", ErrArtifactBinding)
		}
	}
	return nil
}

func loadStageReceipt(path string) (walkforward.StageReceipt, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return walkforward.StageReceipt{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return walkforward.StageReceipt{}, false, nil
		}
		return walkforward.StageReceipt{}, false, err
	}
	if err := AssertNoSchemaLeak(raw); err != nil {
		return walkforward.StageReceipt{}, false, err
	}
	var receipt walkforward.StageReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return walkforward.StageReceipt{}, false, fmt.Errorf("stage receipt decode: %w", err)
	}
	return receipt, true, nil
}

func loadWFReportFull(path string) (walkforward.WFReport, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return walkforward.WFReport{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return walkforward.WFReport{}, false, nil
		}
		return walkforward.WFReport{}, false, err
	}
	if err := AssertNoSchemaLeak(raw); err != nil {
		return walkforward.WFReport{}, false, err
	}
	var wf walkforward.WFReport
	if err := json.Unmarshal(raw, &wf); err != nil {
		return walkforward.WFReport{}, false, fmt.Errorf("wf report decode: %w", err)
	}
	return wf, true, nil
}

func loadMechanismReportFull(path string) (walkforward.MechanismReport, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return walkforward.MechanismReport{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return walkforward.MechanismReport{}, false, nil
		}
		return walkforward.MechanismReport{}, false, err
	}
	if err := AssertNoSchemaLeak(raw); err != nil {
		return walkforward.MechanismReport{}, false, err
	}
	var mech walkforward.MechanismReport
	if err := json.Unmarshal(raw, &mech); err != nil {
		return walkforward.MechanismReport{}, false, fmt.Errorf("mechanism report decode: %w", err)
	}
	return mech, true, nil
}
