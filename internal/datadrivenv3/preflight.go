// Package datadrivenv3 implements the outcome-blind V3 dataset admission gate.
package datadrivenv3

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datadrivenv3/fundingarchive"
	"lingxi/internal/datatruth"
)

const (
	ProfileOKX1H = "okx_1h"
	MinCostKnown = .95
)

type ProtocolBinding struct {
	Path, Field, Expected string
}

var ProtocolChain = []ProtocolBinding{
	{"research/firstbattle/data_driven_router_v3/protocol.json", "protocol_hash", "5d80166eb28307d23e33cd19efae2b60811852ff0da49a3a6e16663aba2e7ddd"},
	{"research/firstbattle/data_driven_router_v3/protocol_g2.json", "overlay_hash", "7096395f1f3bae212b6dc866c5d46ccb372cd284e4a959d424dfbfbb92d6c226"},
	{"research/firstbattle/data_driven_router_v3/protocol_g3.json", "overlay_hash", "63f4ee088efb09e9e58386ab273b997af4a9206d4c5c18e73c59fda5d42a7a57"},
	{"research/firstbattle/data_driven_router_v3/protocol_g4.json", "overlay_hash", "2fd8c5852bba5023c9024d46b5b5e9527e71d670c370a1276e4728f4ef4cd2b9"},
	{"research/firstbattle/data_driven_router_v3/protocol_g5.json", "overlay_hash", "518d0003c829da641f1bcc955c4382023e158ba9938c0ac6f0ff19b5bf729385"},
}

type Coverage struct {
	Profile               string           `json:"profile"`
	Partition             string           `json:"partition"`
	HorizonHours          int              `json:"horizon_hours"`
	DelayBars             int              `json:"delay_bars"`
	Eligible              int64            `json:"eligible_timestamps"`
	PrimaryCovered        int64            `json:"primary_feature_coverage"`
	ProxyInputs           int64            `json:"frozen_proxy_required_inputs"`
	FundingCrossing       int64            `json:"funding_crossing_timestamps"`
	FundingKnown          int64            `json:"signed_funding_known_coverage"`
	DirectCost            int64            `json:"direct_cost_coverage"`
	KnownCost             int64            `json:"proxy_or_direct_cost_coverage"`
	OptionalMissing       map[string]int64 `json:"optional_missing"`
	Days                  int              `json:"independent_days"`
	BTCRegimeBlocks       int              `json:"btc_regime_blocks"`
	MMRAvailable          bool             `json:"mmr_available"`
	MMRWithdrawalDeferred bool             `json:"mmr_unknown_withdrawal_deferred"`
}

func (c Coverage) KnownRatio() float64 {
	if c.Eligible == 0 {
		return 0
	}
	return float64(c.KnownCost) / float64(c.Eligible)
}

type Fitness struct {
	SchemaVersion         string            `json:"schema_version"`
	LabelsUnblinded       bool              `json:"labels_unblinded"`
	Status                string            `json:"status"`
	HighestBlocker        string            `json:"highest_blocker,omitempty"`
	ProtocolHashes        map[string]string `json:"protocol_hashes"`
	FeatureSchemaHash     string            `json:"feature_schema_hash"`
	IdentitySchema        map[string]any    `json:"identity_schema"`
	PartitionBoundaries   map[string]string `json:"partition_boundaries"`
	PurgeHours            int               `json:"purge_embargo_hours"`
	Coverage              []Coverage        `json:"coverage"`
	DecisionJournal       []string          `json:"decision_journal"`
	CertificationEvidence string            `json:"certification_evidence"`
	FundingManifestHash   string            `json:"funding_manifest_hash"`
	FundingReceiptHash    string            `json:"funding_receipt_hash"`
}

type bar struct {
	Time                           int64
	Open, High, Low, Close, Volume float64
}

