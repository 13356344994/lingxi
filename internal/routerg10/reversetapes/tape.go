package reversetapes

import (
	"errors"
	"fmt"

	"lingxi/internal/datatruth"
)

func hashTape(tape ExecutableProfitTape) (string, error) {
	payload := tape
	payload.TapeHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateTape checks sealed tape integrity.
func ValidateTape(tape ExecutableProfitTape) error {
	if tape.SchemaVersion != SchemaVersionTape {
		return fmt.Errorf("reversetapes: schema drift")
	}
	if tape.ProofBinding.ExclusionProofHash == "" ||
		tape.ProofBinding.EntryProofHash == "" ||
		tape.ProofBinding.ExecutionPolicyHash == "" ||
		tape.ProofBinding.ExitProofHash == "" {
		return errors.New("reversetapes: proof binding incomplete")
	}
	if !tape.OutcomeLabel.LabelOnly {
		return errors.New("reversetapes: outcome must be label_only")
	}
	want, err := hashTape(tape)
	if err != nil {
		return err
	}
	if tape.TapeHash == "" || tape.TapeHash != want {
		return errors.New("reversetapes: tape hash mismatch")
	}
	if tape.Status == TapeStatusExecutableCertified {
		if tape.AbsoluteProfitUSDT < 0 && tape.RealizedFill {
			// allowed — losing fill
		}
		if !tape.RealizedFill && tape.AbsoluteProfitUSDT != 0 {
			return errors.New("reversetapes: unfilled profit must be zero")
		}
	}
	return nil
}

// HashTapes canonicalizes tape list hash.
func HashTapes(tapes []ExecutableProfitTape) (string, error) {
	payload := make([]ExecutableProfitTape, len(tapes))
	for i, t := range tapes {
		payload[i] = t
		payload[i].TapeHash = ""
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}
