package historyexam

import "testing"

func validStratum() StratumInput {
	return StratumInput{Axes: map[string]string{
		"volatility":    "high",
		"funding_state": "positive",
	}}
}

func TestStratumInputRejectsRevenueDerivedAxes(t *testing.T) {
	cases := map[string]StratumInput{
		"empty":           {Axes: map[string]string{}},
		"unknown_axis":    {Axes: map[string]string{"whatever": "x"}},
		"revenue_axis":    {Axes: map[string]string{"pnl": "up"}},
		"forbidden_value": {Axes: map[string]string{"volatility": "high_sharpe"}},
		"return_axis":     {Axes: map[string]string{"forward_return": "high"}},
		"empty_value":     {Axes: map[string]string{"volatility": ""}},
		"edge_smuggled":   {Axes: map[string]string{"liquidity_proxy": "positive_edge"}},
	}
	for name, in := range cases {
		if err := in.Validate(); err == nil {
			t.Errorf("%s: expected the no-revenue input contract to reject %+v", name, in.Axes)
		}
	}
}

func TestStratumInputAcceptsDecisionTimeAxes(t *testing.T) {
	in := StratumInput{Axes: map[string]string{
		"time_scale":       "intraday",
		"volatility":       "high",
		"trend_efficiency": "trending",
		"market_breadth":   "narrow",
		"liquidity_proxy":  "thin",
		"funding_state":    "negative",
	}}
	if err := in.Validate(); err != nil {
		t.Fatalf("valid decision-time axes should pass: %v", err)
	}
}

func TestStratumKeyIsOrderIndependentAndDistinct(t *testing.T) {
	a := StratumInput{Axes: map[string]string{"volatility": "high", "funding_state": "positive"}}
	b := StratumInput{Axes: map[string]string{"funding_state": "positive", "volatility": "high"}}
	ka, err := a.Key()
	if err != nil {
		t.Fatal(err)
	}
	kb, err := b.Key()
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Fatalf("stratum key must be order-independent: %s != %s", ka, kb)
	}
	c := StratumInput{Axes: map[string]string{"volatility": "low", "funding_state": "positive"}}
	kc, err := c.Key()
	if err != nil {
		t.Fatal(err)
	}
	if kc == ka {
		t.Fatal("different regimes must produce different stratum keys")
	}
}
