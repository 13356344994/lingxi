package datadrivenv3

import (
	"bufio"
	"compress/gzip"
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

const (
	StateSchemaVersionV2 = "lingxi.data-driven-router-v3.state-row.v2"
	DatasetDirectoryV2   = "dataset_state_okx_1h_v2"
	DatasetDirectoryV3   = "dataset_state_okx_1h_v3"
	StalledV1Status      = "STALLED_UNTRUSTED"
	FeatureCount         = 25
)

var FeatureOrder = [FeatureCount]string{
	"own_return_1h", "own_return_4h", "own_return_24h",
	"own_rms_vol_4h", "own_rms_vol_24h", "own_drawdown_24h",
	"quote_volume_24h", "bar_range_bps",
	"btc_return_1h", "btc_return_4h", "btc_return_24h",
	"btc_rms_vol_24h", "btc_drawdown_24h", "beta_24h",
	"cross_section_return_rank", "cross_section_vol_rank", "cross_section_liquidity_rank",
	"funding_rate_latest", "funding_age_hours", "rubik_taker_ratio", "rubik_ls_ratio",
	"utc_hour_sin", "utc_hour_cos", "utc_weekday_sin", "utc_weekday_cos",
}

const (
	fOwnRet1 = iota
	fOwnRet4
	fOwnRet24
	fOwnRMS4
	fOwnRMS24
	fOwnDrawdown24
	fQuoteVolume24
	fBarRange
	fBTCRet1
	fBTCRet4
	fBTCRet24
	fBTCRMS24
	fBTCDrawdown24
	fBeta24
	fCSRetRank
	fCSVolRank
	fCSLiquidityRank
	fFundingRate
	fFundingAge
	fRubikTaker
	fRubikLS
	fHourSin
	fHourCos
	fWeekdaySin
	fWeekdayCos
)

type FeatureValues [FeatureCount]float64

type StateRowV2 struct {
	SchemaVersion string        `json:"schema_version"`
	EventID       string        `json:"event_id"`
	ProfileID     string        `json:"profile_id"`
	Venue         string        `json:"venue"`
	Instrument    string        `json:"instrument"`
	DecisionMS    int64         `json:"decision_ms"`
	Partition     string        `json:"partition"`
	Values        FeatureValues `json:"feature_values"`
	MissingBits   uint64        `json:"missing_bits"`
	SourceID      string        `json:"source_id"`
	SourceVersion string        `json:"source_version"`
}

type OptionalPoint struct {
	FundingRate  float64
	FundingMS    int64
	TakerRatio   float64
	LSRatio      float64
	HasFunding   bool
	HasTaker     bool
	HasLS        bool
	ObservedAtMS int64
}

type OptionalLookup interface {
	At(instrument string, decisionMS int64) (OptionalPoint, error)
}

type BuildConfigV2 struct {
	Root                        string
	OutputDir                   string
	Symbols                     []string
	Bounds                      [3]time.Time
	ProtocolHashes              map[string]string
	FundingManifestHash         string
	FundingReceiptHash          string
	StartMS                     int64
	EndMS                       int64
	CompleteDays                int
	RequireCompleteCrossSection bool
	Optional                    OptionalLookup
	Progress                    io.Writer
	ThrottleEveryRows           int64
	ThrottleSleep               time.Duration
}

type SourceBinding struct {
	CatalogHash         string            `json:"catalog_hash"`
	ProtocolHashes      map[string]string `json:"protocol_hashes"`
	FundingManifestHash string            `json:"funding_manifest_hash"`
	FundingReceiptHash  string            `json:"funding_receipt_hash"`
	FeatureSchemaHash   string            `json:"feature_schema_hash"`
	PriceSourceHash     string            `json:"price_source_hash"`
}

type ShardReceiptV2 struct {
	SchemaVersion string `json:"schema_version"`
	Partition     string `json:"partition"`
	Month         string `json:"month"`
	Path          string `json:"path"`
	Rows          int64  `json:"rows"`
	FirstMS       int64  `json:"first_ms"`
	LastMS        int64  `json:"last_ms"`
	SHA256        string `json:"sha256"`
	SourceBinding string `json:"source_binding"`
}

type CheckpointV2 struct {
	SchemaVersion string           `json:"schema_version"`
	Status        string           `json:"status"`
	SourceBinding string           `json:"source_binding"`
	SealedThrough int64            `json:"sealed_through_ms"`
	Shards        []ShardReceiptV2 `json:"sealed_shards"`
}

type BuildResultV2 struct {
	SchemaVersion string               `json:"schema_version"`
	StateSchema   string               `json:"state_schema_version"`
	FeatureOrder  [FeatureCount]string `json:"feature_order"`
	Rows          int64                `json:"rows"`
	Shards        []ShardReceiptV2     `json:"shards"`
	Checkpoint    string               `json:"checkpoint"`
	SourceBinding string               `json:"source_binding"`
	EmissionNanos int64                `json:"emission_nanos,omitempty"`
}

type streamBar struct {
	Time, Open, High, Low, Close int64
	QuoteVolume                  float64
}

type ring24 struct {
	bars    [25]streamBar
	n, next int
}

func (r *ring24) push(b streamBar) {
	r.bars[r.next] = b
	r.next = (r.next + 1) % len(r.bars)
	if r.n < len(r.bars) {
		r.n++
	}
}
func (r *ring24) at(back int) streamBar {
	index := (r.next - 1 - back + len(r.bars)) % len(r.bars)
	return r.bars[index]
}
func (r *ring24) complete() bool {
	if r.n < len(r.bars) {
		return false
	}
	for i := 0; i < 24; i++ {
		if r.at(i).Time-r.at(i+1).Time != int64(time.Hour/time.Millisecond) {
			return false
		}
	}
	return true
}

type reverseLines struct {
	f   *os.File
	pos int64
	buf []byte
}

func openReverseLines(path string) (*reverseLines, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &reverseLines{f: f, pos: info.Size()}, nil
}
func (r *reverseLines) Close() error { return r.f.Close() }
func (r *reverseLines) Next() ([]byte, error) {
	for {
		if len(r.buf) > 0 {
			if i := lastIndexByte(r.buf, '\n'); i >= 0 {
				line := append([]byte(nil), r.buf[i+1:]...)
				r.buf = r.buf[:i]
				if len(line) > 0 {
					return line, nil
				}
				continue
			}
		}
		if r.pos == 0 {
			if len(r.buf) == 0 {
				return nil, io.EOF
			}
			line := append([]byte(nil), r.buf...)
			r.buf = nil
			return line, nil
		}
		n := int64(64 << 10)
		if r.pos < n {
			n = r.pos
		}
		r.pos -= n
		chunk := make([]byte, n)
		if _, err := r.f.ReadAt(chunk, r.pos); err != nil {
			return nil, err
		}
		r.buf = append(chunk, r.buf...)
	}
}
func lastIndexByte(b []byte, target byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == target {
			return i
		}
	}
	return -1
}

