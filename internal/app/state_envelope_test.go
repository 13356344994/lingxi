package app

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateEnvelopeStampThenClassifyOK(t *testing.T) {
	state := &persistedDailyRiskState{TradingDay: "2026-06-05", RealizedPnL: -1.25, OrderCount: 3}
	if err := stampStateEnvelope(state); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	if state.SchemaVersion != liveStateSchemaVersion {
		t.Fatalf("expected schema version %d, got %d", liveStateSchemaVersion, state.SchemaVersion)
	}
	if state.Checksum == "" {
		t.Fatal("checksum should be set after stamp")
	}
	status, err := classifyStateEnvelope(state)
	if err != nil {
		t.Fatalf("classify failed: %v", err)
	}
	if status != stateEnvelopeOK {
		t.Fatalf("freshly stamped state should classify OK, got %s", status)
	}
}

func TestStateEnvelopeStampDoesNotMutateChecksumDuringCompute(t *testing.T) {
	state := &persistedDailyRiskState{TradingDay: "2026-06-05", RealizedPnL: 7}
	if err := stampStateEnvelope(state); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	before := state.Checksum
	if _, err := classifyStateEnvelope(state); err != nil {
		t.Fatalf("classify failed: %v", err)
	}
	if state.Checksum != before {
		t.Fatalf("classify must restore checksum, before=%q after=%q", before, state.Checksum)
	}
}

func TestStateEnvelopeLegacyFile(t *testing.T) {
	// 旧文件：无 schema_version / checksum 字段。
	state := &persistedDailyRiskState{TradingDay: "2026-06-05", RealizedPnL: 1}
	status, err := classifyStateEnvelope(state)
	if err != nil {
		t.Fatalf("classify failed: %v", err)
	}
	if status != stateEnvelopeLegacy {
		t.Fatalf("unstamped state should classify legacy, got %s", status)
	}
}

func TestStateEnvelopeChecksumMismatch(t *testing.T) {
	state := &persistedDailyRiskState{TradingDay: "2026-06-05", RealizedPnL: 10}
	if err := stampStateEnvelope(state); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	// 模拟落盘后内容被篡改：改了字段但没重算校验和。
	state.RealizedPnL = 999
	status, err := classifyStateEnvelope(state)
	if err != nil {
		t.Fatalf("classify failed: %v", err)
	}
	if status != stateEnvelopeChecksumMismatch {
		t.Fatalf("tampered content should classify checksum_mismatch, got %s", status)
	}
}

func TestStateEnvelopeVersionAhead(t *testing.T) {
	state := &persistedDailyRiskState{TradingDay: "2026-06-05"}
	if err := stampStateEnvelope(state); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	state.SchemaVersion = liveStateSchemaVersion + 1
	status, err := classifyStateEnvelope(state)
	if err != nil {
		t.Fatalf("classify failed: %v", err)
	}
	if status != stateEnvelopeVersionAhead {
		t.Fatalf("future-version file should classify version_ahead, got %s", status)
	}
}

// TestStateEnvelopeSchemaOlder 钉死良性 schema 演进:文件版本低于本进程(旧版本写出),
// 即便新序列化下 checksum 不一致,也应分级为 schema_older 并允许自动接管(不误判为损坏)。
func TestStateEnvelopeSchemaOlder(t *testing.T) {
	if liveStateSchemaVersion < 2 {
		t.Skip("需要 liveStateSchemaVersion >= 2 才能构造更旧版本")
	}
	state := &persistedDailyRiskState{TradingDay: "2026-06-05", RealizedPnL: -1.25}
	if err := stampStateEnvelope(state); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	// 模拟旧版本写出的文件:版本号更低,且内容(checksum)按旧序列化口径——这里直接降版本号即可触发不一致。
	state.SchemaVersion = liveStateSchemaVersion - 1
	status, err := classifyStateEnvelope(state)
	if err != nil {
		t.Fatalf("classify failed: %v", err)
	}
	if status != stateEnvelopeSchemaOlder {
		t.Fatalf("older-version file should classify schema_older, got %s", status)
	}
	if err := guardStateEnvelope("日级风控状态", state); err != nil {
		t.Fatalf("schema_older state should pass guard (benign evolution), got %v", err)
	}
}

