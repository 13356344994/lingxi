package exclusionmap

import (
	"math"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	g10announcements "lingxi/internal/routerg10/announcements"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
)

const (
	minAbsoluteProfitUSDT       = 50.0
	capitalIdleOccupationRatio  = 0.25
	largeOrderImpactThreshold   = 8.0
	longIdleHoldHours           = 4.0
	drawdownRangeThresholdBPS   = 35.0
	minLotNonlinearityThreshold = 0.15
	eventSparsityCountThreshold = 12.0
	queueCrowdingImpactBPS      = 5.0
	publicCrowdedWinnerThreshold = 8
	crowdingCollapseThresholdBPS = 3.0
)

// MetricContext holds frozen calculable inputs for one event evaluation.
type MetricContext struct {
	Item            EventWorkItem
	Tiers           map[string]residualmap.TierCostBreakdown
	Base            residualmap.CostBase
	Adj             residualmap.PersonalAdjustments
	CellCount       int
	CellWinnerCount int
	VisibleAnnouncements int
	PostPublicationDecayBPS float64
	DecisionBarIdx  int
	ConstitutionHash string
	MapManifestHash string
	AnnouncementTapeHash string
	AnnouncementManifestHash string
	DynamicMinNotionalUSDT float64
}

func buildMetricContext(
	item EventWorkItem,
	bars []eventmap.Bar,
	spec eventmap.ProfileSpec,
	funding []profitmap.FundingPoint,
	ann AnnouncementEnvelope,
	constitutionHash string,
	cellCount, cellWinnerCount int,
	instrumentCat datatruth.InstrumentCatalog,
	hasInstrumentCat bool,
) (MetricContext, error) {
	idx := item.Event.DecisionBarIdx
	if idx < 0 {
		idx = findDecisionBarIndex(bars, item.Event.DecisionTime, spec.BarDuration)
	}
	if idx < 0 {
		return MetricContext{}, errBarReplayMiss
	}
	decisionBar := bars[idx]
	windowBars := eventmap.RollingWindowBars(spec)
	horizon := bestHorizon(spec)
	delay := bestDelay(item)
	holdHours := horizonHoldHours(spec, horizon)
	entryIndex := idx + 1 + delay
	var entryTime, exitTime time.Time
	if entryIndex < len(bars) {
		entryTime = bars[entryIndex].OpenTime
	}
	exitIndex := entryIndex
	for _, h := range spec.Horizons {
		if h.Label == horizon {
			exitIndex = entryIndex + h.BarCount
			break
		}
	}
	if exitIndex < len(bars) {
		exitTime = bars[exitIndex].OpenTime
	}
	base := residualmap.CostBase{
		RollingQuoteVolumeUSDT: profitmap.RollingQuoteVolumeUSDT(bars, idx, windowBars),
		BarHigh:                decisionBar.High,
		BarLow:                 decisionBar.Low,
		BarMid:                 decisionBar.Mid(),
		FundingBPS:             profitmap.FundingBPSOverHold(funding, entryTime, exitTime),
		ProfileID:              item.ProfileID,
		Horizon:                horizon,
		DelayBars:              delay,
		HoldHours:              holdHours,
	}
	gross := bestDirectionGross(item, bars, spec, funding, idx)
	tiers := residualmap.ComputeTierCosts(gross, base, residualmap.PersonalAdjustments{}, 100)
	visible := countVisibleAnnouncements(ann, item.Event.Symbol, item.Event.DecisionTime)
	decay := postPublicationDecayBPS(ann, item.Event.Symbol, item.Event.DecisionTime, gross)
	dynamicMin := 0.0
	if hasInstrumentCat {
		dynamicMin, _ = PITDynamicMinNotionalUSDT(
			instrumentCat, item.Event.Symbol, item.Event.DecisionTime, decisionBar.Mid(),
		)
	}
	return MetricContext{
		Item:                     item,
		Tiers:                    tiers,
		Base:                     base,
		Adj:                      residualmap.PersonalAdjustments{},
		CellCount:                cellCount,
		CellWinnerCount:          cellWinnerCount,
		VisibleAnnouncements:     visible,
		PostPublicationDecayBPS:  decay,
		DecisionBarIdx:           idx,
		ConstitutionHash:         constitutionHash,
		MapManifestHash:          item.ManifestHash,
		AnnouncementTapeHash:     ann.TapeHash,
		AnnouncementManifestHash: ann.ManifestHash,
		DynamicMinNotionalUSDT:   dynamicMin,
	}, nil
}

var errBarReplayMiss = barReplayMissError{}

type barReplayMissError struct{}

func (barReplayMissError) Error() string { return "bar replay miss" }

func findDecisionBarIndex(bars []eventmap.Bar, decision time.Time, barDuration time.Duration) int {
	want := decision.UTC()
	for i := range bars {
		closeTime := bars[i].OpenTime.Add(barDuration)
		if closeTime.Equal(want) {
			return i
		}
	}
	return -1
}

func bestHorizon(spec eventmap.ProfileSpec) string {
	if len(spec.Horizons) == 0 {
		return "1h"
	}
	return spec.Horizons[0].Label
}

func bestDelay(item EventWorkItem) int {
	if item.Event.DelayBars > 0 {
		return item.Event.DelayBars
	}
	return 1
}

func horizonHoldHours(spec eventmap.ProfileSpec, horizon string) float64 {
	for _, h := range spec.Horizons {
		if h.Label == horizon {
			return float64(h.BarCount) * spec.BarDuration.Hours()
		}
	}
	return spec.BarDuration.Hours()
}

