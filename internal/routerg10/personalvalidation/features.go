package personalvalidation

import (
	"math"
	"time"

	"lingxi/internal/routerg10/announcements"
	"lingxi/internal/routerg10/personalbrain"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/eventvalidation"
)

// DecisionFeatureInput carries scan-time inputs for decision-visible features.
type DecisionFeatureInput struct {
	Bars           []eventmap.Bar
	BarIndex       int
	Spec           eventmap.ProfileSpec
	Boundaries     eventmap.Boundaries
	Thresholds     eventmap.FrozenThresholds
	BTCBars        []eventmap.Bar
	WindowBars     int
	PrimaryBack    int
	Decision       time.Time
	Symbol         string
	ProfileID      string
	Announcements  []announcements.Announcement
	ShockCluster   string
	CapitalUSDT    float64
	MinNotionalUSDT float64
	WantEventFamily string
}

// BuildDecisionVisibleFeatures computes trigger fields from market/announcement/event
// state only — never from frozen playbook constants (tape family, horizon, etc.).
func BuildDecisionVisibleFeatures(in DecisionFeatureInput) personalbrain.BarFeatures {
	g6 := eventvalidation.FeaturesFromBar(
		in.Bars, in.BarIndex, in.Spec, in.BTCBars,
		in.WindowBars, in.PrimaryBack, in.Decision, in.Spec.BarDuration,
	)
	visible := announcements.VisibleAnnouncements(in.Announcements, in.Decision)
	minLot := 0.0
	if in.MinNotionalUSDT > 0 && in.CapitalUSDT > 0 {
		minLot = in.MinNotionalUSDT / in.CapitalUSDT
	}
	detected := eventmap.DetectFamilies(
		in.Symbol, in.Bars, in.BarIndex, in.Spec, in.Boundaries, in.Thresholds,
		in.BTCBars, in.WindowBars,
	)
	eventFamily := ""
	for _, family := range detected {
		if in.WantEventFamily == "" || family == in.WantEventFamily {
			eventFamily = family
			break
		}
	}
	mechFlow := 0.0
	if math.Abs(g6.ReturnBPS) >= 5 && g6.RMSBPS >= 20 && sign(g6.ReturnBPS) != sign(g6.PriorReturnBPS) {
		mechFlow = 1
	}
	prepositioned := 0.0
	if minLot >= 0.15 && in.MinNotionalUSDT > 0 {
		prepositioned = 1
	}
	return personalbrain.BarFeatures{
		Symbol:                   in.Symbol,
		ProfileID:                in.ProfileID,
		ReturnBPS:                g6.ReturnBPS,
		PriorReturnBPS:           g6.PriorReturnBPS,
		BTCRelativeBPS:           g6.BTCRelativeBPS,
		BarRangeBPS:              g6.RMSBPS,
		Price:                    in.Bars[in.BarIndex].Close,
		VisibleAnnouncementCount: len(visible),
		CrossVenueShockCluster:   in.ShockCluster,
		DynamicMinNotionalUSDT:    in.MinNotionalUSDT,
		MinLotNonlinearity:       minLot,
		EventFamily:              eventFamily,
		MechanicalFlowExhaustion: mechFlow,
		PrepositionedCapitalWindow: prepositioned,
		CustodyAuthorizationLimit: prepositioned,
		EventSparsity:            eventSparsitySignal(g6),
		DynamicThresholdShift:    dynamicThresholdShiftSignal(g6),
	}
}

func eventSparsitySignal(g6 eventvalidation.FeatureSnapshot) float64 {
	if math.Abs(g6.ReturnBPS) >= 10 && g6.RMSBPS >= 15 {
		return 1
	}
	return 0
}

func dynamicThresholdShiftSignal(g6 eventvalidation.FeatureSnapshot) float64 {
	if sign(g6.ReturnBPS) != sign(g6.PriorReturnBPS) && g6.PriorReturnBPS != 0 && math.Abs(g6.ReturnBPS) >= 8 {
		return 1
	}
	return 0
}