type priceIterator struct {
	symbol, path, sourceHash string
	lines                    *reverseLines
	current                  streamBar
	ring                     ring24
}

func openPriceIterator(path string) (*priceIterator, error) {
	lines, err := openReverseLines(path)
	if err != nil {
		return nil, err
	}
	hash, err := HashFileStreaming(path)
	if err != nil {
		lines.Close()
		return nil, err
	}
	it := &priceIterator{symbol: strings.TrimSuffix(filepath.Base(path), ".jsonl"), path: path, sourceHash: hash, lines: lines}
	if err := it.advance(); err != nil {
		lines.Close()
		return nil, err
	}
	return it, nil
}
func (it *priceIterator) advance() error {
	for {
		line, err := it.lines.Next()
		if err != nil {
			return err
		}
		bar, err := parseStreamBar(line)
		if err != nil {
			return fmt.Errorf("%s: %w", it.path, err)
		}
		it.current = bar
		return nil
	}
}
func parseStreamBar(line []byte) (streamBar, error) {
	var row struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return streamBar{}, err
	}
	if len(row.Data) < 8 {
		return streamBar{}, errors.New("bar has fewer than 8 fields")
	}
	values := [5]int64{}
	for i := 0; i < 5; i++ {
		if i == 0 {
			v, err := strconv.ParseInt(row.Data[i], 10, 64)
			if err != nil {
				return streamBar{}, err
			}
			values[i] = v
		} else {
			v, err := fixedPrice(row.Data[i])
			if err != nil {
				return streamBar{}, err
			}
			values[i] = v
		}
	}
	qv, err := strconv.ParseFloat(row.Data[7], 64)
	if err != nil {
		return streamBar{}, err
	}
	return streamBar{Time: values[0], Open: values[1], High: values[2], Low: values[3], Close: values[4], QuoteVolume: qv}, nil
}
func fixedPrice(raw string) (int64, error) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("invalid positive price")
	}
	return int64(math.Round(v * 1e8)), nil
}

