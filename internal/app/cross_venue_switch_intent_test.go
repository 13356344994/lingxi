package app

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testSwitchIntentExecutor(t *testing.T) *CrossVenueExecutor {
	t.Helper()
	return &CrossVenueExecutor{recordDir: t.TempDir()}
}

func TestSwitchIntentsConcurrentUpsertNoLostUpdates(t *testing.T) {
	e := testSwitchIntentExecutor(t)
	now := time.Now().UTC()

	const n = 24
	var wg sync.WaitGroup
	wg.Add(n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			err := e.upsertSwitchIntent(crossVenueSwitchIntent{
				Symbol:             fmt.Sprintf("SYM-%02d", i),
				TargetHighVenue:    "okx",
				TargetLowVenue:     "gate",
				PartialSwitchPhase: "pending",
				CreatedAt:          now,
				ExpiresAt:          now.Add(30 * time.Minute),
			})
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	intents, err := e.loadSwitchIntents()
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != n {
		t.Fatalf("expected %d intents after concurrent upsert, got %d (possible lost update)", n, len(intents))
	}
}

func TestSwitchIntentsConcurrentReadModifyWrite(t *testing.T) {
	e := testSwitchIntentExecutor(t)
	now := time.Now().UTC()
	seed := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		TargetHighVenue:    "bybit",
		TargetLowVenue:     "okx",
		PartialSwitchPhase: "pending",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(seed); err != nil {
		t.Fatal(err)
	}

	phases := []string{
		"open_new_first_leg",
		"open_new_second_leg",
		"close_old",
		"open_new_retry",
		"rollback_new",
		"consumed",
	}
	const rounds = 40
	var wg sync.WaitGroup
	wg.Add(rounds * 3)
	for i := 0; i < rounds; i++ {
		phase := phases[i%len(phases)]
		go func(p string) {
			defer wg.Done()
			_ = e.updateSwitchIntentPhase(seed.Symbol, seed.TargetHighVenue, seed.TargetLowVenue, p)
		}(phase)
		go func(p string) {
			defer wg.Done()
			_ = e.updateSwitchIntentAttempt(seed.Symbol, seed.TargetHighVenue, seed.TargetLowVenue, p, "concurrent reconcile")
		}(phase)
		go func() {
			defer wg.Done()
			_ = e.activeSwitchIntents(now.Add(time.Minute))
		}()
	}
	wg.Wait()

	intents, err := e.loadSwitchIntents()
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 {
		t.Fatalf("expected exactly one intent after concurrent RMW, got %d", len(intents))
	}
	if intents[0].Symbol != seed.Symbol {
		t.Fatalf("unexpected intent symbol: %+v", intents[0])
	}
}

func TestSwitchIntentsConcurrentSwitchAndReconcile(t *testing.T) {
	e := testSwitchIntentExecutor(t)
	now := time.Now().UTC()

	const workers = 16
	const symbols = 8
	var wg sync.WaitGroup
	var upserts atomic.Int32

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				symbol := fmt.Sprintf("PAIR-%d", (w+i)%symbols)
				intent := crossVenueSwitchIntent{
					Symbol:             symbol,
					TargetHighVenue:    "gate",
					TargetLowVenue:     "okx",
					PartialSwitchPhase: "open_new",
					CreatedAt:          now,
					ExpiresAt:          now.Add(30 * time.Minute),
				}
				if err := e.upsertSwitchIntent(intent); err != nil {
					t.Error(err)
					return
				}
				upserts.Add(1)
				_ = e.updateSwitchIntentAttempt(symbol, "gate", "okx", "open_new_second_leg", "switch leg")
				_ = e.activeSwitchIntents(now)
				if (w+i)%5 == 0 {
					_ = e.removeSwitchIntent(symbol, "gate", "okx")
				}
			}
		}()
	}
	wg.Wait()

	intents, err := e.loadSwitchIntents()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{}, len(intents))
	for _, intent := range intents {
		key := intent.Symbol + "|" + intent.TargetHighVenue + "|" + intent.TargetLowVenue
		if _, dup := seen[key]; dup {
			t.Fatalf("duplicate switch intent key %q after concurrent ops", key)
		}
		seen[key] = struct{}{}
	}
	if upserts.Load() == 0 {
		t.Fatal("expected concurrent upserts to run")
	}
}
