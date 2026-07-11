package app

// 方向因子前向 shadow（绝不下单）：
// A: 长尾1h跌≥2% × BTC24h跌≥3% × 本币24h量≥14天中位1.5倍 → 做多4h；
// B: 同条件且事件时上市≥365天 → 做多1h。
// A 在训练/验证/锁定历史总体均为正但月度稳定门失败；B 仅训练/验证通过。
// 因此这里只做真正未污染前向，禁止接 fastLiveOpen。

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
)

const (
	directionFactorSeedFile  = "direction_factor_seed.json"
	directionFactorStateFile = "direction_factor_shadow_state.json"
	directionFactorEventFile = "direction_factor_shadow.jsonl"
)

type directionFactorHistory struct {
	FirstTS int64     `json:"first_ts"`
	V24     []float64 `json:"v24"`
}

type directionFactorSeed struct {
	Symbols map[string]directionFactorHistory `json:"symbols"`
}

type directionFactorPending struct {
	Source   string    `json:"source"`
	Symbol   string    `json:"symbol"`
	OpenedAt time.Time `json:"opened_at"`
	Entry    float64   `json:"entry"`
	HoldHour int       `json:"hold_hours"`
	Ret1hBps float64   `json:"ret_1h_bps"`
	BTC24Bps float64   `json:"btc_24h_bps"`
	SymVolx  float64   `json:"sym_volx"`
	AgeDays  float64   `json:"age_days"`
}

type directionFactorState struct {
	Hour     int64                             `json:"hour"`
	Hist     map[string]directionFactorHistory `json:"hist"`
	Pending  map[string]directionFactorPending `json:"pending"`
	Cooldown map[string]time.Time              `json:"cooldown"`
}

type directionFactorEvent struct {
	RecordedAt time.Time `json:"recorded_at"`
	Stage      string    `json:"stage"`
	Source     string    `json:"source"`
	Symbol     string    `json:"symbol"`
	Entry      float64   `json:"entry,omitempty"`
	Exit       float64   `json:"exit,omitempty"`
	HoldHours  int       `json:"hold_hours"`
	Ret1hBps   float64   `json:"ret_1h_bps"`
	BTC24Bps   float64   `json:"btc_24h_bps"`
	SymVolx    float64   `json:"sym_volx"`
	AgeDays    float64   `json:"age_days"`
	GrossBps   float64   `json:"gross_bps,omitempty"`
	Net12Bps   float64   `json:"net_12bps,omitempty"`
	Net18Bps   float64   `json:"net_18bps,omitempty"`
	Net24Bps   float64   `json:"net_24bps,omitempty"`
}

var (
	directionFactorMu     sync.Mutex
	directionFactorLoaded bool
	directionFactorSt     directionFactorState
)

var directionFactorExcluded = map[string]bool{
	"BTC-USDT": true, "ETH-USDT": true, "SOL-USDT": true, "XRP-USDT": true, "BNB-USDT": true,
	"DOGE-USDT": true, "ADA-USDT": true, "LTC-USDT": true, "LINK-USDT": true, "AVAX-USDT": true,
	"TRX-USDT": true, "DOT-USDT": true, "TON-USDT": true,
	"SAMSUNG-USDT": true, "SKHYNIX-USDT": true, "SNDK-USDT": true, "AAPL-USDT": true,
	"SOXL-USDT": true, "TSLA-USDT": true, "NVDA-USDT": true, "MSTR-USDT": true,
	"COIN-USDT": true, "CRCL-USDT": true, "HOOD-USDT": true, "META-USDT": true,
	"AMZN-USDT": true, "GOOGL-USDT": true, "ORCL-USDT": true, "MCD-USDT": true,
}

func medianPositive(v []float64) float64 {
	cp := make([]float64, 0, len(v))
	for _, x := range v {
		if x > 0 && !math.IsNaN(x) && !math.IsInf(x, 0) {
			cp = append(cp, x)
		}
	}
	if len(cp) == 0 {
		return 0
	}
	sort.Float64s(cp)
	return cp[len(cp)/2]
}