func TestStateEnvelopeAutoRecoverable(t *testing.T) {
	cases := map[stateEnvelopeStatus]bool{
		stateEnvelopeLegacy:           true,
		stateEnvelopeOK:               true,
		stateEnvelopeChecksumMismatch: false,
		stateEnvelopeVersionAhead:     false,
		stateEnvelopeSchemaOlder:      true,
	}
	for status, want := range cases {
		if got := stateEnvelopeAutoRecoverable(status); got != want {
			t.Fatalf("autoRecoverable(%s)=%v, want %v", status, got, want)
		}
	}
}

func TestGuardStateEnvelopeAllowsLegacyAndOK(t *testing.T) {
	legacy := &persistedDailyRiskState{TradingDay: "2026-06-05"}
	if err := guardStateEnvelope("日级风控状态", legacy); err != nil {
		t.Fatalf("legacy state should pass guard, got %v", err)
	}
	ok := &persistedDailyRiskState{TradingDay: "2026-06-05", RealizedPnL: 3}
	if err := stampStateEnvelope(ok); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	if err := guardStateEnvelope("日级风控状态", ok); err != nil {
		t.Fatalf("ok state should pass guard, got %v", err)
	}
}

func TestGuardStateEnvelopeRefusesUntrusted(t *testing.T) {
	tampered := &persistedDailyRiskState{TradingDay: "2026-06-05", RealizedPnL: 1}
	if err := stampStateEnvelope(tampered); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	tampered.RealizedPnL = 999 // 改内容不改校验和
	if err := guardStateEnvelope("日级风控状态", tampered); err == nil {
		t.Fatal("checksum mismatch must be refused by guard")
	}

	ahead := &persistedDailyRiskState{TradingDay: "2026-06-05"}
	if err := stampStateEnvelope(ahead); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	ahead.SchemaVersion = liveStateSchemaVersion + 1
	if err := guardStateEnvelope("日级风控状态", ahead); err == nil {
		t.Fatal("version-ahead must be refused by guard")
	}
}

// TestStateEnvelopeRoundTripThroughJSON 验证「打戳→落盘 JSON→读回→校验」端到端 OK，
// 覆盖含 float 字段的开仓台账(序列化往返不得破坏校验和)。
func TestStateEnvelopeRoundTripThroughJSON(t *testing.T) {
	original := &persistedOpenHedgeState{
		UpdatedAt: "2026-06-05T00:00:00Z",
		RunID:     "run-1",
		Hedges: []persistedOpenHedgeEntry{
			{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC", HedgeQty: 0.001, OpenFuturesAvgPrice: 60000.5},
		},
	}
	if err := stampStateEnvelope(original); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	reloaded := &persistedOpenHedgeState{}
	if err := jsonUnmarshalNoNumberLoss(data, reloaded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	status, err := classifyStateEnvelope(reloaded)
	if err != nil {
		t.Fatalf("classify failed: %v", err)
	}
	if status != stateEnvelopeOK {
		t.Fatalf("round-tripped state should classify OK, got %s", status)
	}
}

// TestLoadDailyRiskStateRefusesTamperedSameDayFile 钉死 M-S1.2:当日的日级风控台账若内容被篡改
// (校验和不匹配),loadDailyRiskState 必须拒绝接管并阻断启动,绝不静默吞入不可信的风控预算。
func TestLoadDailyRiskStateRefusesTamperedSameDayFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, liveDailyRiskStateName)
	day := tradingDayUTC(time.Now().UTC())

	stamped := persistedDailyRiskState{TradingDay: day, RealizedPnL: -5, OrderCount: 4}
	if err := stampStateEnvelope(&stamped); err != nil {
		t.Fatalf("stamp failed: %v", err)
	}
	stamped.OrderCount = 999 // 篡改内容,不重算校验和
	data, err := json.Marshal(stamped)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	engine := &FundingRateSniperEngine{
		cfg:                DefaultEngineConfig(),
		logger:             log.New(io.Discard, "", 0),
		dailyRiskStatePath: path,
	}
	if err := engine.loadDailyRiskState(); err == nil {
		t.Fatal("tampered same-day daily risk file must block startup (loadDailyRiskState should error)")
	}
}
