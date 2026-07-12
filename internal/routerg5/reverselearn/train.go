package reverselearn

import (
	"errors"
	"fmt"
	"sort"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg5/features"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// TrainingOutcome is the sealed discovery training result.
type TrainingOutcome struct {
	Policy              FrozenPolicy
	Ledger              ExperimentLedger
	Report              TrainingReport
	AllAttemptsRejected bool
}

// RunTraining selects lambda on discovery holdout and seals frozen policy outputs.
func RunTraining(examples []TrainingExample, profileID string, featureHash string) (TrainingOutcome, error) {
	if len(examples) == 0 {
		return TrainingOutcome{}, errors.New("reverselearn: no joined training examples")
	}
	schema, err := features.FrozenFeatureSchema()
	if err != nil {
		return TrainingOutcome{}, err
	}
	if schema.FeatureHash != featureHash {
		return TrainingOutcome{}, fmt.Errorf("reverselearn: feature hash mismatch")
	}
	featureKeys := append([]string(nil), schema.Keys...)
	horizons := SortedHorizons(examples)
	actionSpec := FrozenActionEncoding(horizons)
	train, holdout := SplitChronologicalHoldout(examples)
	norm := FitNormalization(featureKeys, train)

	ledger := ExperimentLedger{
		SchemaVersion: SchemaVersionLedger,
		Generation:    g5protocol.Generation,
		ProtocolHash:    g5protocol.RouterProtocolHash,
		FeatureHash:     featureHash,
		LedgerStage:     LedgerStageDiscovery,
	}
	var bestAttempt *ExperimentAttempt
	for i, lambda := range g5protocol.LambdaGrid {
		if i >= g5protocol.MaxModelAttempts {
			break
		}
		weights := TrainRidgeSGD(train, featureKeys, actionSpec, horizons, norm, lambda, g5protocol.TrainingSeed)
		if IsDegeneratePolicyCandidate(weights, featureKeys, actionSpec, norm, horizons) {
			attempt := ExperimentAttempt{
				AttemptOrdinal: i + 1,
				Lambda:         lambda,
				Seed:           g5protocol.TrainingSeed,
				Epochs:         g5protocol.SGDEpochs,
				Status:         "rejected_fixed_action",
			}
			ledger.Attempts = append(ledger.Attempts, attempt)
			continue
		}
		regret, dirAcc, rewardMean := EvaluateMetrics(holdout, weights, featureKeys, actionSpec, horizons, norm)
		policy := FrozenPolicy{
			SchemaVersion:  SchemaVersionPolicy,
			Generation:     g5protocol.Generation,
			ProtocolHash:   g5protocol.RouterProtocolHash,
			FeatureHash:    featureHash,
			ModelFamily:    g5protocol.ModelFamily,
			ProfileID:      profileID,
			Lambda:         lambda,
			Seed:           g5protocol.TrainingSeed,
			Epochs:         g5protocol.SGDEpochs,
			FeatureKeys:    featureKeys,
			ActionEncoding: actionSpec,
			Weights:        weights,
			Normalization:  norm,
			HorizonLabels:  horizons,
			ExamActions:    g5protocol.ExamActions,
		}
		policy.PolicyHash, err = HashPolicy(policy)
		if err != nil {
			return TrainingOutcome{}, err
		}
		attempt := ExperimentAttempt{
			AttemptOrdinal:     i + 1,
			Lambda:             lambda,
			Seed:               g5protocol.TrainingSeed,
			Epochs:             g5protocol.SGDEpochs,
			TopActionRegretBPS: regret,
			DirectionAccuracy:  dirAcc,
			RewardMeanBPS:      rewardMean,
			TrainRows:          len(train),
			HoldoutRows:        len(holdout),
			PolicyHash:         policy.PolicyHash,
			Status:             "evaluated",
		}
		ledger.Attempts = append(ledger.Attempts, attempt)
		if bestAttempt == nil || regret < bestAttempt.TopActionRegretBPS {
			cp := attempt
			bestAttempt = &cp
		}
	}
	if bestAttempt == nil {
		ledger, err = SealLedger(ledger)
		if err != nil {
			return TrainingOutcome{}, err
		}
		report := TrainingReport{
			SchemaVersion:          SchemaVersionReport,
			Generation:             g5protocol.Generation,
			ProtocolHash:           g5protocol.RouterProtocolHash,
			FeatureHash:            featureHash,
			Status:                 StatusTrainingRejectedDegenerate,
			PolicyHash:             "",
			ExperimentHash:           ledger.LedgerHash,
			ProfileID:              profileID,
			TrainRows:              len(train),
			HoldoutRows:            len(holdout),
			JoinedRows:             len(examples),
			AttemptCount:           len(ledger.Attempts),
			FixedDirectionRejected: true,
			ExamActions:            g5protocol.ExamActions,
		}
		return TrainingOutcome{
			Ledger:              ledger,
			Report:              report,
			AllAttemptsRejected: true,
		}, nil
	}
	ledger, err = SealLedger(ledger)
	if err != nil {
		return TrainingOutcome{}, err
	}
	finalWeights := TrainRidgeSGD(train, featureKeys, actionSpec, horizons, norm, bestAttempt.Lambda, g5protocol.TrainingSeed)
	policy := FrozenPolicy{
		SchemaVersion:  SchemaVersionPolicy,
		Generation:     g5protocol.Generation,
		ProtocolHash:   g5protocol.RouterProtocolHash,
		FeatureHash:    featureHash,
		ExperimentHash: ledger.LedgerHash,
		ModelFamily:    g5protocol.ModelFamily,
		ProfileID:      profileID,
		Lambda:         bestAttempt.Lambda,
		Seed:           g5protocol.TrainingSeed,
		Epochs:         g5protocol.SGDEpochs,
		FeatureKeys:    featureKeys,
		ActionEncoding: actionSpec,
		Weights:        finalWeights,
		Normalization:  norm,
		HorizonLabels:  horizons,
		ExamActions:    g5protocol.ExamActions,
	}
	policy.PolicyHash, err = HashPolicy(policy)
	if err != nil {
		return TrainingOutcome{}, err
	}
	report := TrainingReport{
		SchemaVersion:      SchemaVersionReport,
		Generation:         g5protocol.Generation,
		ProtocolHash:       g5protocol.RouterProtocolHash,
		FeatureHash:        featureHash,
		PolicyHash:         policy.PolicyHash,
		ExperimentHash:     ledger.LedgerHash,
		ProfileID:          profileID,
		SelectedLambda:     bestAttempt.Lambda,
		TopActionRegretBPS: bestAttempt.TopActionRegretBPS,
		DirectionAccuracy:  bestAttempt.DirectionAccuracy,
		RewardMeanBPS:      bestAttempt.RewardMeanBPS,
		TrainRows:          len(train),
		HoldoutRows:        len(holdout),
		JoinedRows:         len(examples),
		AttemptCount:       len(ledger.Attempts),
		ExamActions:        g5protocol.ExamActions,
	}
	return TrainingOutcome{Policy: policy, Ledger: ledger, Report: report}, nil
}

// HashPolicy hashes sealed policy weights and schema.
func HashPolicy(policy FrozenPolicy) (string, error) {
	payload := policy
	payload.PolicyHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// SealLedger stamps append-only experiment ledger hash.
func SealLedger(ledger ExperimentLedger) (ExperimentLedger, error) {
	sort.Slice(ledger.Attempts, func(i, j int) bool {
		return ledger.Attempts[i].AttemptOrdinal < ledger.Attempts[j].AttemptOrdinal
	})
	hash, err := HashLedgerAttempts(ledger.Attempts)
	if err != nil {
		return ExperimentLedger{}, err
	}
	ledger.LedgerHash = hash
	return ledger, nil
}

// HashLedgerAttempts hashes ordered experiment attempts.
func HashLedgerAttempts(attempts []ExperimentAttempt) (string, error) {
	hash, _, err := datatruth.HashJSON(attempts)
	return hash, err
}

// ValidateLedger checks append-only invariants.
func ValidateLedger(ledger ExperimentLedger) error {
	if ledger.SchemaVersion != SchemaVersionLedger {
		return errors.New("reverselearn: ledger schema mismatch")
	}
	for i, attempt := range ledger.Attempts {
		if attempt.AttemptOrdinal != i+1 {
			return errors.New("reverselearn: attempt ordinals must be contiguous")
		}
	}
	want, err := HashLedgerAttempts(ledger.Attempts)
	if err != nil {
		return err
	}
	if ledger.LedgerHash == "" || ledger.LedgerHash != want {
		return errors.New("reverselearn: ledger hash mismatch")
	}
	return nil
}
