package episodic

import (
	"fmt"
	"math"
)

// ResidualModel is an optional <=32-parameter adjustment layer.
// Default RESIDUAL_DISABLED; enable only when independent WF reports gain.
type ResidualModel struct {
	StatusFlag string    `json:"status"`
	WFGain     bool      `json:"wf_gain"`
	Weights    []float64 `json:"weights,omitempty"`
	Bias       float64   `json:"bias,omitempty"`
}

// DefaultResidual returns the frozen disabled residual model.
func DefaultResidual() ResidualModel {
	return ResidualModel{StatusFlag: ResidualDefaultStatus}
}

// Enabled reports whether residual adjustments are active.
func (r ResidualModel) Enabled() bool {
	return r.StatusFlag != ResidualDefaultStatus && r.WFGain && len(r.Weights) > 0
}

// Status returns the residual model status string.
func (r ResidualModel) Status() string {
	if r.StatusFlag == "" {
		return ResidualDefaultStatus
	}
	return r.StatusFlag
}

// Validate enforces the frozen parameter cap.
func (r ResidualModel) Validate() error {
	paramCount := len(r.Weights)
	if r.Bias != 0 {
		paramCount++
	}
	if paramCount > ResidualMaxParameters {
		return fmt.Errorf("residual model exceeds %d parameters: got %d", ResidualMaxParameters, paramCount)
	}
	if r.StatusFlag != ResidualDefaultStatus && !r.WFGain {
		return fmt.Errorf("residual enable requires wf gain")
	}
	return nil
}

// AdjustSizeScale applies a bounded residual nudge to memory size scale.
func (r ResidualModel) AdjustSizeScale(base float64, hits []RetrievalHit) float64 {
	if !r.Enabled() || base <= 0 {
		return base
	}
	var simSum, bpsSum float64
	for _, h := range hits {
		simSum += h.Similarity
		bpsSum += h.OutcomeBPS * h.Similarity
	}
	if simSum <= 0 {
		return base
	}
	features := []float64{bpsSum / simSum, simSum / float64(len(hits)), float64(len(hits))}
	var pred float64
	for i := range features {
		if i < len(r.Weights) {
			pred += r.Weights[i] * features[i]
		}
	}
	pred += r.Bias
	// Confidence can only shrink size.
	scale := base * sigmoid(pred)
	if scale > base {
		return base
	}
	return math.Max(0, scale)
}

func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}
