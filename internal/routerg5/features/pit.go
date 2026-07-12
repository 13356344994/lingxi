package features

import (
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/profitmap"
)

// ListingRule captures point-in-time listing/delisting boundaries for one symbol.
type ListingRule struct {
	ListedAt   time.Time
	DelistedAt time.Time
	HasCatalog bool
}

// PITUniverseInput holds per-symbol bar history for universe filtering.
type PITUniverseInput struct {
	Symbol  string
	Bars    []profitmap.Bar
	Listing ListingRule
}

// EligibleAt reports whether symbol is in the PIT universe at decisionTime.
// Requires a confirmed closed bar at decisionTime, sufficient lookback, and no
// future listing or current-universe backfill.
func EligibleAt(in PITUniverseInput, profile Profile, decisionTime time.Time) bool {
	if decisionTime.IsZero() {
		return false
	}
	if in.Listing.HasCatalog {
		if decisionTime.Before(in.Listing.ListedAt) {
			return false
		}
		if !in.Listing.DelistedAt.IsZero() && !decisionTime.Before(in.Listing.DelistedAt) {
			return false
		}
	}
	idx := profitmap.BarIndexAt(in.Bars, decisionTime, profile.BarDuration)
	if idx < 0 {
		return false
	}
	if profitmap.RejectCurrentBarOpen(in.Bars, idx, profile.BarDuration) {
		return false
	}
	if !in.Bars[idx].Confirmed {
		return false
	}
	lookback := profile.MaxLookbackBars()
	if idx < lookback {
		return false
	}
	firstClose := in.Bars[0].OpenTime.Add(profile.BarDuration)
	if decisionTime.Before(firstClose.Add(time.Duration(lookback-1) * profile.BarDuration)) {
		return false
	}
	if !in.Listing.HasCatalog {
		listingTime := firstClose
		if decisionTime.Before(listingTime.Add(time.Duration(lookback-1) * profile.BarDuration)) {
			return false
		}
	}
	return true
}

// VisibleSymbols returns PIT-eligible symbols at decisionTime sorted by symbol.
func VisibleSymbols(inputs []PITUniverseInput, profile Profile, decisionTime time.Time) []string {
	var out []string
	for _, in := range inputs {
		if EligibleAt(in, profile, decisionTime) {
			out = append(out, in.Symbol)
		}
	}
	sortStrings(out)
	return out
}

// ListingRuleFromCatalog resolves instrument listing boundaries at knownAt.
func ListingRuleFromCatalog(catalog datatruth.InstrumentCatalog, instrumentID string, knownAt time.Time) ListingRule {
	inst, ok := catalog.InstrumentAt(instrumentID, knownAt, knownAt)
	if !ok {
		return ListingRule{}
	}
	return ListingRule{
		ListedAt:   inst.ListedAt.UTC(),
		DelistedAt: inst.DelistedAt.UTC(),
		HasCatalog: true,
	}
}

// RejectFutureListing rejects decisions before a symbol's first confirmed bar close.
func RejectFutureListing(bars []profitmap.Bar, profile Profile, decisionTime time.Time) bool {
	if len(bars) == 0 {
		return true
	}
	firstClose := bars[0].OpenTime.Add(profile.BarDuration)
	return decisionTime.Before(firstClose)
}