func VerifyProtocolChain(root string) (map[string]string, error) {
	got := make(map[string]string, len(ProtocolChain))
	for _, b := range ProtocolChain {
		raw, err := os.ReadFile(filepath.Join(root, b.Path))
		if err != nil {
			return nil, err
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", b.Path, err)
		}
		hashes, ok := doc["hashes"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: missing hashes", b.Path)
		}
		declared, _ := hashes[b.Field].(string)
		hashes[b.Field] = nil
		hash, _, err := datatruth.HashJSON(doc)
		if err != nil {
			return nil, err
		}
		if declared != b.Expected || hash != b.Expected {
			return nil, fmt.Errorf("protocol drift %s: declared=%s computed=%s expected=%s", b.Path, declared, hash, b.Expected)
		}
		got[b.Path] = hash
	}
	return got, nil
}

func FeatureSchemaHash() (string, error) {
	hash, _, err := datatruth.HashJSON(map[string]any{
		"profile": ProfileOKX1H, "lookback_hours": 24, "state_schema": StateSchemaVersionV2,
		"feature_order": FeatureOrder, "feature_values_type": fmt.Sprintf("[%d]float64", FeatureCount),
		"missing_mask":             "uint64_bit_index_equals_feature_index",
		"identity_not_model_input": []string{"event_id", "instrument", "source_id", "source_version"},
	})
	return hash, err
}

func EventID(profile, venue, instrument string, decision time.Time, sourceID, sourceVersion string) string {
	return datatruth.HashStrings(profile, venue, instrument, decision.UTC().Format(time.RFC3339Nano), sourceID, sourceVersion)
}

func Partition(t time.Time, bounds [3]time.Time) string {
	if t.Before(bounds[0]) {
		return "discovery"
	}
	if t.Before(bounds[1]) {
		return "validation"
	}
	if t.Before(bounds[2]) {
		return "leaf_wf"
	}
	return "router_holdout"
}

func Purged(t time.Time, bounds [3]time.Time) bool {
	for _, b := range bounds {
		if d := t.Sub(b); d >= -24*time.Hour && d <= 24*time.Hour {
			return true
		}
	}
	return false
}

