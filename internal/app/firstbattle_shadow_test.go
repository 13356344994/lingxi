package app

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/mechanism"
)

func TestFirstBattleShadowMapsExhaustedLiquidationToLongThesis(t *testing.T) {
	now := time.Now().UTC()
	e := &OKXDirectionalEngine{
		venueName: "okx", recordDir: t.TempDir(), nowFn: func() time.Time { return now },
		logger: log.New(io.Discard, "", 0),
	}
	ev := &fastExecShadowEvent{
		RecordedAt: now, Venue: "okx", Symbol: "X-USDT", SnapshotID: "decision-x",
		Mid: 1, Bid: 0.999, Ask: 1.001, SpreadBps: 2,
		DepthNotional10bps: 1000, OrderbookImbalance: 0.8,
		MidImpulse15sBps: -5, MidImpulse60sBps: -100,
		CandidateSource: "key_casc_fade", LiqQtySum: 100, EventAgeMs: 100,
		MakerPlaceable: true,
	}
	recordFirstBattleShadow(e, ev)

	f, err := os.Open(filepath.Join(e.recordDir, firstBattleShadowFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("未写入首战机制影子")
	}
	var got firstBattleShadowRecord
	if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OOD.ForceFlat {
		t.Fatalf("完整且新鲜的已知机制不应 OOD: %+v", got.OOD)
	}
	found := false
	for _, verdict := range got.Verdicts {
		if verdict.Expert == "panic_liquidation" && verdict.Accepted {
			found = true
			if verdict.Thesis.Direction != mechanism.DirectionLong {
				t.Fatalf("多头强平竭尽应形成承接做多论点, got %s", verdict.Thesis.Direction)
			}
		}
	}
	if !found {
		t.Fatalf("恐慌强平专家未接单: %+v", got.Verdicts)
	}
}

func TestFirstBattleShadowUnknownSourceFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	e := &OKXDirectionalEngine{venueName: "okx", nowFn: func() time.Time { return now }}
	card := mechanismCardFromLegacyEvent(e, &fastExecShadowEvent{
		RecordedAt: now, SnapshotID: "unknown", Symbol: "X-USDT",
		Mid: 1, SpreadBps: 2, DepthNotional10bps: 1000, EventAgeMs: 10,
	})
	if card.Regime.Kind != mechanism.RegimeUnknown {
		t.Fatalf("未知旧来源不得猜测机制, got %s", card.Regime.Kind)
	}
}
