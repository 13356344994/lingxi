package app

import "testing"

// TestBaseAssetOf 校验 base asset 解析对「币安原始拼接」与「中性 canonical」两种形态一致。
func TestBaseAssetOf(t *testing.T) {
	cases := map[string]string{
		"BTCUSDT":      "BTC",
		"BTC-USDT":     "BTC",
		"ETHUSDC":      "ETH",
		"DOGEUSDT":     "DOGE",
		"1000PEPEUSDT": "1000PEPE",
		"btcusdt":      "BTC",
		"SOL-USDT":     "SOL",
		"":             "",
	}
	for in, want := range cases {
		if got := baseAssetOf(in); got != want {
			t.Fatalf("baseAssetOf(%q)=%q want %q", in, got, want)
		}
	}
}

// TestGetSymbolRiskProfileTierByBaseAsset 锚定 P2.3 零行为:分层改按 base asset 后,币安原始符号的
// 分层结果与改造前一致;且中性 canonical 形态解析到同一层。
func TestGetSymbolRiskProfileTierByBaseAsset(t *testing.T) {
	tierOf := func(sym string) string { return GetSymbolRiskProfile(sym).Tier }

	// major(原始 + canonical 同层)
	for _, sym := range []string{"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT", "XRPUSDT", "BTC-USDT", "ETH-USDT"} {
		if got := tierOf(sym); got != "major" {
			t.Fatalf("%s 应为 major, got %s", sym, got)
		}
	}
	// mainstream
	for _, sym := range []string{"DOGEUSDT", "ADAUSDT", "LINKUSDT", "SUIUSDT", "DOGE-USDT"} {
		if got := tierOf(sym); got != "mainstream" {
			t.Fatalf("%s 应为 mainstream, got %s", sym, got)
		}
	}
	// long_tail(不在任一层)
	for _, sym := range []string{"PORTALUSDT", "FIDAUSDT", "1000PEPEUSDT", "FIDA-USDT"} {
		if got := tierOf(sym); got != "long_tail" {
			t.Fatalf("%s 应为 long_tail, got %s", sym, got)
		}
	}
}