func Measure(root string) (Fitness, error) {
	hashes, err := VerifyProtocolChain(root)
	if err != nil {
		return Fitness{}, err
	}
	schema, err := FeatureSchemaHash()
	if err != nil {
		return Fitness{}, err
	}
	priceFiles, err := filepath.Glob(filepath.Join(root, "records/history/okx_1h/*.jsonl"))
	if err != nil {
		return Fitness{}, err
	}
	if len(priceFiles) == 0 {
		return Fitness{}, fmt.Errorf("no okx_1h files")
	}
	// The v3 source is authoritative. The older daily archive is intentionally
	// not opened: it is a duplicate historical artifact, not corroboration.
	archiveFunding, fundingProvenance, err := fundingarchive.LoadVerifiedSource(root)
	if err != nil {
		return Fitness{}, err
	}
	if fundingProvenance.ManifestHash == "" || fundingProvenance.ReceiptHash == "" {
		return Fitness{}, errors.New("verified funding v3 manifest/receipt unavailable")
	}
	allTimes := map[int64]struct{}{}
	for _, f := range priceFiles {
		bs, err := readBars(f)
		if err != nil {
			return Fitness{}, err
		}
		for _, b := range bs {
			allTimes[b.Time] = struct{}{}
		}
	}
	times := make([]int64, 0, len(allTimes))
	for t := range allTimes {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if len(times) < 4 {
		return Fitness{}, fmt.Errorf("insufficient common calendar")
	}
	bounds := [3]time.Time{time.UnixMilli(times[len(times)*500/1000]).UTC(), time.UnixMilli(times[len(times)*700/1000]).UTC(), time.UnixMilli(times[len(times)*850/1000]).UTC()}
	btcBars, err := readBars(filepath.Join(root, "records/history/okx_1h/BTC-USDT-SWAP.jsonl"))
	if err != nil {
		return Fitness{}, err
	}
	btcBlocks := countBTCRegimeBlocks(btcBars, bounds)
	byKey := map[string]*Coverage{}
	for _, f := range priceFiles {
		symbol := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		rubikSymbol := strings.TrimSuffix(symbol, "-USDT-SWAP")
		bs, err := readBars(f)
		if err != nil {
			return Fitness{}, err
		}
		taker, err := readOptional(filepath.Join(root, "records/history/rubik_taker_1h", rubikSymbol+".jsonl"))
		if err != nil {
			return Fitness{}, err
		}
		lsRatio, err := readOptional(filepath.Join(root, "records/history/rubik_lsratio_1h", rubikSymbol+".jsonl"))
		if err != nil {
			return Fitness{}, err
		}
		badPrefix := qualityPrefix(bs)
		funding := archiveFunding[symbol]
		fundingTimes := make([]int64, 0, len(funding))
		for eventTime := range funding {
			fundingTimes = append(fundingTimes, eventTime)
		}
		sort.Slice(fundingTimes, func(i, j int) bool { return fundingTimes[i] < fundingTimes[j] })
		for i := 24; i < len(bs); i++ {
			for _, h := range []int{1, 4, 24} {
				for _, d := range []int{0, 1, 2, 3} {
					exit := i + d + h
					if exit >= len(bs) || Purged(time.UnixMilli(bs[i].Time).UTC(), bounds) {
						continue
					}
					p := Partition(time.UnixMilli(bs[i].Time).UTC(), bounds)
					key := fmt.Sprintf("%s/%d/%d", p, h, d)
					c := byKey[key]
					if c == nil {
						c = &Coverage{Profile: ProfileOKX1H, Partition: p, HorizonHours: h, DelayBars: d, OptionalMissing: map[string]int64{}}
						byKey[key] = c
					}
					c.Eligible++
					entry := i + d
					complete := completeRange(badPrefix, i-24, exit)
					if complete {
						c.PrimaryCovered++
						c.ProxyInputs++
					}
					crossesFunding := fundingCrosses(fundingTimes, bs[entry].Time, bs[exit].Time)
					// Complete UTC daily source coverage makes every actual
					// settlement in this interval knowable; settlement cadence
					// is not assumed.
					fundingKnown := completeUTCDays(fundingProvenance, bs[entry].Time, bs[exit].Time)
					if crossesFunding {
						c.FundingCrossing++
					}
					if crossesFunding && fundingKnown {
						c.FundingKnown++
					}
					// P0 is evidence only, not timestamp-level direct execution cost; absent direct rows are honest zero.
					if complete && fundingKnown {
						c.KnownCost++
					}
					if _, ok := taker[bs[i].Time]; !ok {
						c.OptionalMissing["rubik_taker_1h"]++
					}
					if _, ok := lsRatio[bs[i].Time]; !ok {
						c.OptionalMissing["rubik_lsratio_1h"]++
					}
				}
			}
		}
	}
	out := make([]Coverage, 0, len(byKey))
	for _, c := range byKey {
		days := map[string]struct{}{}
		for _, t := range times {
			if Partition(time.UnixMilli(t).UTC(), bounds) == c.Partition && !Purged(time.UnixMilli(t).UTC(), bounds) {
				days[time.UnixMilli(t).UTC().Format("2006-01-02")] = struct{}{}
			}
		}
		c.Days = len(days)
		c.BTCRegimeBlocks = btcBlocks[c.Partition]
		c.MMRAvailable = false
		c.MMRWithdrawalDeferred = true
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Partition+fmt.Sprint(out[i].HorizonHours, out[i].DelayBars) < out[j].Partition+fmt.Sprint(out[j].HorizonHours, out[j].DelayBars)
	})
	f := Fitness{SchemaVersion: "lingxi.data-driven-router-v3.data-fitness.v2", LabelsUnblinded: false, Status: "PASS", ProtocolHashes: hashes, FeatureSchemaHash: schema, IdentitySchema: map[string]any{"key_fields": []string{"profile_id", "venue", "instrument", "decision_at", "source_id", "source_version"}, "profile_required_in_join": true, "observed_at_must_not_exceed_decision_at": true}, PartitionBoundaries: map[string]string{"validation_start": bounds[0].Format(time.RFC3339), "leaf_wf_start": bounds[1].Format(time.RFC3339), "router_holdout_start": bounds[2].Format(time.RFC3339)}, PurgeHours: 24, Coverage: out, CertificationEvidence: "limited: MMR unavailable; G5 withdrawal is deferred to zero while a position is open. Historical account replay uses a 5x leverage reserve and no-negative-cash constraint; it is not exchange-liquidation proof.", FundingManifestHash: fundingProvenance.ManifestHash, FundingReceiptHash: fundingProvenance.ReceiptHash}
	for _, c := range out {
		if c.KnownRatio() < MinCostKnown {
			f.Status = "DATA_BLIND_COST"
			f.HighestBlocker = fmt.Sprintf("%s/%dh/delay%d known cost coverage %.4f < 0.95", c.Partition, c.HorizonHours, c.DelayBars, c.KnownRatio())
			break
		}
	}
	f.DecisionJournal = []string{"Outcome fields, route results, PnL, winners, and reward labels were not read.", "Only fundingarchive.LoadVerifiedSource (the v3 manifest, receipt, and verified COMPLETE daily shards) was used for signed funding; the older daily archive was not opened.", "Known cost is based on COMPLETE UTC daily coverage over entry through exit and actual interval events; no fixed funding cadence was inferred.", "Exact duplicate signed funding rows are de-duplicated by the verified source; conflicting timestamp values fail closed.", "MMR is unavailable: G5 withdrawals are deferred to zero while a position is open, so certification evidence is limited rather than DATA_BLIND.", "No dataset shards or reward labels are generated unless this report is PASS."}
	return f, nil
}

