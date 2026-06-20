package app

import (
	"log"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// venueSymbolsMatch 比较交易所/台账 symbol 是否同一标的(大小写、下划线/连字符归一)。
func venueSymbolsMatch(a, b venue.Symbol) bool {
	ca := canonicalTradingSymbol(string(a))
	cb := canonicalTradingSymbol(string(b))
	return ca != "" && ca == cb
}

func canonicalTradingSymbol(sym string) string {
	s := strings.ToUpper(strings.TrimSpace(sym))
	s = strings.ReplaceAll(s, "_", "-")
	return s
}

// carryStrategyOwnsSpotLeg 检查 (venue,symbol) 现货腿是否归属任一在场 carry 策略台账。
func carryStrategyOwnsSpotLeg(recordDir, venueName string, sym venue.Symbol) bool {
	recordDir = strings.TrimSpace(recordDir)
	venueName = strings.TrimSpace(venueName)
	if recordDir == "" || venueName == "" {
		return false
	}
	if carryCVSPOwnsSpotLeg(recordDir, venueName, sym) {
		return true
	}
	if carryCVPSOwnsSpotLeg(recordDir, venueName, sym) {
		return true
	}
	if carrySVSPOwnsSpotLeg(recordDir, venueName, sym) {
		return true
	}
	if carrySVPSOwnsSpotLeg(recordDir, venueName, sym) {
		return true
	}
	return false
}

func carryCVSPOwnsSpotLeg(recordDir, venueName string, sym venue.Symbol) bool {
	store, err := newCrossVenueSpotPerpPositionStore(recordDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range activeCrossVenueSpotPerpPositions(ps) {
		if !venueSymbolsMatch(venue.Symbol(p.Symbol), sym) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.SpotVenue), venueName) {
			return true
		}
	}
	return false
}

func carryCVPSOwnsSpotLeg(recordDir, venueName string, sym venue.Symbol) bool {
	store, err := newCrossVenuePerpSpotPositionStore(recordDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range activeCrossVenuePerpSpotPositions(ps) {
		if !venueSymbolsMatch(venue.Symbol(p.Symbol), sym) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.SpotVenue), venueName) {
			return true
		}
	}
	return false
}

func carrySVSPOwnsSpotLeg(recordDir, venueName string, sym venue.Symbol) bool {
	store, err := newSingleVenueSpotPerpPositionStore(recordDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range activeSingleVenueSpotPerpPositions(ps) {
		if !venueSymbolsMatch(venue.Symbol(p.Symbol), sym) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.Venue), venueName) {
			return true
		}
	}
	return false
}

func carrySVPSOwnsSpotLeg(recordDir, venueName string, sym venue.Symbol) bool {
	store, err := newSingleVenuePerpSpotPositionStore(recordDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range activeSingleVenuePerpSpotPositions(ps) {
		if !venueSymbolsMatch(venue.Symbol(p.Symbol), sym) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.Venue), venueName) {
			return true
		}
	}
	return false
}

// carryStrategyOwnsPerpLeg 检查 (venue,symbol) 永续腿是否归属任一在场 carry 策略台账。
func carryStrategyOwnsPerpLeg(recordDir, venueName string, sym venue.Symbol) bool {
	recordDir = strings.TrimSpace(recordDir)
	venueName = strings.TrimSpace(venueName)
	if recordDir == "" || venueName == "" {
		return false
	}
	if carryCVSPOwnsPerpLeg(recordDir, venueName, sym) {
		return true
	}
	if carryCVPSOwnsPerpLeg(recordDir, venueName, sym) {
		return true
	}
	if carrySVSPOwnsPerpLeg(recordDir, venueName, sym) {
		return true
	}
	if carrySVPSOwnsPerpLeg(recordDir, venueName, sym) {
		return true
	}
	return false
}

func carryCVSPOwnsPerpLeg(recordDir, venueName string, sym venue.Symbol) bool {
	store, err := newCrossVenueSpotPerpPositionStore(recordDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range activeCrossVenueSpotPerpPositions(ps) {
		if !venueSymbolsMatch(venue.Symbol(p.Symbol), sym) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.PerpVenue), venueName) {
			return true
		}
	}
	return false
}

func carryCVPSOwnsPerpLeg(recordDir, venueName string, sym venue.Symbol) bool {
	store, err := newCrossVenuePerpSpotPositionStore(recordDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range activeCrossVenuePerpSpotPositions(ps) {
		if !venueSymbolsMatch(venue.Symbol(p.Symbol), sym) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.PerpVenue), venueName) {
			return true
		}
	}
	return false
}

func carrySVSPOwnsPerpLeg(recordDir, venueName string, sym venue.Symbol) bool {
	store, err := newSingleVenueSpotPerpPositionStore(recordDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range activeSingleVenueSpotPerpPositions(ps) {
		if !venueSymbolsMatch(venue.Symbol(p.Symbol), sym) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.Venue), venueName) {
			return true
		}
	}
	return false
}

func carrySVPSOwnsPerpLeg(recordDir, venueName string, sym venue.Symbol) bool {
	store, err := newSingleVenuePerpSpotPositionStore(recordDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range activeSingleVenuePerpSpotPositions(ps) {
		if !venueSymbolsMatch(venue.Symbol(p.Symbol), sym) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.Venue), venueName) {
			return true
		}
	}
	return false
}

// carryReconcileAdoptPositiveLegQtys 将所内实测 qty 写回 carry 台账(重启/崩溃后再采)。
func carryReconcileAdoptPositiveLegQtys(pos *CrossVenueSpotPerpPosition, spotQty, perpQty float64, now time.Time, logger *log.Logger, tag string) bool {
	if pos == nil {
		return false
	}
	changed := false
	if spotQty > executionQtyTolerance && absQty(pos.SpotLeg.Qty-spotQty) > executionQtyTolerance {
		pos.SpotLeg.Qty = spotQty
		changed = true
	}
	if perpQty > executionQtyTolerance && absQty(pos.PerpLeg.Qty-perpQty) > executionQtyTolerance {
		pos.PerpLeg.Qty = perpQty
		changed = true
	}
	if changed {
		pos.UpdatedAt = now
		if logger != nil {
			logger.Printf("[%s] reconcile:%s 重采所内腿 spot=%.6f perp=%.6f", tag, pos.Symbol, pos.SpotLeg.Qty, pos.PerpLeg.Qty)
		}
	}
	return changed
}

// carryReconcileAdoptNegativeLegQtys 将所内实测 qty 写回 CVPS 台账(perp 多 / spot 空)。
func carryReconcileAdoptNegativeLegQtys(pos *CrossVenuePerpSpotPosition, spotShort, perpLong float64, now time.Time, logger *log.Logger, tag string) bool {
	if pos == nil {
		return false
	}
	changed := false
	if spotShort > executionQtyTolerance && absQty(pos.SpotLeg.Qty-spotShort) > executionQtyTolerance {
		pos.SpotLeg.Qty = spotShort
		changed = true
	}
	if perpLong > executionQtyTolerance && absQty(pos.PerpLeg.Qty-perpLong) > executionQtyTolerance {
		pos.PerpLeg.Qty = perpLong
		changed = true
	}
	if changed {
		pos.UpdatedAt = now
		if logger != nil {
			logger.Printf("[%s] reconcile:%s 重采所内腿 spot_short=%.6f perp_long=%.6f", tag, pos.Symbol, pos.SpotLeg.Qty, pos.PerpLeg.Qty)
		}
	}
	return changed
}