type iteratorHeap []*priceIterator

func (h iteratorHeap) Len() int { return len(h) }
func (h iteratorHeap) Less(i, j int) bool {
	if h[i].current.Time != h[j].current.Time {
		return h[i].current.Time < h[j].current.Time
	}
	return h[i].symbol < h[j].symbol
}
func (h iteratorHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *iteratorHeap) Push(x any)   { *h = append(*h, x.(*priceIterator)) }
func (h *iteratorHeap) Pop() any     { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }

type computedRow struct {
	it      *priceIterator
	values  FeatureValues
	missing uint64
}

func BuildStateRowsV2(cfg BuildConfigV2) (BuildResultV2, error) {
	if cfg.Root == "" || cfg.OutputDir == "" {
		return BuildResultV2{}, errors.New("root and output required")
	}
	outputBase := filepath.Base(cfg.OutputDir)
	if outputBase != DatasetDirectoryV2 && outputBase != DatasetDirectoryV3 && !strings.Contains(outputBase, "benchmark") {
		return BuildResultV2{}, fmt.Errorf("state builder refuses unknown output directory %q", cfg.OutputDir)
	}
	symbols, err := selectedSymbols(cfg.Root, cfg.Symbols)
	if err != nil {
		return BuildResultV2{}, err
	}
	binding, bindingHash, err := buildSourceBinding(cfg, symbols)
	if err != nil {
		return BuildResultV2{}, err
	}
	_ = binding
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return BuildResultV2{}, err
	}
	if err := freezeSourceBinding(cfg.OutputDir, binding, bindingHash); err != nil {
		return BuildResultV2{}, err
	}
	cpPath := filepath.Join(cfg.OutputDir, "checkpoint.json")
	cp, err := loadCheckpointV2(cpPath, cfg.OutputDir, bindingHash)
	if err != nil {
		return BuildResultV2{}, err
	}
	if err := removePartials(cfg.OutputDir); err != nil {
		return BuildResultV2{}, err
	}

	var h iteratorHeap
	var allIterators []*priceIterator
	for _, symbol := range symbols {
		path := filepath.Join(cfg.Root, "records/history/okx_1h", symbol+".jsonl")
		it, err := openPriceIterator(path)
		if err != nil {
			for _, opened := range allIterators {
				_ = opened.lines.Close()
			}
			return BuildResultV2{}, err
		}
		allIterators = append(allIterators, it)
		heap.Push(&h, it)
	}
	defer func() {
		for _, it := range allIterators {
			_ = it.lines.Close()
		}
	}()
	heap.Init(&h)

	var writer *monthWriter
	defer func() {
		if writer != nil {
			writer.abort()
		}
	}()
	var completeStart int64
	var emissionStarted time.Time
	var expectedSymbols = len(symbols)
	lastTimestamp := int64(-1)
	var emittedRows int64
	for h.Len() > 0 {
		ts := h[0].current.Time
		if ts < lastTimestamp {
			return BuildResultV2{}, errors.New("global iterator order regressed")
		}
		lastTimestamp = ts
		var current []*priceIterator
		for h.Len() > 0 && h[0].current.Time == ts {
			it := heap.Pop(&h).(*priceIterator)
			it.ring.push(it.current)
			current = append(current, it)
			if err := it.advance(); err == nil {
				heap.Push(&h, it)
			} else if err != io.EOF {
				return BuildResultV2{}, err
			}
		}
		if ts <= cp.SealedThrough || (cfg.StartMS > 0 && ts < cfg.StartMS) {
			continue
		}
		if cfg.EndMS > 0 && ts > cfg.EndMS {
			break
		}
		if Purged(time.UnixMilli(ts).UTC(), cfg.Bounds) {
			continue
		}
		bySymbol := make(map[string]*priceIterator, len(current))
		for _, it := range current {
			bySymbol[it.symbol] = it
		}
		btc := bySymbol["BTC-USDT-SWAP"]
		if btc == nil || !btc.ring.complete() {
			continue
		}
		if cfg.CompleteDays > 0 && completeStart == 0 {
			allReady := len(current) == expectedSymbols
			for _, it := range current {
				allReady = allReady && it.ring.complete()
			}
			t := time.UnixMilli(ts).UTC()
			if allReady && t.Hour() == 0 {
				completeStart = ts
				emissionStarted = time.Now()
			}
			if completeStart == 0 {
				continue
			}
		}
		if cfg.CompleteDays > 0 && ts >= completeStart+int64(cfg.CompleteDays)*int64(24*time.Hour/time.Millisecond) {
			break
		}
		if cfg.RequireCompleteCrossSection && len(current) != expectedSymbols {
			return BuildResultV2{}, fmt.Errorf("incomplete cross section at %d: %d/%d", ts, len(current), expectedSymbols)
		}
		computed := make([]computedRow, 0, len(current))
		for _, it := range current {
			if !it.ring.complete() {
				continue
			}
			values := computeFixedFeatures(&it.ring, &btc.ring, ts)
			missing := uint64(0)
			for _, index := range []int{fFundingRate, fFundingAge, fRubikTaker, fRubikLS} {
				missing |= 1 << index
			}
			if cfg.Optional != nil {
				point, err := cfg.Optional.At(it.symbol, ts)
				if err != nil {
					return BuildResultV2{}, err
				}
				if point.ObservedAtMS > ts {
					return BuildResultV2{}, fmt.Errorf("optional future leak %s/%d", it.symbol, ts)
				}
				if point.HasFunding {
					values[fFundingRate], values[fFundingAge] = point.FundingRate, float64(ts-point.FundingMS)/float64(time.Hour/time.Millisecond)
					missing &^= (1 << fFundingRate) | (1 << fFundingAge)
				}
				if point.HasTaker {
					values[fRubikTaker] = point.TakerRatio
					missing &^= 1 << fRubikTaker
				}
				if point.HasLS {
					values[fRubikLS] = point.LSRatio
					missing &^= 1 << fRubikLS
				}
			}
			computed = append(computed, computedRow{it: it, values: values, missing: missing})
		}
		applyRanks(computed)
		decision := time.UnixMilli(ts).UTC()
		key := Partition(decision, cfg.Bounds) + "/" + decision.Format("2006-01")
		if writer == nil || writer.key != key {
			if writer != nil {
				receipt, err := writer.seal(bindingHash)
				if err != nil {
					return BuildResultV2{}, err
				}
				cp.Shards = append(cp.Shards, receipt)
				cp.SealedThrough = receipt.LastMS
				if err := writeCheckpointV2(cpPath, cp); err != nil {
					return BuildResultV2{}, err
				}
				if cfg.Progress != nil {
					fmt.Fprintf(cfg.Progress, "sealed %s rows=%d hash=%s\n", receipt.Path, receipt.Rows, receipt.SHA256)
				}
			}
			writer, err = openMonthWriter(cfg.OutputDir, key)
			if err != nil {
				return BuildResultV2{}, err
			}
		}
		for _, c := range computed {
			sourceID := filepath.ToSlash(stringsTrimPrefix(c.it.path, cfg.Root+"/"))
			row := StateRowV2{SchemaVersion: StateSchemaVersionV2, EventID: EventID(ProfileOKX1H, "okx", c.it.symbol, decision, sourceID, c.it.sourceHash), ProfileID: ProfileOKX1H, Venue: "okx", Instrument: c.it.symbol, DecisionMS: ts, Partition: strings.SplitN(key, "/", 2)[0], Values: c.values, MissingBits: c.missing, SourceID: sourceID, SourceVersion: c.it.sourceHash}
			if err := writer.encode(row); err != nil {
				return BuildResultV2{}, err
			}
			emittedRows++
			if cfg.Progress != nil && emittedRows%250000 == 0 {
				fmt.Fprintf(cfg.Progress, "state rows emitted=%d decision=%s\n", emittedRows, decision.Format(time.RFC3339))
			}
			if cfg.ThrottleEveryRows > 0 && cfg.ThrottleSleep > 0 && emittedRows%cfg.ThrottleEveryRows == 0 {
				time.Sleep(cfg.ThrottleSleep)
			}
		}
	}
	if writer != nil {
		receipt, err := writer.seal(bindingHash)
		if err != nil {
			return BuildResultV2{}, err
		}
		cp.Shards = append(cp.Shards, receipt)
		cp.SealedThrough = receipt.LastMS
		if err := writeCheckpointV2(cpPath, cp); err != nil {
			return BuildResultV2{}, err
		}
	}
	cp.Status = "COMPLETE"
	if err := writeCheckpointV2(cpPath, cp); err != nil {
		return BuildResultV2{}, err
	}
	var emissionNanos int64
	if !emissionStarted.IsZero() {
		emissionNanos = time.Since(emissionStarted).Nanoseconds()
	}
	return BuildResultV2{SchemaVersion: "lingxi.data-driven-router-v3.dataset-manifest.v2", StateSchema: StateSchemaVersionV2, FeatureOrder: FeatureOrder, Rows: sumRows(cp.Shards), Shards: cp.Shards, Checkpoint: filepath.Base(cpPath), SourceBinding: bindingHash, EmissionNanos: emissionNanos}, nil
}

