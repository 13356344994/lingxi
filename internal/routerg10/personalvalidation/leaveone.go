package personalvalidation

// BuildLeaveOneReport evaluates leave-one announcement/exchange/symbol/day/shock robustness.
func BuildLeaveOneReport(daily DailyEquityReplay, events []TriggerEvent) LeaveOneReport {
	report := LeaveOneReport{
		AnnouncementLOOOK: true,
		ExchangeLOOOK:     true,
		SymbolLOOOK:       true,
		DayLOOOK:          true,
		ShockLOOOK:        true,
	}
	total := sumMap(daily.DailyPnL)
	if total <= 0 {
		return report
	}
	for day, pnl := range daily.DailyPnL {
		if total-pnl < 0 {
			report.DayLOOOK = false
			break
		}
		_ = day
	}
	shockTotal := sumMap(daily.ShockDayPnL)
	if shockTotal > 0 {
		for _, pnl := range daily.ShockDayPnL {
			if shockTotal-pnl < 0 {
				report.ShockLOOOK = false
				break
			}
		}
	}
	symbolTotal := sumMap(daily.SymbolPnL)
	if symbolTotal > 0 {
		for _, pnl := range daily.SymbolPnL {
			if symbolTotal-pnl < 0 {
				report.SymbolLOOOK = false
				break
			}
		}
	}
	annIDs := map[string]float64{}
	exIDs := map[string]float64{}
	for _, ev := range events {
		key := ev.PersonalTapeFamily + "|" + ev.ShockCluster
		annIDs[key] += daily.EventPnL[ev.EventID]
		exIDs[ev.ProfileID] += daily.EventPnL[ev.EventID]
	}
	for _, pnl := range annIDs {
		if sumMap(annIDs)-pnl < 0 {
			report.AnnouncementLOOOK = false
			break
		}
	}
	for _, pnl := range exIDs {
		if sumMap(exIDs)-pnl < 0 {
			report.ExchangeLOOOK = false
			break
		}
	}
	return report
}

func sumMap(m map[string]float64) float64 {
	total := 0.0
	for _, v := range m {
		total += v
	}
	return total
}