func qualityPrefix(bs []bar) []int {
	prefix := make([]int, len(bs)+1)
	for i, b := range bs {
		prefix[i+1] = prefix[i]
		if b.Open <= 0 || b.High <= 0 || b.Low <= 0 || b.Close <= 0 || b.Volume < 0 || math.IsNaN(b.Close) {
			prefix[i+1]++
		}
	}
	return prefix
}
func completeRange(prefix []int, start, end int) bool {
	return start >= 0 && end+1 < len(prefix) && prefix[end+1] == prefix[start]
}
func completeUTCDays(provenance fundingarchive.Provenance, entry, exit int64) bool {
	entryDay := time.UnixMilli(entry).UTC().Format("2006-01-02")
	exitDay := time.UnixMilli(exit).UTC().Format("2006-01-02")
	if provenance.GapDays == 0 && provenance.StartDate != "" && provenance.EndDate != "" {
		return entryDay >= provenance.StartDate && exitDay <= provenance.EndDate
	}
	for day := time.UnixMilli(entry).UTC().Truncate(24 * time.Hour); !day.After(time.UnixMilli(exit).UTC().Truncate(24 * time.Hour)); day = day.AddDate(0, 0, 1) {
		if !provenance.CompleteDays[day.Format("2006-01-02")] {
			return false
		}
	}
	return true
}
func fundingCrosses(points []int64, entry, exit int64) bool {
	at := sort.Search(len(points), func(i int) bool { return points[i] > entry })
	return at < len(points) && points[at] <= exit
}
func countBTCRegimeBlocks(bars []bar, bounds [3]time.Time) map[string]int {
	out := map[string]int{"discovery": 0, "validation": 0, "leaf_wf": 0, "router_holdout": 0}
	last := map[string]int{}
	for i := 24; i < len(bars); i++ {
		t := time.UnixMilli(bars[i].Time).UTC()
		if Purged(t, bounds) || bars[i-24].Close <= 0 {
			continue
		}
		regime := 0
		if bars[i].Close/bars[i-24].Close >= 1 {
			regime = 1
		}
		p := Partition(t, bounds)
		if prior, ok := last[p]; !ok || prior != regime {
			out[p]++
		}
		last[p] = regime
	}
	return out
}
func readBars(path string) ([]bar, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []bar
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1024), 1024*1024)
	for s.Scan() {
		var v struct {
			Data []string `json:"data"`
		}
		if err := json.Unmarshal(s.Bytes(), &v); err != nil {
			return nil, err
		}
		if len(v.Data) < 7 {
			continue
		}
		var b bar
		if _, err := fmt.Sscan(strings.Join(v.Data[:7], " "), &b.Time, &b.Open, &b.High, &b.Low, &b.Close, new(float64), &b.Volume); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}
