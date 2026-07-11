package execution

import (
	"errors"
	"testing"
)

func TestValidateIntentTransitionLegalPath(t *testing.T) {
	path := []IntentState{
		IntentStatePending, IntentStateClaimed, IntentStateSubmitted,
		IntentStateWorking, IntentStateWorking, IntentStateFilled,
	}
	for i := 1; i < len(path); i++ {
		if err := ValidateIntentTransition(path[i-1], path[i]); err != nil {
			t.Fatalf("步骤 %d (%s->%s) 应合法: %v", i, path[i-1], path[i], err)
		}
	}
}

func TestValidateIntentTransitionRejectsIllegal(t *testing.T) {
	cases := []struct {
		from, to IntentState
	}{
		{IntentStateFilled, IntentStatePending},
		{IntentStateCanceled, IntentStateWorking},
		{IntentStateRejected, IntentStateFilled},
		{IntentStateQuarantined, IntentStateWorking},
		{IntentStatePending, IntentStateFilled},
	}
	for _, c := range cases {
		err := ValidateIntentTransition(c.from, c.to)
		if err == nil {
			t.Fatalf("%s->%s 应被判非法", c.from, c.to)
		}
		var target *ErrIllegalIntentTransition
		if !errors.As(err, &target) {
			t.Fatalf("错误类型应为 *ErrIllegalIntentTransition, got %T", err)
		}
	}
}

func TestValidateOrderTransitionLegalAndIllegal(t *testing.T) {
	if err := ValidateOrderTransition(OrderStateNew, OrderStateAcked); err != nil {
		t.Fatalf("new->acked 应合法: %v", err)
	}
	if err := ValidateOrderTransition(OrderStateAcked, OrderStatePartiallyFilled); err != nil {
		t.Fatalf("acked->partially_filled 应合法: %v", err)
	}
	if err := ValidateOrderTransition(OrderStatePartiallyFilled, OrderStatePartiallyFilled); err != nil {
		t.Fatalf("partially_filled 自环(追加成交)应合法: %v", err)
	}
	if err := ValidateOrderTransition(OrderStateFilled, OrderStateCanceled); err == nil {
		t.Fatal("filled->canceled 应非法(终态不应再迁移)")
	}
	if err := ValidateOrderTransition(OrderStateRejected, OrderStateAcked); err == nil {
		t.Fatal("rejected->acked 应非法")
	}
}

func TestAnyStateCanTransitionToUnknown(t *testing.T) {
	for _, s := range []OrderState{OrderStateNew, OrderStateAcked, OrderStatePartiallyFilled} {
		if err := ValidateOrderTransition(s, OrderStateUnknown); err != nil {
			t.Fatalf("%s->unknown 应总是合法: %v", s, err)
		}
	}
}

func TestResolveUnknownOrderTransitionRequiresTerminal(t *testing.T) {
	if err := ResolveUnknownOrderTransition(OrderStateAcked); err == nil {
		t.Fatal("unknown 收敛到非终态应报错")
	}
	for _, s := range []OrderState{OrderStateFilled, OrderStateCanceled, OrderStateRejected} {
		if err := ResolveUnknownOrderTransition(s); err != nil {
			t.Fatalf("unknown 收敛到终态 %s 应成功: %v", s, err)
		}
	}
}

func TestSideOfQty(t *testing.T) {
	cases := []struct {
		qty  float64
		want PositionSide
	}{
		{0, PositionFlat},
		{1e-12, PositionFlat},
		{1, PositionLong},
		{-1, PositionShort},
	}
	for _, c := range cases {
		if got := sideOfQty(c.qty); got != c.want {
			t.Fatalf("sideOfQty(%v)=%s, want %s", c.qty, got, c.want)
		}
	}
}

func TestTradeReducerRejectsOrderStateRegression(t *testing.T) {
	r := NewTradeReducer()
	if err := r.UpdateOrderState("cli1", OrderStateAcked); err != nil {
		t.Fatalf("首次状态应建立基线: %v", err)
	}
	if err := r.UpdateOrderState("cli1", OrderStateFilled); err != nil {
		t.Fatalf("acked->filled 应合法: %v", err)
	}
	if err := r.UpdateOrderState("cli1", OrderStateAcked); err == nil {
		t.Fatal("filled->acked 倒退必须拒绝")
	}
	state, _ := r.OrderState("cli1")
	if state != OrderStateFilled {
		t.Fatalf("非法倒退不应污染原状态, got %s", state)
	}
}
