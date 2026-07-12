package executionpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

const SchemaVersionFixture = "lingxi.routerg10.execution-policy.fixture.v1"

// FixtureBundle seals execution-policy path fixtures.
type FixtureBundle struct {
	SchemaVersion string         `json:"schema_version"`
	Policies      []FrozenPolicy `json:"policies"`
	BundleHash    string         `json:"bundle_hash"`
}

// WriteFixtures materializes execution-policy fixtures for path tests.
func WriteFixtures(outputDir string) (FixtureBundle, error) {
	triggers := DefaultFixtureTriggers()
	policies := make([]FrozenPolicy, 0, len(triggers))
	for _, tr := range triggers {
		fp, err := FreezePolicy(tr, triggers)
		if err != nil {
			return FixtureBundle{}, err
		}
		policies = append(policies, fp)
	}
	bundle := FixtureBundle{
		SchemaVersion: SchemaVersionFixture,
		Policies:      policies,
	}
	hash, err := hashFixtureBundle(bundle)
	if err != nil {
		return FixtureBundle{}, err
	}
	bundle.BundleHash = hash
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return FixtureBundle{}, err
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return FixtureBundle{}, err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "execution_policy_fixtures.json"), raw, 0o644); err != nil {
		return FixtureBundle{}, err
	}
	return bundle, nil
}

func hashFixtureBundle(b FixtureBundle) (string, error) {
	payload := b
	payload.BundleHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// DefaultFixtureTriggers returns frozen scenarios covering maker bias and toxic fill.
func DefaultFixtureTriggers() []TriggerRecord {
	return []TriggerRecord{
		{
			TriggerID: "touch_not_fill_missed_profit", DecisionTime: parseTime("2026-07-01T10:00:00Z"),
			Symbol: "BTC-USDT", Side: "long", Price: 100000,
			MakerTouched: true, MakerFilled: false, SubsequentlyProfitable: true,
			NetPnLUSDT: 0, RemainingEdgeBPS: 25, SpreadBPS: 3, FeeBPS: 2, LatencyMS: 45,
			MaxPriceCap: 100500, MaxSlippageBPS: 10, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
		},
		{
			TriggerID: "filled_unprofitable", DecisionTime: parseTime("2026-07-01T10:15:00Z"),
			Symbol: "BTC-USDT", Side: "long", Price: 100000,
			MakerFilled: true, FillWaitMS: 80, NetPnLUSDT: -2.5,
			RemainingEdgeBPS: 10, SpreadBPS: 3, FeeBPS: 2, LatencyMS: 40,
			MaxPriceCap: 100500, MaxSlippageBPS: 10, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
		},
		{
			TriggerID: "filled_profitable_slow", DecisionTime: parseTime("2026-07-01T10:30:00Z"),
			Symbol: "ETH-USDT", Side: "short", Price: 3500,
			MakerFilled: true, FillWaitMS: 800, NetPnLUSDT: 3.0,
			RemainingEdgeBPS: 30, SpreadBPS: 4, FeeBPS: 2, LatencyMS: 60,
			MaxPriceCap: 3520, MaxSlippageBPS: 12, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
		},
		{
			TriggerID: "unfilled_not_profitable", DecisionTime: parseTime("2026-07-01T10:45:00Z"),
			Symbol: "ETH-USDT", Side: "short", Price: 3500,
			MakerTouched: true, MakerFilled: false, SubsequentlyProfitable: false,
			RemainingEdgeBPS: 8, SpreadBPS: 4, FeeBPS: 2, LatencyMS: 55,
			MaxPriceCap: 3520, MaxSlippageBPS: 12, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
		},
		{
			TriggerID: "toxic_fast_fill_exit", DecisionTime: parseTime("2026-07-01T11:00:00Z"),
			Symbol: "BTC-USDT", Side: "long", Price: 100000,
			MakerFilled: true, FillWaitMS: 50, NetPnLUSDT: -5,
			ToxicFastFullFill: true, ToxicBookSyncBad: true, ThesisStillValid: false,
			RemainingEdgeBPS: 15, SpreadBPS: 5, FeeBPS: 2, LatencyMS: 30,
			MaxPriceCap: 100500, MaxSlippageBPS: 10, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
		},
	}
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// WriteTriggerFixtures writes triggers JSONL for runner tests.
func WriteTriggerFixtures(path string) error {
	triggers := DefaultFixtureTriggers()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, tr := range triggers {
		if err := enc.Encode(tr); err != nil {
			return err
		}
	}
	return nil
}