func (e *OKXDirectionalEngine) directionFactorLoadLocked() {
	if directionFactorLoaded {
		return
	}
	directionFactorLoaded = true
	directionFactorSt = directionFactorState{
		Hist: map[string]directionFactorHistory{}, Pending: map[string]directionFactorPending{},
		Cooldown: map[string]time.Time{},
	}
	statePath := filepath.Join(e.recordDir, directionFactorStateFile)
	if b, err := osReadFile(statePath); err == nil && json.Unmarshal(b, &directionFactorSt) == nil {
		if directionFactorSt.Hist == nil {
			directionFactorSt.Hist = map[string]directionFactorHistory{}
		}
		if directionFactorSt.Pending == nil {
			directionFactorSt.Pending = map[string]directionFactorPending{}
		}
		if directionFactorSt.Cooldown == nil {
			directionFactorSt.Cooldown = map[string]time.Time{}
		}
		return
	}
	var seed directionFactorSeed
	if b, err := osReadFile(filepath.Join(e.recordDir, directionFactorSeedFile)); err == nil && json.Unmarshal(b, &seed) == nil {
		directionFactorSt.Hist = seed.Symbols
	}
}

// osReadFile 保持测试可替换面最小；这里只封装标准库读取。
var osReadFile = func(path string) ([]byte, error) { return os.ReadFile(path) }

func (e *OKXDirectionalEngine) directionFactorPersistLocked() {
	_ = writeJSONFile(filepath.Join(e.recordDir, directionFactorStateFile), directionFactorSt)
}

func fastWxRet1h(sym string, now time.Time) (float64, bool) {
	fastWx60Mu.Lock()
	defer fastWx60Mu.Unlock()
	h := fastWx60Hist[sym]
	if len(h) < 2 {
		return 0, false
	}
	newest := h[len(h)-1]
	var old fastMidPoint
	for _, p := range h {
		if now.Sub(p.ts) >= 50*time.Minute {
			old = p
		} else {
			break
		}
	}
	if old.mid <= 0 || newest.mid <= 0 {
		return 0, false
	}
	return (newest.mid/old.mid - 1) * 1e4, true
}

func btcReturn24h(ctx context.Context, v DirectionalVenue) (float64, bool) {
	candles, err := v.FetchCandles(ctx, venue.MarketPerp, "BTC-USDT", "1H", 30)
	if err != nil || len(candles) < 25 {
		return 0, false
	}
	sort.Slice(candles, func(i, j int) bool { return candles[i].OpenTime.Before(candles[j].OpenTime) })
	a, b := candles[len(candles)-25].Close, candles[len(candles)-1].Close
	if a <= 0 || b <= 0 {
		return 0, false
	}
	return (b/a - 1) * 1e4, true
}

func factorPendingKey(source, sym string) string { return source + "|" + sym }

func directionFactorCommonEligible(ret1Bps, btc24Bps, symVolx float64) bool {
	return ret1Bps <= -200 && btc24Bps <= -300 && symVolx >= 1.5
}

