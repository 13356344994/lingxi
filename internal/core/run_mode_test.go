package core

import "testing"

func TestParseRunMode(t *testing.T) {
	cases := []struct {
		in   string
		want RunMode
	}{
		{"OFF", RunModeOFF},
		{"off", RunModeOFF},
		{"CANARY_LIVE", RunModeCANARYLive},
		{"canary", RunModeCANARYLive},
		{"LIVE", RunModeLIVE},
		{"live", RunModeLIVE},
		{"bogus", RunModeOFF},
	}
	for _, tc := range cases {
		if got := ParseRunMode(tc.in); got != tc.want {
			t.Fatalf("ParseRunMode(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestRunModeIsLiveMoney(t *testing.T) {
	if RunModeOFF.IsLiveMoney() {
		t.Fatal("OFF must not be live money")
	}
	if !RunModeCANARYLive.IsLiveMoney() || !RunModeLIVE.IsLiveMoney() {
		t.Fatal("CANARY_LIVE and LIVE must be live money")
	}
	if RunModeOFF.AllowsRealLedgerWrites() {
		t.Fatal("OFF must not allow real ledger writes")
	}
}