func readMergedFunding(existingPath, archiveDirectory, symbol string) (map[int64]string, error) {
	out, err := readFunding(existingPath)
	if err != nil {
		return nil, err
	}
	archiveFiles, err := filepath.Glob(filepath.Join(archiveDirectory, "????-??-??.jsonl"))
	if err != nil {
		return nil, err
	}
	for _, path := range archiveFiles {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 1024), 1024*1024)
		for s.Scan() {
			var v FundingRecord
			if err := json.Unmarshal(s.Bytes(), &v); err != nil {
				_ = f.Close()
				return nil, err
			}
			if v.Instrument != symbol {
				continue
			}
			if err := addFunding(out, v.EventTime, v.FundingRate); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		if err := s.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func loadDailyFundingArchive(directory string) (map[string]map[int64]string, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "source_manifest.json"))
	if os.IsNotExist(err) {
		return map[string]map[int64]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest FundingArchiveSourceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	expected := manifest.ManifestHash
	manifest.ManifestHash = ""
	computed, _, err := datatruth.HashJSON(manifest)
	if err != nil || expected == "" || computed != expected || manifest.Module != "3" ||
		manifest.StartDate != "2023-12-31" || manifest.EndDate != "2026-07-09" {
		return nil, errors.New("funding source manifest hash or scope mismatch")
	}
	out := map[string]map[int64]string{}
	for _, day := range manifest.Days {
		if day.Status != "DOWNLOADED" {
			continue
		}
		path := filepath.Join(directory, day.UTCDate+".jsonl")
		hash, err := SHA256File(path)
		if err != nil || hash != day.CanonicalSHA256 {
			return nil, fmt.Errorf("funding archive hash mismatch for %s", day.UTCDate)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 1024), 1024*1024)
		for s.Scan() {
			var record FundingRecord
			if err := json.Unmarshal(s.Bytes(), &record); err != nil {
				_ = f.Close()
				return nil, err
			}
			byTime := out[record.Instrument]
			if byTime == nil {
				byTime = map[int64]string{}
				out[record.Instrument] = byTime
			}
			if err := addFunding(byTime, record.EventTime, record.FundingRate); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		if err := s.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readFunding(path string) (map[int64]string, error) {
	out := map[int64]string{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1024), 1024*1024)
	for s.Scan() {
		var v struct {
			Data struct {
				FundingTime  string `json:"fundingTime"`
				FundingRate  string `json:"fundingRate"`
				RealizedRate string `json:"realizedRate"`
			} `json:"data"`
		}
		if err := json.Unmarshal(s.Bytes(), &v); err != nil {
			return nil, err
		}
		var t int64
		if _, err := fmt.Sscan(v.Data.FundingTime, &t); err == nil && t > 0 {
			rate := v.Data.RealizedRate
			if rate == "" {
				rate = v.Data.FundingRate
			}
			if err := addFunding(out, t, rate); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	return out, s.Err()
}

func addFunding(out map[int64]string, eventTime int64, rawRate string) error {
	rate, err := canonicalRate(rawRate)
	if err != nil {
		return err
	}
	if existing, ok := out[eventTime]; ok && existing != rate {
		return fmt.Errorf("conflicting signed funding at %d: %s != %s", eventTime, existing, rate)
	}
	out[eventTime] = rate
	return nil
}
func readOptional(path string) (map[int64]struct{}, error) {
	out := map[int64]struct{}{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1024), 1024*1024)
	for s.Scan() {
		var v struct {
			Data []string `json:"data"`
		}
		if err := json.Unmarshal(s.Bytes(), &v); err != nil {
			return nil, err
		}
		if len(v.Data) == 0 {
			continue
		}
		var t int64
		if _, err := fmt.Sscan(v.Data[0], &t); err == nil && t > 0 {
			out[t] = struct{}{}
		}
	}
	return out, s.Err()
}
func SHA256File(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}