func computeFixedFeatures(own, btc *ring24, ts int64) FeatureValues {
	var v FeatureValues
	v[fOwnRet1] = ret(own.at(0), own.at(1))
	v[fOwnRet4] = ret(own.at(0), own.at(4))
	v[fOwnRet24] = ret(own.at(0), own.at(24))
	v[fOwnRMS4] = rms(own, 4)
	v[fOwnRMS24] = rms(own, 24)
	v[fOwnDrawdown24] = drawdown(own)
	for i := 0; i < 24; i++ {
		v[fQuoteVolume24] += own.at(i).QuoteVolume
	}
	v[fBarRange] = (float64(own.at(0).High)/float64(own.at(0).Low) - 1) * 10000
	v[fBTCRet1] = ret(btc.at(0), btc.at(1))
	v[fBTCRet4] = ret(btc.at(0), btc.at(4))
	v[fBTCRet24] = ret(btc.at(0), btc.at(24))
	v[fBTCRMS24] = rms(btc, 24)
	v[fBTCDrawdown24] = drawdown(btc)
	v[fBeta24] = beta(own, btc)
	t := time.UnixMilli(ts).UTC()
	hourAngle := 2 * math.Pi * float64(t.Hour()) / 24
	weekdayAngle := 2 * math.Pi * float64(t.Weekday()) / 7
	v[fHourSin], v[fHourCos] = math.Sin(hourAngle), math.Cos(hourAngle)
	v[fWeekdaySin], v[fWeekdayCos] = math.Sin(weekdayAngle), math.Cos(weekdayAngle)
	return v
}
func ret(now, before streamBar) float64 { return float64(now.Close)/float64(before.Close) - 1 }
func rms(r *ring24, n int) float64 {
	var s float64
	for i := 0; i < n; i++ {
		x := ret(r.at(i), r.at(i+1))
		s += x * x
	}
	return math.Sqrt(s / float64(n))
}
func drawdown(r *ring24) float64 {
	high := r.at(0).Close
	for i := 1; i < 25; i++ {
		if r.at(i).Close > high {
			high = r.at(i).Close
		}
	}
	return float64(r.at(0).Close)/float64(high) - 1
}
func beta(a, b *ring24) float64 {
	var ax, bx [24]float64
	var am, bm float64
	for i := 0; i < 24; i++ {
		ax[i] = ret(a.at(i), a.at(i+1))
		bx[i] = ret(b.at(i), b.at(i+1))
		am += ax[i]
		bm += bx[i]
	}
	am /= 24
	bm /= 24
	var cov, variance float64
	for i := 0; i < 24; i++ {
		cov += (ax[i] - am) * (bx[i] - bm)
		variance += (bx[i] - bm) * (bx[i] - bm)
	}
	if variance == 0 {
		return 0
	}
	return cov / variance
}
func applyRanks(rows []computedRow) {
	rankFeature(rows, fOwnRet1, fCSRetRank)
	rankFeature(rows, fOwnRMS24, fCSVolRank)
	rankFeature(rows, fQuoteVolume24, fCSLiquidityRank)
}
func rankFeature(rows []computedRow, source, target int) {
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := rows[order[i]], rows[order[j]]
		if a.values[source] != b.values[source] {
			return a.values[source] < b.values[source]
		}
		return a.it.symbol < b.it.symbol
	})
	if len(order) == 1 {
		rows[order[0]].values[target] = .5
		return
	}
	for start := 0; start < len(order); {
		end := start + 1
		for end < len(order) && rows[order[end]].values[source] == rows[order[start]].values[source] {
			end++
		}
		rank := float64(start+end-1) / 2 / float64(len(order)-1)
		for i := start; i < end; i++ {
			rows[order[i]].values[target] = rank
		}
		start = end
	}
}

