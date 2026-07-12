package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// LoadPolicyBundle verifies frozen policy, ledger, and manifest hashes.
func LoadPolicyBundle(dir string) (PolicyBundle, error) {
	manifestRaw, err := os.ReadFile(filepath.Join(dir, "policy_manifest.json"))
	if err != nil {
		return PolicyBundle{}, err
	}
	var manifest reverselearn.PolicyManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return PolicyBundle{}, err
	}
	ledgerRaw, err := os.ReadFile(filepath.Join(dir, "g5_policy_ledger.json"))
	if err != nil {
		return PolicyBundle{}, err
	}
	var ledger reverselearn.ExperimentLedger
	if err := json.Unmarshal(ledgerRaw, &ledger); err != nil {
		return PolicyBundle{}, err
	}
	if reverselearn.IsNoFrozenPolicy(manifest) {
		if err := verifyNoFrozenPolicyBundle(ledger, manifest); err != nil {
			return PolicyBundle{}, err
		}
		return PolicyBundle{
			Dir:            dir,
			Ledger:         ledger,
			PolicyManifest: manifest,
		}, nil
	}
	policyRaw, err := os.ReadFile(filepath.Join(dir, "g5_policy.json"))
	if err != nil {
		return PolicyBundle{}, err
	}
	var policy reverselearn.FrozenPolicy
	if err := json.Unmarshal(policyRaw, &policy); err != nil {
		return PolicyBundle{}, err
	}
	if err := VerifyPolicyBundle(policy, ledger, manifest); err != nil {
		return PolicyBundle{}, err
	}
	return PolicyBundle{
		Dir:            dir,
		Policy:         policy,
		Ledger:         ledger,
		PolicyManifest: manifest,
	}, nil
}

// VerifyPolicyBundle checks protocol/hash chain and exam_actions=0.
func VerifyPolicyBundle(policy reverselearn.FrozenPolicy, ledger reverselearn.ExperimentLedger, manifest reverselearn.PolicyManifest) error {
	if policy.ProtocolHash != g5protocol.RouterProtocolHash ||
		ledger.ProtocolHash != g5protocol.RouterProtocolHash ||
		manifest.ProtocolHash != g5protocol.RouterProtocolHash {
		return errors.New("policy: protocol hash mismatch")
	}
	if policy.ExamActions != g5protocol.ExamActions ||
		ledger.LedgerStage != reverselearn.LedgerStageDiscovery ||
		manifest.ExamActions != g5protocol.ExamActions {
		return errors.New("policy: exam_actions must be 0")
	}
	wantPolicyHash, err := reverselearn.HashPolicy(policy)
	if err != nil {
		return err
	}
	if policy.PolicyHash == "" || policy.PolicyHash != wantPolicyHash {
		return errors.New("policy: policy hash mismatch")
	}
	if err := reverselearn.ValidateLedger(ledger); err != nil {
		return err
	}
	if policy.ExperimentHash != ledger.LedgerHash {
		return errors.New("policy: experiment hash mismatch")
	}
	if manifest.FeatureHash != policy.FeatureHash ||
		manifest.PolicyHash != policy.PolicyHash ||
		manifest.ExperimentHash != policy.ExperimentHash {
		return errors.New("policy: manifest hash chain mismatch")
	}
	if policy.FeatureHash == "" || policy.PolicyHash == "" || ledger.LedgerHash == "" {
		return errors.New("policy: hash chain incomplete")
	}
	return nil
}

func verifyNoFrozenPolicyBundle(ledger reverselearn.ExperimentLedger, manifest reverselearn.PolicyManifest) error {
	if manifest.Status != reverselearn.StatusNoFrozenPolicy {
		return errors.New("policy: manifest status mismatch")
	}
	if manifest.ProtocolHash != g5protocol.RouterProtocolHash ||
		ledger.ProtocolHash != g5protocol.RouterProtocolHash {
		return errors.New("policy: protocol hash mismatch")
	}
	if manifest.ExamActions != g5protocol.ExamActions ||
		ledger.LedgerStage != reverselearn.LedgerStageDiscovery {
		return errors.New("policy: exam_actions must be 0")
	}
	if manifest.PolicyHash != "" {
		return errors.New("policy: no-frozen manifest must have empty policy hash")
	}
	if err := reverselearn.ValidateLedger(ledger); err != nil {
		return err
	}
	if manifest.ExperimentHash != ledger.LedgerHash {
		return errors.New("policy: experiment hash mismatch")
	}
	if manifest.FeatureHash == "" || ledger.LedgerHash == "" {
		return errors.New("policy: hash chain incomplete")
	}
	return nil
}

// RejectDiscoveryLabels ensures validation never reads discovery training labels.
func RejectDiscoveryLabels(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return fmt.Errorf("policy: discovery labels forbidden in validation: %s", path)
}
