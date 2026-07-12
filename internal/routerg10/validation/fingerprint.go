package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/residualmap"
)

// FingerprintBinding binds a verified personal execution fingerprint artifact.
type FingerprintBinding struct {
	Fingerprint fingerprint.PersonalExecutionFingerprint
	Path        string
	Hash        string
}

// LoadFingerprintBinding reads and verifies one personal execution fingerprint.
func LoadFingerprintBinding(path string) (FingerprintBinding, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return FingerprintBinding{}, fmt.Errorf("validation: read fingerprint %q: %w", path, err)
	}
	var fp fingerprint.PersonalExecutionFingerprint
	if err := json.Unmarshal(raw, &fp); err != nil {
		return FingerprintBinding{}, fmt.Errorf("validation: decode fingerprint: %w", err)
	}
	if err := fingerprint.ValidateFingerprintSchema(fp); err != nil {
		return FingerprintBinding{}, err
	}
	return FingerprintBinding{
		Fingerprint: fp,
		Path:        path,
		Hash:        fp.FingerprintHash,
	}, nil
}

// VerifyFingerprintBinding rejects missing or drifted fingerprint hashes.
func VerifyFingerprintBinding(binding FingerprintBinding) error {
	if binding.Hash == "" || binding.Fingerprint.FingerprintHash == "" {
		return errors.New("validation: fingerprint hash required")
	}
	if binding.Hash != binding.Fingerprint.FingerprintHash {
		return errors.New("validation: fingerprint hash mismatch")
	}
	return nil
}

// InstrumentConstraints carries fingerprint-derived sizing floors for replay.
type InstrumentConstraints struct {
	MinNotionalUSDT float64
	LotNotionalUSDT float64
	ContractValue   float64
	Unknown         bool
}

// LookupInstrumentConstraints resolves min-notional/lot/contract value for one symbol.
func LookupInstrumentConstraints(fp fingerprint.PersonalExecutionFingerprint, symbol string) InstrumentConstraints {
	sym := normalizeValidationSymbol(symbol)
	var best *fingerprint.ExecutionCell
	for i := range fp.Cells {
		c := &fp.Cells[i]
		if normalizeValidationSymbol(c.Symbol) != sym {
			continue
		}
		if best == nil || c.SampleCount > best.SampleCount {
			best = c
		}
	}
	if best == nil || best.Instrument.Status == fingerprint.StatusUnknown {
		return InstrumentConstraints{Unknown: true}
	}
	minNotional, okMin := parsePositiveFloat(best.Instrument.MinNotional)
	lotSize, okLot := parsePositiveFloat(best.Instrument.LotSize)
	contractValue, okContract := parsePositiveFloat(best.Instrument.ContractValue)
	if !okMin || !okLot || !okContract {
		return InstrumentConstraints{Unknown: true}
	}
	lotNotional := lotSize * contractValue
	if lotNotional <= 0 {
		return InstrumentConstraints{Unknown: true}
	}
	return InstrumentConstraints{
		MinNotionalUSDT: minNotional,
		LotNotionalUSDT: lotNotional,
		ContractValue:   contractValue,
	}
}

// LookupPersonalAdjustments resolves personal fee/slip/latency deltas for one event.
func LookupPersonalAdjustments(fp fingerprint.PersonalExecutionFingerprint, symbol, direction string) residualmap.PersonalAdjustments {
	return residualmap.LookupPersonalAdjustments(fp, symbol, direction)
}

// ActualEquityUSDT returns fingerprint median cash when available.
func ActualEquityUSDT(fp fingerprint.PersonalExecutionFingerprint) float64 {
	return residualmap.ActualEquityUSDT(fp)
}

func normalizeValidationSymbol(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	return strings.TrimSuffix(s, "-SWAP")
}

func parsePositiveFloat(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, false
	}
	return v, true
}
