package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/execution"
	"lingxi/internal/venue"
)

type recordingDirectionalVenue struct {
	DirectionalVenue
	orders    []venue.OrderRequest
	positions []venue.Position
}

func (v *recordingDirectionalVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	v.orders = append(v.orders, req)
	return venue.OrderResult{Status: "filled", ExecutedQty: req.Qty}, nil
}

func (v *recordingDirectionalVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return append([]venue.Position(nil), v.positions...), nil
}

func TestDirectionalOrderExecutorSealsOpensButAllowsReduceOnly(t *testing.T) {
	v := &recordingDirectionalVenue{}
	x := newGuardedDirectionalOrderExecutor(v, true)

	_, err := x.PlaceOrder(context.Background(), venue.OrderRequest{Qty: 1})
	if !errors.Is(err, ErrDirectionalOpenSealed) {
		t.Fatalf("新开仓应物理封死, got %v", err)
	}
	if len(v.orders) != 0 {
		t.Fatalf("封火订单不应触达交易所, got %d", len(v.orders))
	}

	if _, err := x.PlaceOrder(context.Background(), venue.OrderRequest{Qty: 1, ReduceOnly: true}); err != nil {
		t.Fatalf("reduce-only 安全平仓必须放行: %v", err)
	}
	if len(v.orders) != 1 || !v.orders[0].ReduceOnly {
		t.Fatalf("仅应记录一笔 reduce-only, got %+v", v.orders)
	}
}

func TestDirectionalOrderExecutorFailsClosedWhenUnwired(t *testing.T) {
	e := &OKXDirectionalEngine{}
	_, err := e.placeDirectionalOrder(context.Background(), venue.OrderRequest{Qty: 1})
	if !errors.Is(err, ErrDirectionalOpenSealed) {
		t.Fatalf("未接线新开仓必须 fail-closed, got %v", err)
	}
}

func TestDirectionStackAlwaysRequiresSingleWriterLease(t *testing.T) {
	cfg := EngineConfig{}
	cfg.OKXDirectional.Enabled = true
	if !requiresLiveSingleWriterLease(cfg) {
		t.Fatal("方向栈即使 shadow-only 也必须独占单写租约")
	}
}

func TestLegacyAutonomousResearchCannotPromoteDuringRebuild(t *testing.T) {
	if !automaticLivePromotionSealed {
		t.Fatal("旧考试/自治生命周期不得拥有真钱晋级权")
	}
	if legacyAutonomousResearchEnabled {
		t.Fatal("旧 auto_miner/阅卷/自治循环应保持隔离，只保留原始影子数据")
	}
}

func TestDirectionalPlaceOrderHasSingleGateway(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"directional_live_executor.go": true,
		"exchange_truth_flatten.go":    true, // 独立 reduce-only 真仓收敛出口
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}
		data, readErr := os.ReadFile(filepath.Clean(name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), ".PlaceOrder(") {
			t.Fatalf("%s 绕过 directional order executor 直接调用 PlaceOrder", name)
		}
	}
}

func TestDirectionalExecutionClaimSerializesSameSymbol(t *testing.T) {
	e := &OKXDirectionalEngine{executionClaims: execution.NewClaimManager()}
	var won atomic.Int64
	var lost atomic.Int64
	start := make(chan struct{})
	releaseWinner := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			release, err := e.acquireExecutionClaim("BTC-USDT", fmt.Sprintf("intent-%d", i))
			if err != nil {
				lost.Add(1)
				return
			}
			won.Add(1)
			<-releaseWinner
			release()
		}(i)
	}
	close(start)
	for won.Load() != 1 || lost.Load() != 99 {
		runtime.Gosched()
	}
	close(releaseWinner)
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("同一时刻同 symbol 应恰好一个 claim 成功, got %d", won.Load())
	}
}

func TestDirectionalRecoveryQuarantinesExchangeOnlyPosition(t *testing.T) {
	v := &recordingDirectionalVenue{positions: []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.01}}}
	e := &OKXDirectionalEngine{
		v: v, venueName: "okx", recordDir: t.TempDir(), nowFn: time.Now,
		logger: log.New(io.Discard, "", 0), positions: map[string]*okxDirectionalPosition{},
		executionClaims: execution.NewClaimManager(),
	}
	if err := e.reconcileExchangePositionsOnBoot(context.Background()); err == nil {
		t.Fatal("交易所有仓而本地为空必须阻断恢复")
	}
	if e.isExecutionRecoveryReady() {
		t.Fatal("存在真仓差异时 recovery 不得 ready")
	}
	if _, ok := e.executionClaims.IsQuarantined("BTC-USDT"); !ok {
		t.Fatal("未知真实仓必须隔离 symbol")
	}
}

func TestDirectionalRecoveryReadyWhenFlatAndConsistent(t *testing.T) {
	v := &recordingDirectionalVenue{}
	e := &OKXDirectionalEngine{
		v: v, venueName: "okx", recordDir: t.TempDir(), nowFn: time.Now,
		logger: log.New(io.Discard, "", 0), positions: map[string]*okxDirectionalPosition{},
		executionClaims: execution.NewClaimManager(),
	}
	if err := e.reconcileExchangePositionsOnBoot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.isExecutionRecoveryReady() {
		t.Fatal("本地与交易所均空时 recovery 应 ready")
	}
}

func TestPrivateExecutionTruthDeduplicatesFillAndQuarantinesDivergence(t *testing.T) {
	now := time.Now().UTC()
	e := &OKXDirectionalEngine{
		venueName: "okx", recordDir: t.TempDir(), nowFn: func() time.Time { return now },
		logger: log.New(io.Discard, "", 0), executionClaims: execution.NewClaimManager(),
		executionReducer: execution.NewTradeReducer(),
	}
	order := venue.OrderResult{ClientOrderID: "c1", ExchangeOrderID: "o1", Status: "new"}
	e.applyPrivateExecutionEvent(venue.UserEvent{
		Kind: venue.UserEventOrderUpdate, Symbol: "BTC-USDT", Order: &order, ReceivedAt: now,
	})
	trade := venue.Trade{
		ID: "t1", OrderID: "o1", ClientOrderID: "c1", Symbol: "BTC-USDT",
		Side: venue.SideBuy, Qty: 1, Price: 100, Time: now,
	}
	event := venue.UserEvent{Kind: venue.UserEventTrade, Symbol: "BTC-USDT", Trade: &trade, ReceivedAt: now}
	e.applyPrivateExecutionEvent(event)
	e.applyPrivateExecutionEvent(event)
	if qty := e.executionReducer.PositionQty("BTC-USDT"); qty != 1 {
		t.Fatalf("重复 trade_id 不得双计, qty=%v", qty)
	}
	position := venue.Position{Symbol: "BTC-USDT", PositionAmt: 2}
	e.applyPrivateExecutionEvent(venue.UserEvent{
		Kind: venue.UserEventAccountUpdate, Symbol: "BTC-USDT", Position: &position, ReceivedAt: now,
	})
	if _, ok := e.executionClaims.IsQuarantined("BTC-USDT"); !ok {
		t.Fatal("私有持仓与成交账本不一致必须隔离")
	}
}
