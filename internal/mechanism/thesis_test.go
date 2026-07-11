package mechanism

import "testing"

func baseValidThesis() Thesis {
	return Thesis{
		Expert:         "unit",
		CardID:         "c1",
		Payer:          PayerLongLiquidations,
		ForcedAction:   "forced_sell",
		Direction:      DirectionShort,
		DeadlineUnixMs: 1000,
		Invalidators:   []Invalidator{{Metric: "remaining_forced_notional", Condition: "drains", Observable: true}},
	}
}

// TestThesisValidateRejectsMissingHardPreconditions 钉死三条硬前提：无 payer / 无期限 /
// 无运行时可观测证伪器的论点一律被拒（对应「仅凭『跌多会涨』直接淘汰」）。
func TestThesisValidateRejectsMissingHardPreconditions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Thesis)
		want   string
	}{
		{"valid", func(*Thesis) {}, ""},
		{"no_payer", func(th *Thesis) { th.Payer = PayerUnknown }, RejectNoPayer},
		{"no_deadline", func(th *Thesis) { th.DeadlineUnixMs = 0 }, RejectNoDeadline},
		{"negative_deadline", func(th *Thesis) { th.DeadlineUnixMs = -5 }, RejectNoDeadline},
		{"no_invalidator", func(th *Thesis) { th.Invalidators = nil }, RejectNoInvalidator},
		{"only_unobservable_invalidator", func(th *Thesis) {
			th.Invalidators = []Invalidator{{Metric: "gut_feel", Condition: "price fell a lot", Observable: false}}
		}, RejectNoInvalidator},
		{"observable_without_metric", func(th *Thesis) {
			th.Invalidators = []Invalidator{{Condition: "price fell a lot", Observable: true}}
		}, RejectNoInvalidator},
		{"observable_without_condition", func(th *Thesis) {
			th.Invalidators = []Invalidator{{Metric: "remaining_forced_notional", Observable: true}}
		}, RejectNoInvalidator},
		{"flat_direction", func(th *Thesis) { th.Direction = DirectionFlat }, RejectNoDirection},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			th := baseValidThesis()
			tc.mutate(&th)
			ok, reason := th.Validate()
			if tc.want == "" {
				if !ok {
					t.Fatalf("expected valid, got reject %q", reason)
				}
				return
			}
			if ok {
				t.Fatalf("expected reject %q, got valid", tc.want)
			}
			if reason != tc.want {
				t.Fatalf("reject reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

// TestAcceptEnforcesValidate 钉死 accept 不会放过缺硬前提的论点：即便专家构造了论点，
// 只要缺 payer / 期限 / 证伪器，accept 也会降级为拒绝。
func TestAcceptEnforcesValidate(t *testing.T) {
	bad := baseValidThesis()
	bad.Invalidators = nil
	ev := accept(bad)
	if ev.Accepted {
		t.Fatal("accept must reject a thesis without observable invalidator")
	}
	if ev.RejectReason != RejectNoInvalidator {
		t.Fatalf("reject reason = %q, want %q", ev.RejectReason, RejectNoInvalidator)
	}

	good := accept(baseValidThesis())
	if !good.Accepted {
		t.Fatalf("accept must pass a valid thesis, got %q", good.RejectReason)
	}
}

func TestDirectionOpposite(t *testing.T) {
	if DirectionLong.Opposite() != DirectionShort {
		t.Fatal("long opposite should be short")
	}
	if DirectionShort.Opposite() != DirectionLong {
		t.Fatal("short opposite should be long")
	}
	if DirectionFlat.Opposite() != DirectionFlat {
		t.Fatal("flat opposite should be flat")
	}
}