type monthWriter struct {
	key, partial, final string
	file                *os.File
	gzip                *gzip.Writer
	enc                 *json.Encoder
	rows, first, last   int64
	closed              bool
}

func openMonthWriter(output, key string) (*monthWriter, error) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid month key")
	}
	dir := filepath.Join(output, "partition="+parts[0])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	final := filepath.Join(dir, "month="+parts[1]+".jsonl.gz")
	partial := final + ".partial"
	if _, err := os.Stat(final); err == nil {
		return nil, fmt.Errorf("sealed shard already exists without checkpoint: %s", final)
	}
	if err := os.Remove(partial); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	z, err := gzip.NewWriterLevel(f, gzip.BestSpeed)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &monthWriter{key: key, partial: partial, final: final, file: f, gzip: z, enc: json.NewEncoder(z)}, nil
}
func (w *monthWriter) encode(row StateRowV2) error {
	if w.rows == 0 {
		w.first = row.DecisionMS
	}
	w.last = row.DecisionMS
	w.rows++
	return w.enc.Encode(row)
}
func (w *monthWriter) seal(binding string) (ShardReceiptV2, error) {
	if err := w.gzip.Close(); err != nil {
		return ShardReceiptV2{}, err
	}
	if err := w.file.Sync(); err != nil {
		return ShardReceiptV2{}, err
	}
	if err := w.file.Close(); err != nil {
		return ShardReceiptV2{}, err
	}
	w.closed = true
	if err := os.Rename(w.partial, w.final); err != nil {
		return ShardReceiptV2{}, err
	}
	if err := syncDir(filepath.Dir(w.final)); err != nil {
		return ShardReceiptV2{}, err
	}
	hash, err := HashFileStreaming(w.final)
	if err != nil {
		return ShardReceiptV2{}, err
	}
	parts := strings.SplitN(w.key, "/", 2)
	receipt := ShardReceiptV2{SchemaVersion: "lingxi.data-driven-router-v3.shard-receipt.v2", Partition: parts[0], Month: parts[1], Path: filepath.ToSlash(filepath.Join("partition="+parts[0], filepath.Base(w.final))), Rows: w.rows, FirstMS: w.first, LastMS: w.last, SHA256: hash, SourceBinding: binding}
	raw, _ := json.MarshalIndent(receipt, "", "  ")
	if err := writeAtomic(w.final+".receipt.json", append(raw, '\n')); err != nil {
		return ShardReceiptV2{}, err
	}
	return receipt, nil
}
func (w *monthWriter) abort() {
	if w.closed {
		return
	}
	_ = w.gzip.Close()
	_ = w.file.Close()
	w.closed = true
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func selectedSymbols(root string, requested []string) ([]string, error) {
	if len(requested) > 0 {
		out := append([]string(nil), requested...)
		sort.Strings(out)
		return out, nil
	}
	files, err := filepath.Glob(filepath.Join(root, "records/history/okx_1h/*.jsonl"))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, p := range files {
		out = append(out, strings.TrimSuffix(filepath.Base(p), ".jsonl"))
	}
	sort.Strings(out)
	return out, nil
}
func buildSourceBinding(cfg BuildConfigV2, symbols []string) (SourceBinding, string, error) {
	catalogHash, err := HashFileStreaming(filepath.Join(cfg.Root, "research/firstbattle/history_catalog.json"))
	if err != nil {
		return SourceBinding{}, "", err
	}
	type source struct{ Symbol, Hash string }
	sources := make([]source, 0, len(symbols))
	for _, s := range symbols {
		h, err := HashFileStreaming(filepath.Join(cfg.Root, "records/history/okx_1h", s+".jsonl"))
		if err != nil {
			return SourceBinding{}, "", err
		}
		sources = append(sources, source{s, h})
	}
	priceHash, _, err := datatruth.HashJSON(sources)
	if err != nil {
		return SourceBinding{}, "", err
	}
	schemaHash, _, err := datatruth.HashJSON(FeatureOrder)
	if err != nil {
		return SourceBinding{}, "", err
	}
	b := SourceBinding{CatalogHash: catalogHash, ProtocolHashes: cfg.ProtocolHashes, FundingManifestHash: cfg.FundingManifestHash, FundingReceiptHash: cfg.FundingReceiptHash, FeatureSchemaHash: schemaHash, PriceSourceHash: priceHash}
	hash, _, err := datatruth.HashJSON(b)
	return b, hash, err
}
func freezeSourceBinding(output string, binding SourceBinding, hash string) error {
	doc := struct {
		SchemaVersion string        `json:"schema_version"`
		BindingHash   string        `json:"binding_hash"`
		Binding       SourceBinding `json:"binding"`
	}{SchemaVersion: "lingxi.data-driven-router-v3.source-binding.v2", BindingHash: hash, Binding: binding}
	path := filepath.Join(output, "source_binding.json")
	if raw, err := os.ReadFile(path); err == nil {
		var prior struct {
			SchemaVersion string        `json:"schema_version"`
			BindingHash   string        `json:"binding_hash"`
			Binding       SourceBinding `json:"binding"`
		}
		if json.Unmarshal(raw, &prior) != nil || prior.SchemaVersion != doc.SchemaVersion || prior.BindingHash != hash {
			return errors.New("frozen source binding drift")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(raw, '\n'))
}
func loadCheckpointV2(path, output, binding string) (CheckpointV2, error) {
	cp := CheckpointV2{SchemaVersion: "lingxi.data-driven-router-v3.dataset-checkpoint.v2", Status: "BUILDING", SourceBinding: binding}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cp, nil
	}
	if err != nil {
		return cp, err
	}
	if err := json.Unmarshal(raw, &cp); err != nil {
		return cp, err
	}
	if cp.SchemaVersion != "lingxi.data-driven-router-v3.dataset-checkpoint.v2" {
		return cp, fmt.Errorf("%s checkpoint rejected: %s", StalledV1Status, cp.SchemaVersion)
	}
	if cp.SourceBinding != binding {
		return cp, errors.New("checkpoint source binding drift")
	}
	for _, r := range cp.Shards {
		path := filepath.Join(output, filepath.FromSlash(r.Path))
		h, err := HashFileStreaming(path)
		if err != nil || h != r.SHA256 || r.SourceBinding != binding {
			return cp, fmt.Errorf("sealed shard receipt mismatch %s", r.Path)
		}
		raw, err := os.ReadFile(path + ".receipt.json")
		if err != nil {
			return cp, fmt.Errorf("missing shard sidecar %s", r.Path)
		}
		var sidecar ShardReceiptV2
		if json.Unmarshal(raw, &sidecar) != nil || sidecar != r {
			return cp, fmt.Errorf("shard sidecar drift %s", r.Path)
		}
	}
	if cp.Status == "COMPLETE" {
		return cp, errors.New("dataset already complete")
	}
	return cp, nil
}
func writeCheckpointV2(path string, cp CheckpointV2) error {
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(raw, '\n'))
}
func writeAtomic(path string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func removePartials(root string) error {
	paths, err := filepath.Glob(filepath.Join(root, "partition=*", "*.partial"))
	if err != nil {
		return err
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}
func sumRows(shards []ShardReceiptV2) int64 {
	var n int64
	for _, s := range shards {
		n += s.Rows
	}
	return n
}
func stringsTrimPrefix(s, prefix string) string {
	if strings.HasPrefix(s, prefix) {
		return strings.TrimPrefix(s, prefix)
	}
	return s
}

func HashFileStreaming(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, bufio.NewReaderSize(f, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
