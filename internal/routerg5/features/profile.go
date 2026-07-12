package features

import (
	"time"

	"lingxi/internal/routerg4/profitmap"
	g5protocol "lingxi/internal/routerg5/protocol"
)

const (
	ProfileOKX1H = profitmap.ProfileOKX1H
	ProfileOKX5M = profitmap.ProfileOKX5M

	drawdownLookbackHours = 168
)

// Profile wraps profitmap profile spec with G5 lookback helpers.
type Profile struct {
	profitmap.ProfileSpec
}

// ProfileFor returns the frozen profile specification.
func ProfileFor(profileID string) (Profile, error) {
	spec, err := profitmap.ProfileSpecFor(profileID)
	if err != nil {
		return Profile{}, err
	}
	return Profile{ProfileSpec: spec}, nil
}

// MaxLookbackBars is the minimum confirmed history required before a decision.
func (p Profile) MaxLookbackBars() int {
	bars := int(p.MaxFeatureLookback / p.BarDuration)
	if bars < 1 {
		return 1
	}
	return bars
}

// DrawdownLookbackBars converts the 168h drawdown window to bar count.
func (p Profile) DrawdownLookbackBars() int {
	bars := int(time.Duration(drawdownLookbackHours) * time.Hour / p.BarDuration)
	if bars < 2 {
		return 2
	}
	return bars
}

// RollingQuoteWindowBars is the rolling liquidity window in bars.
func (p Profile) RollingQuoteWindowBars() int {
	return profitmap.RollingWindowBars(p.ProfileSpec)
}

// PrimaryReturnBars is the primary symbol return horizon in bars.
func (p Profile) PrimaryReturnBars() int {
	if len(p.Horizons) == 0 {
		return 1
	}
	return p.Horizons[0].BarCount
}

// ProtocolProfileID validates against frozen G5 profile ids.
func ProtocolProfileID(profileID string) bool {
	for _, id := range g5protocol.DataProfileIDs {
		if id == profileID {
			return true
		}
	}
	return false
}
