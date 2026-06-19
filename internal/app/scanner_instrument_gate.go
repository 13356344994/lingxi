package app

import (
	"log"
	"strings"
)

// scannerMarketLegInstrumentValid 要求 tick 与 step 均 >0,表示该 venue+market 可安全下单。
func scannerMarketLegInstrumentValid(tick, step float64) bool {
	return tick > 0 && step > 0
}

// scannerInstrumentDropLog 扫描周期内按 symbol+venue+market 去重,避免重复日志。
type scannerInstrumentDropLog struct {
	seen map[string]struct{}
}

func newScannerInstrumentDropLog() *scannerInstrumentDropLog {
	return &scannerInstrumentDropLog{seen: make(map[string]struct{})}
}

func (d *scannerInstrumentDropLog) skip(strategyTag, symbol, venue, marketLabel string) {
	if d == nil {
		return
	}
	symbol = strings.TrimSpace(symbol)
	venue = strings.TrimSpace(venue)
	key := strategyTag + "|" + symbol + "|" + venue + "|" + marketLabel
	if _, ok := d.seen[key]; ok {
		return
	}
	d.seen[key] = struct{}{}
	log.Printf("[扫描] 跳过 %s %s: %s %s无有效 instrument(tick/step=0)", symbol, strategyTag, venue, marketLabel)
}

func scannerSpotLegEligible(m CrossVenueSpotPerpMarketSnap, strategyTag, symbol, venue string, drops *scannerInstrumentDropLog) bool {
	if !m.HasSpot || m.SpotMid <= 0 {
		return false
	}
	if !scannerMarketLegInstrumentValid(m.SpotTickSize, m.SpotStepSize) {
		drops.skip(strategyTag, symbol, venue, "现货")
		return false
	}
	return true
}

func scannerPerpLegEligible(m CrossVenueSpotPerpMarketSnap, strategyTag, symbol, venue string, drops *scannerInstrumentDropLog) bool {
	if !m.HasPerp || m.PerpMid <= 0 {
		return false
	}
	if !scannerMarketLegInstrumentValid(m.PerpTickSize, m.PerpStepSize) {
		drops.skip(strategyTag, symbol, venue, "永续")
		return false
	}
	return true
}

func scannerBothLegsInstrumentValid(spotMkt, perpMkt CrossVenueSpotPerpMarketSnap) bool {
	return scannerMarketLegInstrumentValid(spotMkt.SpotTickSize, spotMkt.SpotStepSize) &&
		scannerMarketLegInstrumentValid(perpMkt.PerpTickSize, perpMkt.PerpStepSize)
}