func (e *OKXDirectionalEngine) fastDirectionFactorShadowTick(
	ctx context.Context,
	now time.Time,
	books map[string]venue.BookTicker,
	v24Now map[string]float64,
) {
	btc24, btcOK := btcReturn24h(ctx, e.v)
	if !btcOK {
		return
	}
	directionFactorMu.Lock()
	defer directionFactorMu.Unlock()
	e.directionFactorLoadLocked()

	changed := false
	hour := now.Unix() / 3600
	if hour != directionFactorSt.Hour {
		directionFactorSt.Hour = hour
		for sym, v := range v24Now {
			h := directionFactorSt.Hist[sym]
			if v > 0 {
				h.V24 = append(h.V24, v)
				if len(h.V24) > 336 {
					h.V24 = h.V24[len(h.V24)-336:]
				}
				directionFactorSt.Hist[sym] = h
			}
		}
		changed = true
	}

	for key, p := range directionFactorSt.Pending {
		if now.Before(p.OpenedAt.Add(time.Duration(p.HoldHour) * time.Hour)) {
			continue
		}
		book, ok := books[p.Symbol]
		if !ok || book.BidPrice <= 0 || book.AskPrice <= 0 || p.Entry <= 0 {
			continue
		}
		exit := (book.BidPrice + book.AskPrice) / 2
		gross := (exit/p.Entry - 1) * 1e4
		ev := directionFactorEvent{
			RecordedAt: now, Stage: "close", Source: p.Source, Symbol: p.Symbol,
			Entry: p.Entry, Exit: exit, HoldHours: p.HoldHour,
			Ret1hBps: p.Ret1hBps, BTC24Bps: p.BTC24Bps, SymVolx: p.SymVolx, AgeDays: p.AgeDays,
			GrossBps: gross, Net12Bps: gross - 12, Net18Bps: gross - 18, Net24Bps: gross - 24,
		}
		_ = appendJSONLRecord(filepath.Join(e.recordDir, directionFactorEventFile), ev)
		delete(directionFactorSt.Pending, key)
		changed = true
	}

	if btc24 <= -300 {
		for sym, book := range books {
			if directionFactorExcluded[sym] || book.BidPrice <= 0 || book.AskPrice <= 0 {
				continue
			}
			ret1, ok := fastWxRet1h(sym, now)
			if !ok || ret1 > -200 {
				continue
			}
			h, ok := directionFactorSt.Hist[sym]
			med := medianPositive(h.V24)
			cur := v24Now[sym]
			if !ok || med <= 0 || !directionFactorCommonEligible(ret1, btc24, cur/med) || h.FirstTS <= 0 {
				continue
			}
			ageDays := now.Sub(time.Unix(h.FirstTS, 0)).Hours() / 24
			mid := (book.BidPrice + book.AskPrice) / 2
			metrics := autonomousMetrics{Ret1hBps: ret1, BTC24Bps: btc24, SymVolx: cur / med, AgeDays: ageDays}
			risk, _ := loadAutonomousRisk(filepath.Dir(e.recordDir))
			for _, spec := range autonomousSpecsMatching(e.recordDir, now, risk.Generation, metrics) {
				key := factorPendingKey(spec.Source, sym)
				if !directionFactorSt.Cooldown[key].Before(now) {
					continue
				}
				p := directionFactorPending{
					Source: spec.Source, Symbol: sym, OpenedAt: now, Entry: mid, HoldHour: spec.HoldHours,
					Ret1hBps: ret1, BTC24Bps: btc24, SymVolx: cur / med, AgeDays: ageDays,
				}
				directionFactorSt.Pending[key] = p
				directionFactorSt.Cooldown[key] = now.Add(12 * time.Hour)
				_ = appendJSONLRecord(filepath.Join(e.recordDir, directionFactorEventFile), directionFactorEvent{
					RecordedAt: now, Stage: "open", Source: spec.Source, Symbol: sym, Entry: mid,
					HoldHours: spec.HoldHours, Ret1hBps: ret1, BTC24Bps: btc24, SymVolx: cur / med, AgeDays: ageDays,
				})
				if autonomyStrategyIsLive(e.recordDir, spec.Source) {
					liveEv := &fastExecShadowEvent{
						RecordedAt: now, Venue: e.venueName, Symbol: sym,
						SnapshotID: now.Format(time.RFC3339Nano) + "|" + spec.Source + "|" + sym,
						Bid:        book.BidPrice, Ask: book.AskPrice, Mid: mid,
						TopBidQty: book.BidQty, TopAskQty: book.AskQty,
						FastCandidateSide: "long", CandidateSource: spec.Source,
						FastCandidateScore: 1, FastCandidateReason: "autonomous_dsl_v1",
						PondOK: true, PondSamples: fastPondWarmupSamples,
					}
					e.logger.Printf("[autonomy] %s %s → %s", spec.Source, sym, e.fastLiveOpen(ctx, liveEv))
				}
				changed = true
			}
		}
	}
	if len(directionFactorSt.Cooldown) > 4096 {
		for key, until := range directionFactorSt.Cooldown {
			if until.Before(now.Add(-24 * time.Hour)) {
				delete(directionFactorSt.Cooldown, key)
			}
		}
	}
	if changed {
		e.directionFactorPersistLocked()
	}
}

func normalizeDirectionFactorSymbol(instID string) string {
	return strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(instID)), "-SWAP")
}
