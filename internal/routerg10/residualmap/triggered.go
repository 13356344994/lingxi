package residualmap

import (
	"fmt"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
)

const flatDirection = "flat"

// CollectTriggeredEventWorkItems enumerates all feature-only triggered events from
// frozen G6 event-map thresholds over the full primary-file universe. It does not
// seed from G6 discovery winners.
func CollectTriggeredEventWorkItems(
	bundles []reverseevent.MapBundle,
	catalog historydata.HistoryCatalog,
	historyRoot string,
	maxFiles, maxLines int,
) ([]EventWorkItem, error) {
	return CollectTriggeredEventWorkItemsForSymbols(
		bundles, catalog, historyRoot, maxFiles, maxLines, nil,
	)
}

// CollectTriggeredEventWorkItemsForSymbols scans only symbols that can pass an
// outcome-blind upstream eligibility gate. A nil set preserves the full scan.
func CollectTriggeredEventWorkItemsForSymbols(
	bundles []reverseevent.MapBundle,
	catalog historydata.HistoryCatalog,
	historyRoot string,
	maxFiles, maxLines int,
	eligibleSymbols map[string]bool,
) ([]EventWorkItem, error) {
	seen := map[string]bool{}
	var items []EventWorkItem
	for _, bundle := range bundles {
		spec, err := eventmap.ProfileSpecFor(bundle.Manifest.ProfileID)
		if err != nil {
			return nil, err
		}
		selection, err := eventmap.SelectPrimaryFiles(catalog, bundle.Manifest.ProfileID, maxFiles, bundle.Manifest.CatalogSymbolCount)
		if err != nil {
			return nil, err
		}
		btcRel := spec.PrimaryFamily + "/BTC-USDT-SWAP.jsonl"
		btcBars, err := eventmap.LoadBars(eventmap.CatalogFilePath(historyRoot, btcRel), maxLines)
		if err != nil {
			return nil, fmt.Errorf("load BTC bars for %s: %w", bundle.Manifest.ProfileID, err)
		}
		boundaries, err := eventmap.ComputeBoundaries(spec, btcBars)
		if err != nil {
			return nil, err
		}
		shockIndex := eventmap.BuildShockIndex(btcBars, spec, bundle.Thresholds)
		windowBars := eventmap.RollingWindowBars(spec)
		tracker := eventmap.NewWindowTracker()
		for _, file := range selection.Files {
			if eligibleSymbols != nil && !eligibleSymbols[normalizeSymbol(file.Symbol)] {
				continue
			}
			bars, err := eventmap.LoadBars(eventmap.CatalogFilePath(historyRoot, file.Path), maxLines)
			if err != nil {
				continue
			}
			funding, _ := eventmap.LoadSymbolFunding(historyRoot, file.Symbol, maxLines)
			cfg := eventmap.ScanSymbolConfig{
				Spec:           spec,
				Boundaries:     boundaries,
				Thresholds:     bundle.Thresholds,
				BTCBars:        btcBars,
				ShockClusterAt: shockIndex.Lookup,
				Funding:        funding,
				WindowBars:     windowBars,
			}
			_, err = eventmap.StreamSymbol(bars, file.Symbol, cfg, tracker, func(out eventmap.EventOutcome) error {
				if seen[out.EventID] {
					return nil
				}
				seen[out.EventID] = true
				items = append(items, EventWorkItem{
					ProfileID:    bundle.Manifest.ProfileID,
					ManifestHash: bundle.Manifest.ManifestHash,
					ShockCluster: out.ShockCluster,
					Event: TriggeredEventEntry{
						EventID:        out.EventID,
						Symbol:         out.Symbol,
						DecisionTime:   out.DecisionTime,
						EventFamily:    out.EventFamily,
						DecisionBarIdx: findDecisionBarIndex(bars, out.DecisionTime, spec.BarDuration),
					},
				})
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	return items, nil
}

func findDecisionBarIndex(bars []eventmap.Bar, decision time.Time, barDuration time.Duration) int {
	want := decision.UTC()
	for i := range bars {
		closeTime := bars[i].OpenTime.Add(barDuration)
		if closeTime.Equal(want) {
			return i
		}
	}
	return -1
}

// EvaluateTriggeredEvent replays one triggered event against all long/short/flat outcomes.
func EvaluateTriggeredEvent(
	item EventWorkItem,
	bars []eventmap.Bar,
	spec eventmap.ProfileSpec,
	funding []profitmap.FundingPoint,
	fp fingerprint.PersonalExecutionFingerprint,
) (EvaluationResult, error) {
	idx := item.Event.DecisionBarIdx
	if idx < 0 {
		idx = findDecisionBar(bars, item.Event.DecisionTime, spec.BarDuration)
	}
	if idx < 0 {
		return EvaluationResult{
			EventID:       item.Event.EventID,
			RejectReasons: []string{RejectBarReplayMiss},
		}, nil
	}
	event := eventmap.DetectedEvent{
		ID:             item.Event.EventID,
		Symbol:         item.Event.Symbol,
		Family:         item.Event.EventFamily,
		DecisionTime:   item.Event.DecisionTime,
		DecisionBarIdx: idx,
		ShockCluster:   item.ShockCluster,
	}
	outcomes := eventmap.ExpandOutcomes(event, bars, spec, funding)
	if len(outcomes) == 0 {
		return EvaluationResult{
			EventID:       item.Event.EventID,
			RejectReasons: []string{RejectBarReplayMiss},
		}, nil
	}
	actualEquity := ActualEquityUSDT(fp)
	best := directionEvaluation{}
	bestSet := false
	flatSeen := false
	type comboKey struct {
		horizon string
		delay   int
	}
	byCombo := map[comboKey]map[string]*eventmap.EventOutcome{}
	for i := range outcomes {
		out := outcomes[i]
		key := comboKey{horizon: out.Horizon, delay: out.DelayBars}
		if byCombo[key] == nil {
			byCombo[key] = map[string]*eventmap.EventOutcome{}
		}
		copy := out
		byCombo[key][out.Direction] = &copy
	}
	for key, dirs := range byCombo {
		for _, direction := range []string{"long", "short", flatDirection} {
			var gross float64
			delay := key.delay
			switch direction {
			case flatDirection:
				gross = 0
				flatSeen = true
			default:
				out, ok := dirs[direction]
				if !ok {
					continue
				}
				gross = out.GrossBPS
				delay = out.DelayBars
			}
			eval := evaluateDirectionGross(item, idx, key.horizon, delay, direction, gross, bars, spec, funding, fp, actualEquity)
			if direction == flatDirection {
				continue
			}
			if eval.adj.CriticalUnknown {
				continue
			}
			if !bestSet || eval.userTierConservative > best.userTierConservative {
				best = eval
				bestSet = true
			}
		}
	}
	if !flatSeen {
		_ = evaluateDirectionGross(item, idx, spec.Horizons[0].Label, 0, flatDirection, 0, bars, spec, funding, fp, actualEquity)
	}
	if !bestSet || best.direction == "" {
		return EvaluationResult{
			EventID:       item.Event.EventID,
			RejectReasons: []string{RejectBarReplayMiss},
		}, nil
	}
	result := ClassifyResidual(best.tiers, item, best.adj)
	result.EventID = item.Event.EventID
	result.Direction = best.direction
	result.Horizon = best.horizon
	result.DelayBars = best.delayBars
	return result, nil
}

type directionEvaluation struct {
	direction            string
	horizon              string
	delayBars            int
	userTierConservative float64
	tiers                map[string]TierCostBreakdown
	adj                  PersonalAdjustments
}

func evaluateDirectionGross(
	item EventWorkItem,
	idx int,
	horizon string,
	delayBars int,
	direction string,
	gross float64,
	bars []eventmap.Bar,
	spec eventmap.ProfileSpec,
	funding []profitmap.FundingPoint,
	fp fingerprint.PersonalExecutionFingerprint,
	actualEquity float64,
) directionEvaluation {
	decisionBar := bars[idx]
	windowBars := eventmap.RollingWindowBars(spec)
	entryIndex := idx + 1 + delayBars
	exitIndex := entryIndex
	for _, h := range spec.Horizons {
		if h.Label == horizon {
			exitIndex = entryIndex + h.BarCount
			break
		}
	}
	var entryTime, exitTime time.Time
	if entryIndex < len(bars) {
		entryTime = bars[entryIndex].OpenTime
	}
	if exitIndex < len(bars) {
		exitTime = bars[exitIndex].OpenTime
	}
	base := CostBase{
		RollingQuoteVolumeUSDT: profitmap.RollingQuoteVolumeUSDT(bars, idx, windowBars),
		BarHigh:                decisionBar.High,
		BarLow:                 decisionBar.Low,
		BarMid:                 decisionBar.Mid(),
		FundingBPS:             profitmap.FundingBPSOverHold(funding, entryTime, exitTime),
		ProfileID:              item.ProfileID,
		Horizon:                horizon,
		DelayBars:              delayBars,
		HoldHours:              horizonHoldHours(spec, horizon),
	}
	lookupDir := direction
	if lookupDir == flatDirection {
		lookupDir = "long"
	}
	adj := LookupPersonalAdjustments(fp, item.Event.Symbol, lookupDir)
	tiers := ComputeTierCosts(gross, base, adj, actualEquity)
	userBest := bestUserTierConservative(tiers)
	return directionEvaluation{
		direction:            direction,
		horizon:              horizon,
		delayBars:            delayBars,
		userTierConservative: userBest,
		tiers:                tiers,
		adj:                  adj,
	}
}
