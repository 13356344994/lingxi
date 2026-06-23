package app

import (
	"strings"

	"lingxi/internal/venue"
)

// crossVenueKnownVenueDefaultIntervalHours 各所适配器 FundingIntervalsHours 的最小值(无 venues 注入时的静态回退)。
var crossVenueKnownVenueDefaultIntervalHours = map[string]int{
	"binance": 4,
	"bybit":   1,
	"okx":     1,
	"gate":    1,
}

// crossVenueResolveFundingIntervalHours 解析单条资费快照的结算间隔:优先用逐币 interval,否则用该所默认周期,最后才 8h。
func crossVenueResolveFundingIntervalHours(venueName string, snapInterval int, venues map[string]venue.Venue) int {
	if snapInterval > 0 {
		return snapInterval
	}
	return crossVenueVenueDefaultFundingIntervalHours(venueName, venues)
}

// crossVenueVenueDefaultFundingIntervalHours 返回交易所适配器声明的最小支持结算周期;未知所最后回退 8h。
func crossVenueVenueDefaultFundingIntervalHours(venueName string, venues map[string]venue.Venue) int {
	venueName = strings.ToLower(strings.TrimSpace(venueName))
	if venues != nil {
		if v := venues[venueName]; v != nil {
			caps := v.Capabilities()
			if len(caps.FundingIntervalsHours) > 0 {
				min := 0
				for _, h := range caps.FundingIntervalsHours {
					if h > 0 && (min == 0 || h < min) {
						min = h
					}
				}
				if min > 0 {
					return min
				}
			}
		}
	}
	if h, ok := crossVenueKnownVenueDefaultIntervalHours[venueName]; ok {
		return h
	}
	return 8
}

func crossVenueEnrichOppLegIntervals(opp *CrossVenueOpportunity, venues map[string]venue.Venue) {
	if opp == nil {
		return
	}
	if opp.HighIntervalHours <= 0 && (opp.HighFundingRate != 0 || !opp.HighNextFundingTime.IsZero()) {
		opp.HighIntervalHours = crossVenueVenueDefaultFundingIntervalHours(opp.HighVenue, venues)
	}
	if opp.LowIntervalHours <= 0 && (opp.LowFundingRate != 0 || !opp.LowNextFundingTime.IsZero()) {
		opp.LowIntervalHours = crossVenueVenueDefaultFundingIntervalHours(opp.LowVenue, venues)
	}
}