func bestDirectionGross(
	item EventWorkItem,
	bars []eventmap.Bar,
	spec eventmap.ProfileSpec,
	funding []profitmap.FundingPoint,
	idx int,
) float64 {
	event := eventmap.DetectedEvent{
		ID:             item.Event.EventID,
		Symbol:         item.Event.Symbol,
		Family:         item.Event.EventFamily,
		DecisionTime:   item.Event.DecisionTime,
		DecisionBarIdx: idx,
		ShockCluster:   item.ShockCluster,
	}
	outcomes := eventmap.ExpandOutcomes(event, bars, spec, funding)
	best := 0.0
	for _, out := range outcomes {
		if out.GrossBPS > best {
			best = out.GrossBPS
		}
	}
	if best <= 0 {
		return 0
	}
	return best
}

func countVisibleAnnouncements(ann AnnouncementEnvelope, symbol string, decision time.Time) int {
	base := symbolBase(symbol)
	count := 0
	for _, a := range ann.Announcements {
		rec := g10announcements.Announcement{
			Symbol:      a.Symbol,
			PublishedAt: a.PublishedAt,
			FirstSeenAt: a.FirstSeenAt,
			EffectiveAt: a.EffectiveAt,
			SourceHash:  a.SourceHash,
		}
		if symbolBase(a.Symbol) != base {
			continue
		}
		if g10announcements.VisibleAt(rec, decision) {
			count++
		}
	}
	return count
}

func postPublicationDecayBPS(ann AnnouncementEnvelope, symbol string, decision time.Time, gross float64) float64 {
	if gross <= 0 {
		return 0
	}
	base := symbolBase(symbol)
	decay := 0.0
	for _, a := range ann.Announcements {
		rec := g10announcements.Announcement{
			Symbol:      a.Symbol,
			PublishedAt: a.PublishedAt,
			FirstSeenAt: a.FirstSeenAt,
			EffectiveAt: a.EffectiveAt,
			SourceHash:  a.SourceHash,
		}
		if symbolBase(a.Symbol) != base {
			continue
		}
		if !g10announcements.VisibleAt(rec, decision) {
			continue
		}
		switch strings.ToLower(a.Kind) {
		case "listing", "delisting", "maintenance", "suspension":
			decay += gross * 0.08
		}
	}
	if decay > gross*0.5 {
		decay = gross * 0.5
	}
	return decay
}

func symbolBase(symbol string) string {
	s := strings.ToUpper(symbol)
	s = strings.TrimSuffix(s, "-SWAP")
	return s
}

func barRangeBPS(base residualmap.CostBase) float64 {
	mid := math.Max(base.BarMid, 1e-12)
	return (base.BarHigh - base.BarLow) / mid * 10000
}

func absoluteProfitUSDT(tier residualmap.TierCostBreakdown) float64 {
	if tier.CapitalUSDT <= 0 {
		return 0
	}
	return tier.CapitalUSDT * tier.ConservativeBPS / 10000
}

func capitalIdleRatio(tier residualmap.TierCostBreakdown, base residualmap.CostBase) float64 {
	if base.RollingQuoteVolumeUSDT <= 0 || base.HoldHours <= 0 {
		return 0
	}
	dailyVolume := base.RollingQuoteVolumeUSDT
	return tier.CapitalUSDT * base.HoldHours / 24 / dailyVolume
}

func minLotNonlinearityRatio(tier residualmap.TierCostBreakdown, dynamicMinNotional float64) float64 {
	if tier.CapitalUSDT <= 0 || dynamicMinNotional <= 0 {
		return 0
	}
	return dynamicMinNotional / tier.CapitalUSDT
}

func largeTiersPass(tiers map[string]residualmap.TierCostBreakdown) bool {
	for _, label := range residualmap.LargeTierLabels {
		tier, ok := tiers[label]
		if !ok || tier.CapitalUSDT <= 0 {
			return false
		}
		if tier.CapacityFail || tier.ConservativeBPS < g10protocol.PersonalConservativeExpectedNetMin {
			return false
		}
	}
	return true
}

func personalTiersPass(tiers map[string]residualmap.TierCostBreakdown) bool {
	for _, label := range residualmap.UserTierLabels {
		if label == g10protocol.ActualEquityTierLabel {
			continue
		}
		tier, ok := tiers[label]
		if !ok || tier.CapitalUSDT <= 0 {
			continue
		}
		if tier.ConservativeBPS >= g10protocol.PersonalConservativeExpectedNetMin && !tier.CapacityFail {
			return true
		}
	}
	return false
}

func isFixedPublicSignal(item EventWorkItem, tiers map[string]residualmap.TierCostBreakdown) bool {
	if item.Event.EventFamily != eventmap.FamilyExtremeContinuation {
		return false
	}
	t30, ok30 := tiers["30"]
	t10k, ok10k := tiers["10000"]
	t100k, ok100k := tiers["100000"]
	if !ok30 || !ok10k || !ok100k {
		return false
	}
	return t30.ConservativeBPS >= 0 &&
		t10k.ConservativeBPS >= 0 &&
		t100k.ConservativeBPS >= 0 &&
		!t10k.CapacityFail &&
		!t100k.CapacityFail
}

func isFirstInOnly(item EventWorkItem) bool {
	return item.ProfileID == eventmap.ProfileOKX5M && item.Event.DelayBars == 0
}

func isSmallOnly(ctx MetricContext, institution, retail, bot AxisExclusion) bool {
	if institution.Excluded && retail.Excluded && bot.Excluded {
		return false
	}
	return ctx.Base.RollingQuoteVolumeUSDT < 2e5
}

func isPublicCrowded(ctx MetricContext, tiers map[string]residualmap.TierCostBreakdown) bool {
	return largeTiersPass(tiers) && ctx.CellWinnerCount >= publicCrowdedWinnerThreshold
}
