package reversemine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
)

// Runner orchestrates reverse mining across one or more profit-map outputs.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(attempts, candidates int)
	OnSymbolLoaded   func(profileID, symbol string, liveSymbolBars int)
	OnBTCLoaded      func(profileID, primaryFamily string)
	CatalogValidator func(historydata.HistoryCatalog) error
}

// RunResult holds sealed discovery outputs.
type RunResult struct {
	Candidates []Candidate
	Ledger     CandidateLedger
	Manifest   CandidateManifest
	Summary    MineSummary
}

// Run executes bounded reverse mining with optional checkpoint resume.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != protocol.HistoryDataHash {
		return RunResult{}, fmt.Errorf("reversemine: catalog data hash mismatch")
	}

	bundles := make([]MapBundle, 0, len(r.Config.MapDirs))
	mapManifestHashes := map[string]string{}
	mapArtifactHashes := map[string]string{}
	for _, dir := range r.Config.MapDirs {
		bundle, err := LoadMapBundle(dir)
		if err != nil {
			return RunResult{}, err
		}
		bundles = append(bundles, bundle)
		mapManifestHashes[bundle.Manifest.ProfileID] = bundle.Manifest.ManifestHash
		for name, hash := range bundle.Manifest.ArtifactHashes {
			mapArtifactHashes[bundle.Manifest.ProfileID+"/"+name] = hash
		}
	}
	if err := VerifyCatalogBinding(r.Catalog, r.Config.HistoryRoot, bundles, r.Config.MaxFiles, r.Config.MaxLines); err != nil {
		return RunResult{}, err
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}

	cells := CollectCells(bundles)
	winnerFeatures, winnerCount, err := r.buildWinnerFeatureSamples(ctx, bundles, cells)
	if err != nil {
		return RunResult{}, err
	}

	var resume *Checkpoint
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, r.Catalog.DataHash, mapManifestHashes); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return r.loadCompleted()
		}
		resume = &cp
	}

	candidates, ledger, err := Derive(cells, winnerFeatures, r.Config.CandidateLimit, resume)
	if err != nil {
		return RunResult{}, err
	}
	if r.OnProgress != nil {
		r.OnProgress(len(ledger.Entries), len(candidates))
	}

	coverage, coverageHash, err := BuildCoverage(cells, winnerFeatures, candidates)
	if err != nil {
		return RunResult{}, err
	}
	manifest, err := BuildCandidateManifest(
		r.Config, r.Catalog.DataHash, mapManifestHashes, mapArtifactHashes,
		candidates, ledger, coverage, coverageHash,
	)
	if err != nil {
		return RunResult{}, err
	}
	summary := BuildSummary(
		cells, candidates, ledger, manifest, coverage,
		winnerCount, r.Config.CandidateLimit,
	)

	if err := WriteOutputs(r.Config.OutputDir, candidates, ledger, manifest, summary); err != nil {
		return RunResult{}, err
	}

	cp := Checkpoint{
		Generation:         protocol.Generation,
		ProtocolHash:       protocol.RouterProtocolHash,
		HistoryDataHash:    protocol.HistoryDataHash,
		ConfigHash:         manifest.ConfigHash,
		CatalogHash:        r.Catalog.DataHash,
		MapManifestHashes:  mapManifestHashes,
		OutputDir:          r.Config.OutputDir,
		NextAttemptOrdinal: len(ledger.Entries) + 1,
		CandidateCount:     len(candidates),
		Candidates:         candidates,
		LedgerEntries:      ledger.Entries,
		CandidatesHash:     manifest.CandidatesHash,
		LedgerHash:         manifest.LedgerHash,
		Done:               true,
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return RunResult{Candidates: candidates, Ledger: ledger, Manifest: manifest, Summary: summary}, nil
}

