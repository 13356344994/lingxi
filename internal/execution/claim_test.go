package execution

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestClaimManagerConcurrentSingleClaimSameSymbol 是 execution-truth 的核心并发不变量测试:
// 100 个并发决策抢同一 symbol 的 claim,必须恰好一个成功。
func TestClaimManagerConcurrentSingleClaimSameSymbol(t *testing.T) {
	m := NewClaimManager()
	const n = 100
	var wg sync.WaitGroup
	var successCount atomic.Int64
	var claimedCount atomic.Int64
	releases := make(chan func(), n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		owner := fmt.Sprintf("intent-%03d", i)
		go func(owner string) {
			defer wg.Done()
			release, err := m.TryAcquire("BTC-USDT", owner, time.Now())
			if err == nil {
				successCount.Add(1)
				claimedCount.Add(1)
				releases <- release
				return
			}
			if !errors.Is(err, ErrSymbolClaimed) {
				t.Errorf("owner=%s 失败原因应为 ErrSymbolClaimed, got %v", owner, err)
			}
		}(owner)
	}
	wg.Wait()
	close(releases)

	if got := successCount.Load(); got != 1 {
		t.Fatalf("100 个并发 claim 应恰好 1 个成功, got %d", got)
	}

	// 释放后应能被下一个 owner 重新拿到。
	for release := range releases {
		release()
	}
	release2, err := m.TryAcquire("BTC-USDT", "intent-after-release", time.Now())
	if err != nil {
		t.Fatalf("释放后应能重新获取 claim: %v", err)
	}
	release2()
}

func TestClaimManagerDistinctSymbolsIndependent(t *testing.T) {
	m := NewClaimManager()
	relBTC, err := m.TryAcquire("BTC-USDT", "intent-btc", time.Now())
	if err != nil {
		t.Fatalf("BTC-USDT 应可获取: %v", err)
	}
	defer relBTC()
	relETH, err := m.TryAcquire("ETH-USDT", "intent-eth", time.Now())
	if err != nil {
		t.Fatalf("不同 symbol 的 claim 不应互相阻塞: %v", err)
	}
	defer relETH()
}

func TestClaimManagerSameOwnerReentryIsIdempotent(t *testing.T) {
	m := NewClaimManager()
	rel1, err := m.TryAcquire("BTC-USDT", "intent-1", time.Now())
	if err != nil {
		t.Fatalf("首次获取应成功: %v", err)
	}
	rel2, err := m.TryAcquire("BTC-USDT", "intent-1", time.Now())
	if err != nil {
		t.Fatalf("同 owner 重入应幂等成功(不应报 ErrSymbolClaimed): %v", err)
	}
	if _, err := m.TryAcquire("BTC-USDT", "intent-other", time.Now()); !errors.Is(err, ErrSymbolClaimed) {
		t.Fatalf("其他 owner 在持有期间应被拒绝, got %v", err)
	}
	// 重入返回的 release 与首次返回的 release 都对应同一持有者,调用任一个都会真正释放,
	// 之后 release 对该 symbol 的重复调用必须是安全 no-op(不会误释放后来者的 claim)。
	rel1()
	if _, held := m.Holder("BTC-USDT"); held {
		t.Fatal("release 后 claim 应已释放")
	}
	relNext, err := m.TryAcquire("BTC-USDT", "intent-next", time.Now())
	if err != nil {
		t.Fatalf("释放后新 owner 应能获取: %v", err)
	}
	rel2() // 对已不属于自己的旧 owner 记录再次调用,必须是安全 no-op,不能误释放 intent-next 的 claim
	if _, held := m.Holder("BTC-USDT"); !held {
		t.Fatal("过期的 release 不应误释放后来者持有的 claim")
	}
	relNext()
}

func TestClaimManagerQuarantineBlocksNewClaimButKeepsExistingHolder(t *testing.T) {
	m := NewClaimManager()
	release, err := m.TryAcquire("BTC-USDT", "intent-1", time.Now())
	if err != nil {
		t.Fatalf("首次获取应成功: %v", err)
	}
	m.Quarantine("BTC-USDT", "unknown_order", time.Now(), "ord-1")

	if _, held := m.Holder("BTC-USDT"); !held {
		t.Fatal("quarantine 不应释放既有 claim")
	}

	if _, err := m.TryAcquire("BTC-USDT", "intent-2", time.Now()); !errors.Is(err, ErrSymbolQuarantined) {
		t.Fatalf("隔离期间新 claim 应被拒绝, got %v", err)
	}

	release() // 既有 claim 释放后,symbol 仍处于隔离态
	if _, err := m.TryAcquire("BTC-USDT", "intent-3", time.Now()); !errors.Is(err, ErrSymbolQuarantined) {
		t.Fatalf("release 不应清除隔离态, got %v", err)
	}

	m.ClearQuarantine("BTC-USDT")
	rel3, err := m.TryAcquire("BTC-USDT", "intent-3", time.Now())
	if err != nil {
		t.Fatalf("显式清除隔离后应能重新获取 claim: %v", err)
	}
	rel3()
}

func TestClaimManagerQuarantinedSymbolsListing(t *testing.T) {
	m := NewClaimManager()
	m.Quarantine("BTC-USDT", "reason-a", time.Now(), "")
	m.Quarantine("ETH-USDT", "reason-b", time.Now(), "")
	list := m.QuarantinedSymbols()
	if len(list) != 2 {
		t.Fatalf("应有 2 个隔离 symbol, got %d", len(list))
	}
}
