package app

import (
	"errors"
	"fmt"
	"io"
	"log"
	"sync/atomic"
	"testing"
	"time"
)

// TestIsBorrowUnavailableError 覆盖借币不可用错误码识别:命中 -3045/-3006/-3008(直串 / signedRequestError.Body /
// 多层包裹),不命中其它错误码、nil、普通错误。
func TestIsBorrowUnavailableError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain -3045", errors.New(`status 400 body={"code":-3045,"msg":"The system does not have enough asset now."}`), true},
		{"plain -3006", errors.New(`body={"code":-3006,"msg":"exceed maximum borrow amount"}`), true},
		{"plain -3008", errors.New(`body={"code":-3008,"msg":"Borrow is banned for this asset."}`), true},
		{"other code -2010", errors.New(`body={"code":-2010,"msg":"Account has insufficient balance"}`), false},
		{"generic", errors.New("context deadline exceeded"), false},
		{
			"signedRequestError body -3045",
			signedRequestError{Endpoint: "https://api.binance.com/sapi/v1/margin/order", StatusCode: 400, Status: "400 Bad Request", Body: `{"code":-3045,"msg":"The system does not have enough asset now."}`},
			true,
		},
		{
			"wrapped signedRequestError -3045",
			fmt.Errorf("现货补仓失败，已回补合约空单: %w", signedRequestError{StatusCode: 400, Body: `{"code":-3045,"msg":"no asset"}`}),
			true,
		},
		{
			"signedRequestError body -2019 (margin insufficient, not borrow-unavailable)",
			signedRequestError{StatusCode: 400, Body: `{"code":-2019,"msg":"Margin is insufficient."}`},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBorrowUnavailableError(tc.err); got != tc.want {
				t.Fatalf("isBorrowUnavailableError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestBorrowUnavailableQueryFormMatches 钉死 MaxBorrowable 端点返回的错误串形态(「签名请求状态异常: ... body={...}」)
// 也能被识别为借不到 —— 这是 R29 把 -3045 查询错误判定为「确定性借不到→剔除」的前提。
func TestBorrowUnavailableQueryFormMatches(t *testing.T) {
	err := errors.New(`签名请求状态异常: 400 400 Bad Request body={"code":-3045,"msg":"The system does not have enough asset now."}`)
	if !isBorrowUnavailableError(err) {
		t.Fatal("MaxBorrowable -3045 错误串应被识别为借不到")
	}
}

// TestDecideBorrowAdmissionKnownZeroExcludes 钉死:已知最大可借=0(R29 把 -3045 查询映射为 known/qty=0)时,
// 负向 carry 候选必被剔除 —— 这正是 HOME 应在选币阶段干净跳过、不再白开合约腿的判定。
func TestDecideBorrowAdmissionKnownZeroExcludes(t *testing.T) {
	res := decideBorrowAdmission(borrowAdmissionInput{
		Asset:              "HOME",
		RequiredBaseQty:    652,
		MaxBorrowableQty:   0,
		MaxBorrowableKnown: true,
	}, borrowAdmissionConfig{Enabled: true, MinHeadroomRate: 0.10})
	if res.Admit {
		t.Fatal("已知最大可借=0 时应剔除候选")
	}
}

// TestBorrowUnavailableRevertedErrorUnwrapsViaErrorsAs 确认包裹后仍可经 errors.As 命中哨兵类型(executeOpenHedgePlans 据此分支)。
func TestBorrowUnavailableRevertedErrorUnwrapsViaErrorsAs(t *testing.T) {
	err := error(borrowUnavailableRevertedError{message: "现货补仓失败(借币不可用)，已回补合约空单"})
	var sentinel borrowUnavailableRevertedError
	if !errors.As(err, &sentinel) {
		t.Fatal("expected errors.As to match borrowUnavailableRevertedError")
	}
}

// TestHandleBorrowUnavailableOpenSkipNonFatal 验证良性跳过:置 symbol 冷却 + 【不递增熔断计数】。
func TestHandleBorrowUnavailableOpenSkipNonFatal(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:              DefaultEngineConfig(),
		logger:           log.New(io.Discard, "", 0),
		portfolioManager: portfolioManager{symbolCooldowns: make(map[string]symbolCooldownState)},
	}
	engine.cfg.RiskConfig.SymbolAbnormalCooldown = time.Minute

	engine.handleBorrowUnavailableOpenSkip("HOMEUSDT", `{"code":-3045,"msg":"no asset"}`)

	if got := atomic.LoadInt32(&engine.executionIncidents); got != 0 {
		t.Fatalf("expected executionIncidents to stay 0 (non-fatal), got %d", got)
	}
	state, ok := engine.symbolCooldowns["HOMEUSDT"]
	if !ok {
		t.Fatal("expected HOMEUSDT to be put on symbol cooldown")
	}
	if state.Category != "borrow_unavailable" || state.Severity != "major" {
		t.Fatalf("unexpected cooldown state: %+v", state)
	}
	if !state.Until.After(time.Now().UTC()) {
		t.Fatalf("expected cooldown Until in the future, got %s", state.Until)
	}
}