func (r *Runner) loadCompleted() (RunResult, error) {
	manifest, err := LoadCandidateManifest(filepath.Join(r.Config.OutputDir, "candidate_manifest.json"))
	if err != nil {
		return RunResult{}, err
	}
	if err := VerifyOutputArtifacts(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, "candidates.json"))
	if err != nil {
		return RunResult{}, err
	}
	var candPayload struct {
		Candidates []Candidate `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &candPayload); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g4_candidate_ledger.json"))
	if err != nil {
		return RunResult{}, err
	}
	var ledger CandidateLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return RunResult{}, err
	}
	if err := ValidateLedger(ledger); err != nil {
		return RunResult{}, err
	}
	var summary MineSummary
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "reverse_mine_summary.json"))
	if err != nil {
		return RunResult{}, err
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return RunResult{}, err
	}
	if summary.CoverageHash == "" || summary.CoverageHash != manifest.CoverageHash {
		return RunResult{}, fmt.Errorf("reversemine: summary coverage hash mismatch")
	}
	return RunResult{
		Candidates: candPayload.Candidates,
		Ledger:     ledger,
		Manifest:   manifest,
		Summary:    summary,
	}, nil
}

func (r *Runner) buildWinnerFeatureSamples(
	ctx context.Context,
	bundles []MapBundle,
	cells []CellWorkItem,
) (WinnerFeatureMatrix, int, error) {
	specs := map[string]profitmap.ProfileSpec{}
	btcBars := map[string][]profitmap.Bar{}
	winnerCount := 0

	for _, bundle := range bundles {
		spec, err := profitmap.ProfileSpecFor(bundle.Manifest.ProfileID)
		if err != nil {
			return nil, 0, err
		}
		specs[bundle.Manifest.ProfileID] = spec
		btcRel := spec.PrimaryFamily + "/BTC-USDT-SWAP.jsonl"
		if _, ok := btcBars[bundle.Manifest.ProfileID]; !ok {
			bars, err := profitmap.LoadBars(profitmap.CatalogFilePath(r.Config.HistoryRoot, btcRel), r.Config.MaxLines)
			if err != nil {
				return nil, 0, err
			}
			btcBars[bundle.Manifest.ProfileID] = bars
			if r.OnBTCLoaded != nil {
				r.OnBTCLoaded(bundle.Manifest.ProfileID, spec.PrimaryFamily)
			}
		}
	}

	type winnerRequest struct {
		cell   CellWorkItem
		winner profitmap.WinnerEntry
	}
	requests := map[string]map[string][]winnerRequest{}
	for _, cell := range cells {
		for _, winner := range cell.Winners {
			winnerCount++
			if requests[cell.ProfileID] == nil {
				requests[cell.ProfileID] = map[string][]winnerRequest{}
			}
			requests[cell.ProfileID][winner.Symbol] = append(
				requests[cell.ProfileID][winner.Symbol],
				winnerRequest{cell: cell, winner: winner},
			)
		}
	}

	matrix := WinnerFeatureMatrix{}
	profileIDs := make([]string, 0, len(requests))
	for profileID := range requests {
		profileIDs = append(profileIDs, profileID)
	}
	sort.Strings(profileIDs)
	for _, profileID := range profileIDs {
		spec := specs[profileID]
		symbols := make([]string, 0, len(requests[profileID]))
		for symbol := range requests[profileID] {
			symbols = append(symbols, symbol)
		}
		sort.Strings(symbols)
		for symbolIndex, symbol := range symbols {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			default:
			}
			rel := spec.PrimaryFamily + "/" + symbol + ".jsonl"
			if err := profitmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
				return nil, 0, err
			}
			bars, err := profitmap.LoadBars(profitmap.CatalogFilePath(r.Config.HistoryRoot, rel), r.Config.MaxLines)
			if err != nil {
				return nil, 0, err
			}
			if r.OnSymbolLoaded != nil {
				r.OnSymbolLoaded(profileID, symbol, 1)
			}
			var optional optionalSeries
			if profileID == profitmap.ProfileOKX1H {
				optional, err = loadOptionalSeries(
					r.Catalog, r.Config.HistoryRoot, symbol, r.Config.MaxLines,
				)
				if err != nil {
					return nil, 0, err
				}
			}
			for _, request := range requests[profileID][symbol] {
				computed, err := ComputeFeatures(
					spec, bars, btcBars[profileID], request.winner.DecisionTime,
					optional.featuresAt(request.winner.DecisionTime),
				)
				if err != nil {
					continue
				}
				id := cellID(request.cell)
				if matrix[id] == nil {
					matrix[id] = map[string]map[string]float64{}
				}
				wid := winnerID(request.winner)
				if matrix[id][wid] == nil {
					matrix[id][wid] = map[string]float64{}
				}
				for key, value := range computed {
					if isFinite(value) {
						matrix[id][wid][key] = value
					}
				}
			}
			// bars and optional are intentionally scoped to one symbol and
			// become unreachable before the next symbol is loaded.
			if r.OnProgress != nil && (symbolIndex+1)%r.Config.ProgressEvery == 0 {
				r.OnProgress(symbolIndex+1, 0)
			}
		}
	}
	return matrix, winnerCount, nil
}

// BuildSummary constructs reverse_mine_summary.json payload.
func BuildSummary(
	cells []CellWorkItem,
	candidates []Candidate,
	ledger CandidateLedger,
	manifest CandidateManifest,
	coverage CoverageSummary,
	winnerSeedCount int,
	candidateLimit int,
) MineSummary {
	profiles := map[string]bool{}
	for _, cell := range cells {
		profiles[cell.ProfileID] = true
	}
	profileIDs := make([]string, 0, len(profiles))
	for id := range profiles {
		profileIDs = append(profileIDs, id)
	}
	sort.Strings(profileIDs)
	single, dual := 0, 0
	phenomena := map[string]bool{}
	capitalSets := map[string]map[int]bool{}
	subpaths := map[string]bool{}
	for _, c := range candidates {
		if len(c.Conditions) == 1 {
			single++
		} else if len(c.Conditions) == 2 {
			dual++
		}
		phenomena[c.PhenomenonID] = true
		if capitalSets[c.PhenomenonID] == nil {
			capitalSets[c.PhenomenonID] = map[int]bool{}
		}
		capitalSets[c.PhenomenonID][c.CellKey.CapitalTier] = true
		subpaths[c.PhenomenonID+"|"+c.CellKey.Liquidity+"|"+c.CellKey.SymbolClass] = true
	}
	capitalTiers := map[string][]int{}
	for phenomenonID, set := range capitalSets {
		for capital := range set {
			capitalTiers[phenomenonID] = append(capitalTiers[phenomenonID], capital)
		}
		sort.Ints(capitalTiers[phenomenonID])
	}
	return MineSummary{
		SchemaVersion:                SchemaVersionSummary,
		Generation:                   protocol.Generation,
		ProtocolHash:                 protocol.RouterProtocolHash,
		SearchSpaceID:                protocol.ReverseMineSearchSpaceID,
		DerivationAlgorithm:          protocol.DerivationAlgorithmVersion,
		ProfileIDs:                   profileIDs,
		DiscoveryCellCount:           len(cells),
		TotalCells:                   coverage.TotalCells,
		CoveredCells:                 coverage.CoveredCells,
		CoverageByProfile:            coverage.CoverageByProfile,
		CoverageByHorizon:            coverage.CoverageByHorizon,
		CoverageByDirection:          coverage.CoverageByDirection,
		CoverageByCapital:            coverage.CoverageByCapital,
		CoverageByPriority:           coverage.CoverageByPriority,
		CandidatesByFeature:          coverage.CandidatesByFeature,
		CandidatesByProfile:          coverage.CandidatesByProfile,
		CandidatesByHorizon:          coverage.CandidatesByHorizon,
		CandidatesByDirection:        coverage.CandidatesByDirection,
		CandidatesByCapital:          coverage.CandidatesByCapital,
		DeclaredDistinctFeatureNames: coverage.DeclaredDistinctFeatureNames,
		AvailableProfileFeatureSlots: coverage.AvailableProfileFeatureSlots,
		CoveredDistinctFeatureNames:  coverage.CoveredDistinctFeatureNames,
		MissingDeclaredFeatureNames:  coverage.MissingDeclaredFeatureNames,
		CoverageHash:                 manifest.CoverageHash,
		WinnerSeedCount:              winnerSeedCount,
		AttemptCount:                 len(ledger.Entries),
		ActualCandidateCount:         len(candidates),
		CandidateCount:               len(candidates),
		UniquePhenomenonCount:        len(phenomena),
		CapitalTiersByPhenomenon:     capitalTiers,
		SubpathCount:                 len(subpaths),
		CandidateLimit:               candidateLimit,
		SingleFeatureCount:           single,
		DualFeatureCount:             dual,
		LedgerStatusCounts:           ledgerStatusCounts(ledger.Entries),
		CandidatesHash:               manifest.CandidatesHash,
		LedgerHash:                   manifest.LedgerHash,
		CandidateManifestHash:        manifest.ManifestHash,
		RoundRobinDimensions:         RoundRobinDimensions(),
		ExamActions:                  protocol.ExamActions,
	}
}

// BuildCoverage summarizes unique discovery cells represented by candidates.
func BuildCoverage(
	cells []CellWorkItem,
	matrix WinnerFeatureMatrix,
	candidates []Candidate,
) (CoverageSummary, string, error) {
	cellByID := make(map[string]CellWorkItem, len(cells))
	for _, cell := range cells {
		cellByID[cellID(cell)] = cell
	}
	covered := map[string]bool{}
	summary := CoverageSummary{
		TotalCells:            len(cells),
		CoverageByProfile:     map[string]int{},
		CoverageByHorizon:     map[string]int{},
		CoverageByDirection:   map[string]int{},
		CoverageByCapital:     map[string]int{},
		CoverageByPriority:    map[string]int{},
		CandidatesByFeature:   map[string]int{},
		CandidatesByProfile:   map[string]int{},
		CandidatesByHorizon:   map[string]int{},
		CandidatesByDirection: map[string]int{},
		CandidatesByCapital:   map[string]int{},
	}
	availableByProfileSets := map[string]map[string]bool{}
	for _, cell := range cells {
		for _, winnerFeatures := range matrix[cellID(cell)] {
			for feature, value := range winnerFeatures {
				if isFinite(value) {
					if availableByProfileSets[cell.ProfileID] == nil {
						availableByProfileSets[cell.ProfileID] = map[string]bool{}
					}
					availableByProfileSets[cell.ProfileID][feature] = true
				}
			}
		}
	}
	coveredDistinct := map[string]bool{}
	for _, candidate := range candidates {
		summary.CandidatesByProfile[candidate.ProfileID]++
		summary.CandidatesByHorizon[candidate.CellKey.Horizon]++
		summary.CandidatesByDirection[candidate.CellKey.Direction]++
		summary.CandidatesByCapital[fmt.Sprint(candidate.CellKey.CapitalTier)]++
		for _, condition := range candidate.Conditions {
			summary.CandidatesByFeature[condition.FeatureKey]++
			coveredDistinct[condition.FeatureKey] = true
		}
		probe := CellWorkItem{ProfileID: candidate.ProfileID, Key: candidate.CellKey}
		id := cellID(probe)
		if covered[id] {
			continue
		}
		cell, ok := cellByID[id]
		if !ok {
			continue
		}
		covered[id] = true
		summary.CoveredCells++
		summary.CoverageByProfile[cell.ProfileID]++
		summary.CoverageByHorizon[cell.Key.Horizon]++
		summary.CoverageByDirection[cell.Key.Direction]++
		summary.CoverageByCapital[fmt.Sprint(cell.Key.CapitalTier)]++
		summary.CoverageByPriority[fmt.Sprint(cell.PriorityTier)]++
	}
	availableByProfile := map[string][]string{}
	for profile, set := range availableByProfileSets {
		for feature := range set {
			availableByProfile[profile] = append(availableByProfile[profile], feature)
		}
	}
	var coveredNames []string
	for feature := range coveredDistinct {
		coveredNames = append(coveredNames, feature)
	}
	featureSummary := protocol.BuildFeatureCatalogSummary(availableByProfile, coveredNames)
	summary.DeclaredDistinctFeatureNames = featureSummary.DeclaredDistinctFeatureNames
	summary.AvailableProfileFeatureSlots = featureSummary.AvailableProfileFeatureSlots
	summary.CoveredDistinctFeatureNames = featureSummary.CoveredFeatureNames
	summary.MissingDeclaredFeatureNames = featureSummary.MissingDeclaredFeatureNames
	hash, _, err := datatruth.HashJSON(summary)
	return summary, hash, err
}
