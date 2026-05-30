package main

import "testing"

func TestIsManagedClientOrderIDRecognizesEnginePrefixes(t *testing.T) {
	cases := []string{
		"fopen-btcusdt-1",
		"fclose-btcusdt-1",
		"sopen-btcusdt-1",
		"sclosemaker-btcusdt-1",
		"sclosechase-btcusdt-1",
		"sclose_revert-btcusdt-1",
		"srebalance-btcusdt-1",
		"srebalf-btcusdt-1",
		"exp_sell-btcusdt-1",
		"exp_buy-btcusdt-1",
		"expfclose-btcusdt-1",
	}
	for _, clientOrderID := range cases {
		if !isManagedClientOrderID(clientOrderID) {
			t.Fatalf("expected managed client order id to be tracked: %s", clientOrderID)
		}
	}
}

func TestIsManagedClientOrderIDRejectsExternalOrderIDs(t *testing.T) {
	cases := []string{
		"",
		"manual-order-1",
		"grid-bot-ethusdt-1",
		"thirdparty-abc",
	}
	for _, clientOrderID := range cases {
		if isManagedClientOrderID(clientOrderID) {
			t.Fatalf("did not expect external client order id to be tracked: %s", clientOrderID)
		}
	}
}
