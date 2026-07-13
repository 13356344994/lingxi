package executionpolicy

// ReferenceNotional returns the notional basis for BPS<->USDT conversion.
func ReferenceNotional(tr TriggerRecord) float64 {
	if tr.ReferenceNotionalUSDT > 0 {
		return tr.ReferenceNotionalUSDT
	}
	return 100
}

// BpsToUSDT converts basis points to absolute USDT at a reference notional.
func BpsToUSDT(bps, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	return notional * bps / 10000
}

// USDTToBps converts absolute USDT to basis points at a reference notional.
func USDTToBps(usdt, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	return usdt / notional * 10000
}

func setExpectedNet(ev *ActionEvaluation, bps, usdt float64) {
	ev.ExpectedNetBPS = bps
	ev.ExpectedNetUSDT = usdt
}

func populateExpectedNetFromBPS(ev *ActionEvaluation, bps, notional float64) {
	ev.ExpectedNetBPS = bps
	ev.ExpectedNetUSDT = BpsToUSDT(bps, notional)
}

func populateExpectedNetFromUSDT(ev *ActionEvaluation, usdt, notional float64) {
	ev.ExpectedNetUSDT = usdt
	ev.ExpectedNetBPS = USDTToBps(usdt, notional)
}

// ActionExpectedNetBPS returns conservative BPS without double conversion.
func ActionExpectedNetBPS(a ActionEvaluation, refNotional float64) float64 {
	if a.ExpectedNetBPS != 0 {
		return a.ExpectedNetBPS
	}
	if refNotional > 0 && a.ExpectedNetUSDT != 0 {
		return USDTToBps(a.ExpectedNetUSDT, refNotional)
	}
	return 0
}
