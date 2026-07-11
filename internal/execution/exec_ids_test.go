package execution

import (
	"regexp"
	"testing"
	"time"
)

var idCharsetRe = regexp.MustCompile(`^[a-z0-9.]+$`)

func TestNewDecisionIDFormat(t *testing.T) {
	at := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	id := NewDecisionID(at)
	if id[0] != 'd' {
		t.Fatalf("decision_id 应以 'd' 开头, got %q", id)
	}
	if !idCharsetRe.MatchString(id) {
		t.Fatalf("decision_id 字符集非法: %q", id)
	}
}

func TestNewDecisionIDLowCollisionAcrossCalls(t *testing.T) {
	at := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		id := NewDecisionID(at)
		if seen[id] {
			t.Fatalf("50 次生成出现重复 decision_id: %q", id)
		}
		seen[id] = true
	}
}

func TestDeriveIntentClientTradeIDDeterministic(t *testing.T) {
	decisionID := "dfixeddecision123"
	intentID1 := DeriveIntentID(decisionID, 0)
	intentID1Again := DeriveIntentID(decisionID, 0)
	if intentID1 != intentID1Again {
		t.Fatalf("相同输入应产出相同 intent_id: %q vs %q", intentID1, intentID1Again)
	}
	intentID2 := DeriveIntentID(decisionID, 1)
	if intentID1 == intentID2 {
		t.Fatalf("不同 seq 应产出不同 intent_id, both=%q", intentID1)
	}
	if !idCharsetRe.MatchString(intentID1) {
		t.Fatalf("intent_id 字符集非法: %q", intentID1)
	}

	clientOrderID1 := DeriveClientOrderID(intentID1, 0)
	clientOrderID1Again := DeriveClientOrderID(intentID1, 0)
	if clientOrderID1 != clientOrderID1Again {
		t.Fatalf("相同输入应产出相同 client_order_id: %q vs %q", clientOrderID1, clientOrderID1Again)
	}
	clientOrderID2 := DeriveClientOrderID(intentID1, 1)
	if clientOrderID1 == clientOrderID2 {
		t.Fatalf("不同 attempt 应产出不同 client_order_id, both=%q", clientOrderID1)
	}
	if !idCharsetRe.MatchString(clientOrderID1) {
		t.Fatalf("client_order_id 字符集非法: %q", clientOrderID1)
	}

	tradeID1 := DeriveLocalTradeID(clientOrderID1, 0)
	tradeID1Again := DeriveLocalTradeID(clientOrderID1, 0)
	if tradeID1 != tradeID1Again {
		t.Fatalf("相同输入应产出相同 trade_id: %q vs %q", tradeID1, tradeID1Again)
	}
	tradeID2 := DeriveLocalTradeID(clientOrderID1, 1)
	if tradeID1 == tradeID2 {
		t.Fatalf("不同 fillSeq 应产出不同 trade_id, both=%q", tradeID1)
	}
	if !idCharsetRe.MatchString(tradeID1) {
		t.Fatalf("trade_id 字符集非法: %q", tradeID1)
	}

	// 谱系可从 client_order_id 前缀还原出 intent_id,再还原出 decision_id。
	if got := clientOrderID1[:len(intentID1)]; got != intentID1 {
		t.Fatalf("client_order_id 应以其 intent_id 为前缀: %q vs %q", got, intentID1)
	}
	if got := intentID1[:len(decisionID)]; got != decisionID {
		t.Fatalf("intent_id 应以其 decision_id 为前缀: %q vs %q", got, decisionID)
	}
}

func TestDeriveIntentIDNegativeSeqClampedToZero(t *testing.T) {
	a := DeriveIntentID("dfoo", -5)
	b := DeriveIntentID("dfoo", 0)
	if a != b {
		t.Fatalf("负 seq 应钳制为 0: %q vs %q", a, b)
	}
}
