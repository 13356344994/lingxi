package g2prize

import (
	"testing"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/protocol"
)

func TestSkyPatternMatchesOnlyFrozenAxes(t *testing.T) {
	combo := Combination{
		SkyState:     "ret=low/te=*/rv=high/dd=*/fund=*",
		SorterDomain: "liquid", Horizon: Horizon4h,
		FeatureKey: "funding_abs", Op: OpGTE, Threshold: 0,
		Direction: mechanism.DirectionLong,
	}
	event := Event{
		SkyState:     "ret=low/te=mid/rv=high/dd=low/fund=high",
		SorterDomain: "liquid", Horizon: Horizon4h,
		Direction: mechanism.DirectionLong, Features: map[string]float64{"funding_abs": 0.1},
	}
	if fires, _ := combo.Fires(event); !fires {
		t.Fatal("valid sky subset pattern did not match")
	}
	event.SkyState = "ret=high/te=mid/rv=high/dd=low/fund=high"
	if fires, _ := combo.Fires(event); fires {
		t.Fatal("different signed return bucket crossed sky boundary")
	}
}

func TestDefaultSearchSpaceIsSymmetricAndBounded(t *testing.T) {
	space, err := DefaultSearchSpace()
	if err != nil {
		t.Fatal(err)
	}
	if len(space.Combinations) != 162 || len(space.Combinations) > protocol.BackwardMaxSearchCombos {
		t.Fatalf("combinations=%d", len(space.Combinations))
	}
	if err := space.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Logf("g2 search definition hash=%s", space.DefinitionHash)
	long, short := 0, 0
	for _, combo := range space.Combinations {
		if combo.Direction == mechanism.DirectionLong {
			long++
		} else if combo.Direction == mechanism.DirectionShort {
			short++
		}
	}
	if long != short {
		t.Fatalf("long/short asymmetry: %d/%d", long, short)
	}
}
