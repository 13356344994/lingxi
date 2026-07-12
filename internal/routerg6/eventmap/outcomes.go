package eventmap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"

	g4protocol "lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/protocol"
)

// OptionalContext holds nonblocking optional feature values at decision time.
type OptionalContext struct {
	Values  map[string]float64
	Missing []string
}

// LoadOptionalContext loads declared optional families; missing never blocks events.
func LoadOptionalContext(historyRoot, instrument, profileID string, maxLines int) (OptionalContext, error) {
	out := OptionalContext{Values: map[string]float64{}}
	families := optionalFamiliesForProfile(profileID)
	for _, family := range families {
		path := filepath.Join(historyRoot, family, instrument+".jsonl")
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				out.Missing = append(out.Missing, family)
				continue
			}
			return OptionalContext{}, err
		}
		value, ok, err := loadOptionalScalar(path, maxLines)
		if err != nil {
			return OptionalContext{}, fmt.Errorf("optional %s: %w", family, err)
		}
		if !ok {
			out.Missing = append(out.Missing, family)
			continue
		}
		out.Values[family] = value
	}
	return out, nil
}

func optionalFamiliesForProfile(profileID string) []string {
	switch profileID {
	case ProfileOKX1H:
		return []string{"okx_funding", "bybit_funding", "rubik_taker_1h", "rubik_lsratio_1h"}
	case ProfileOKX5M:
		return []string{"okx_funding", "bybit_funding", "rubik_taker_5m"}
	default:
		return nil
	}
}

func loadOptionalScalar(path string, maxLines int) (float64, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var last float64
	found := false
	lines := 0
	for scanner.Scan() && (maxLines <= 0 || lines < maxLines) {
		lines++
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return 0, false, err
		}
		v, ok := extractOptionalValue(row)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		last = v
		found = true
	}
	if err := scanner.Err(); err != nil {
		return 0, false, err
	}
	return last, found, nil
}

func extractOptionalValue(row map[string]any) (float64, bool) {
	if data, ok := row["data"].(map[string]any); ok {
		for _, key := range []string{"fundingRate", "takerBuyRatio", "longShortRatio", "ratio", "value"} {
			if raw, ok := data[key]; ok {
				return parseFloatAny(raw)
			}
		}
	}
	for _, key := range []string{"value", "ratio", "taker_buy_ratio"} {
		if raw, ok := row[key]; ok {
			return parseFloatAny(raw)
		}
	}
	return 0, false
}

func parseFloatAny(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// ExpandOutcomes evaluates all long/short × horizons × delay × capital outcomes
// for one detected event using G4 conservative cost.
func ExpandOutcomes(event DetectedEvent, bars []Bar, spec ProfileSpec, funding []profitmap.FundingPoint) []EventOutcome {
	if event.DecisionBarIdx < 0 || event.DecisionBarIdx >= len(bars)-1 {
		return nil
	}
	windowBars := profitmap.RollingWindowBars(spec)
	decisionBar := bars[event.DecisionBarIdx]
	rollingVol := profitmap.RollingQuoteVolumeUSDT(bars, event.DecisionBarIdx, windowBars)

	var out []EventOutcome
	for _, delay := range protocol.DelayBars {
		entryIndex := event.DecisionBarIdx + 1 + delay
		if entryIndex >= len(bars) {
			continue
		}
		entryTime := bars[entryIndex].OpenTime
		entryPrice := bars[entryIndex].Open
		if entryPrice <= 0 {
			continue
		}
		if delay == 0 && !entryTime.Equal(event.DecisionTime) {
			continue
		}
		for _, horizon := range spec.Horizons {
			exitIndex := entryIndex + horizon.BarCount
			if exitIndex >= len(bars) {
				continue
			}
			exitTime := bars[exitIndex].OpenTime
			exitPrice := bars[exitIndex].Open
			if exitPrice <= 0 {
				continue
			}
			wantExit := entryTime.Add(horizon.Duration)
			if !exitTime.Equal(wantExit) {
				continue
			}
			fundingBPS := profitmap.FundingBPSOverHold(funding, entryTime, exitTime)
			costBase := profitmap.CostInputs{
				RollingQuoteVolumeUSDT: rollingVol,
				BarHigh:                decisionBar.High,
				BarLow:                 decisionBar.Low,
				BarMid:                 decisionBar.Mid(),
				FundingBPS:             fundingBPS,
			}
			for _, direction := range protocol.EventDirections {
				gross := profitmap.SignedGrossBPS(entryPrice, exitPrice, direction)
				for _, tier := range protocol.CapitalTiersUSDT {
					costIn := costBase
					costIn.CapitalUSDT = tier
					feeNet, conservative, capFail := profitmap.ComputeReturns(gross, costIn)
					wipeout := gross > g4protocol.NeutralBandBPS && conservative <= 0
					out = append(out, EventOutcome{
						EventID:         event.ID,
						Symbol:          event.Symbol,
						EventFamily:     event.Family,
						Partition:       event.Partition,
						DecisionTime:    event.DecisionTime,
						ShockCluster:    event.ShockCluster,
						SessionBucket:   event.SessionBucket,
						LiquidityBucket: event.LiquidityBucket,
						Horizon:         horizon.Label,
						Direction:       direction,
						DelayBars:       delay,
						CapitalTier:     int(tier),
						GrossBPS:        gross,
						FeeNetBPS:       feeNet,
						ConservativeBPS: conservative,
						CostWipeout:     wipeout,
						CapacityFail:    capFail,
						OptionalMissing: append([]string(nil), event.OptionalMissing...),
					})
				}
			}
		}
	}
	return out
}

// CooldownBarsForProfile returns the max event deadline span across one profile.
func CooldownBarsForProfile(spec ProfileSpec) int {
	max := 1
	for _, h := range spec.Horizons {
		if bars := eventDeadlineBars(h); bars > max {
			max = bars
		}
	}
	return max
}

// CooldownBarsForEvent returns non-overlap span: 1 entry bar + delay + horizon bars.
func CooldownBarsForEvent(spec ProfileSpec, horizon string, delayBars int) int {
	if delayBars < 0 {
		delayBars = 0
	}
	for _, h := range spec.Horizons {
		if h.Label == horizon {
			return 1 + delayBars + eventDeadlineBars(h)
		}
	}
	return CooldownBarsForProfile(spec)
}

func eventDeadlineBars(h profitmap.HorizonSpec) int {
	if h.BarCount < 1 {
		return 1
	}
	return h.BarCount
}

// DecisionUsesFutureData reports whether detection would read future bars.
func DecisionUsesFutureData(bars []Bar, idx int, spec ProfileSpec) bool {
	if idx < 0 || idx >= len(bars) {
		return true
	}
	decision := profitmap.DecisionTime(bars, idx, spec.BarDuration)
	for j := idx + 1; j < len(bars); j++ {
		if !profitmap.DecisionTime(bars, j, spec.BarDuration).After(decision) {
			continue
		}
		if bars[j].OpenTime.Before(decision) {
			return true
		}
	}
	return false
}

// EntryUsesNextOpen verifies delay-0 entry is next bar open after decision close.
func EntryUsesNextOpen(bars []Bar, decisionIdx int, spec ProfileSpec) bool {
	if decisionIdx+1 >= len(bars) {
		return false
	}
	decision := profitmap.DecisionTime(bars, decisionIdx, spec.BarDuration)
	return bars[decisionIdx+1].OpenTime.Equal(decision)
}

// ValidateEventID checks the frozen event_id formula.
func ValidateEventID(event DetectedEvent) bool {
	want := BuildEventID(event.Symbol, event.DecisionTime, event.Family)
	return event.ID == want
}
