package app

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lingxi/internal/lifecycle"
	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// venue_funding_conflicts.go 单所 funding 引擎与跨所引擎的双向冲突守卫(读台账,5s 缓存)。

const fundingHeldCacheTTL = 5 * time.Second

var (
	fundingHeldCacheMu sync.Mutex
	fundingHeldCache   = make(map[string]*fundingHeldCacheEntry)
)

type fundingHeldCacheEntry struct {
	at   time.Time
	held map[venue.Symbol]bool
}

func fundingExecutorVenueNames() []string {
	return []string{okx.VenueName, gate.VenueName}
}

func isFundingExecutorVenue(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == okx.VenueName || n == gate.VenueName
}

// isFundingSymbolHeldOnVenue 读某所 funding_positions.json,判断 symbol 是否在场。
func isFundingSymbolHeldOnVenue(baseRecordDir, venueName string, sym venue.Symbol) bool {
	dir := recordDirForVenue(EngineConfig{NetworkConfig: NetworkConfig{RecordDir: baseRecordDir}}, venueName)
	store, err := newVenueFundingPositionStore(dir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range ps {
		if p.Symbol == sym {
			return true
		}
	}
	return false
}

// isFundingSymbolHeldAnyVenue 任一 okx/gate 单所引擎台账在场则 true(跨所闸门用)。
func isFundingSymbolHeldAnyVenue(baseRecordDir string, sym venue.Symbol) bool {
	for _, v := range fundingExecutorVenueNames() {
		if isFundingSymbolHeldOnVenue(baseRecordDir, v, sym) {
			return true
		}
	}
	return false
}

func loadFundingHeldSet(baseRecordDir, venueName string) map[venue.Symbol]bool {
	out := make(map[venue.Symbol]bool)
	dir := recordDirForVenue(EngineConfig{NetworkConfig: NetworkConfig{RecordDir: baseRecordDir}}, venueName)
	store, err := newVenueFundingPositionStore(dir)
	if err != nil {
		return out
	}
	ps, err := store.load()
	if err != nil {
		return out
	}
	for _, p := range ps {
		out[p.Symbol] = true
	}
	return out
}

// fundingHeldSetCached 带 5s 缓存的台账读取(引擎选币循环用,防每轮重复 IO)。
func fundingHeldSetCached(baseRecordDir, venueName string) map[venue.Symbol]bool {
	key := strings.TrimSpace(baseRecordDir) + "|" + strings.ToLower(strings.TrimSpace(venueName))
	fundingHeldCacheMu.Lock()
	defer fundingHeldCacheMu.Unlock()
	if entry, ok := fundingHeldCache[key]; ok && time.Since(entry.at) < fundingHeldCacheTTL {
		return entry.held
	}
	held := loadFundingHeldSet(baseRecordDir, venueName)
	fundingHeldCache[key] = &fundingHeldCacheEntry{at: time.Now(), held: held}
	return held
}

// isCrossVenueSymbolHeld 读跨所台账,判断 symbol 是否在场(单所引擎开仓前守卫)。
func isCrossVenueSymbolHeld(baseRecordDir string, sym venue.Symbol) bool {
	dir := strings.TrimSpace(baseRecordDir)
	if dir == "" {
		dir = "records"
	}
	store, err := newCrossVenuePositionStore(dir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	want := normalizeConflictSymbol(sym)
	for _, p := range ps {
		if normalizeConflictSymbol(venue.Symbol(p.Symbol)) == want && p.Status != CrossVenueStatusClosed {
			return true
		}
	}
	return false
}

// normalizeConflictSymbol 把 BTCUSDT / BTC-USDT 等形统一为规范 venue.Symbol,供跨所↔单所冲突比对。
func normalizeConflictSymbol(sym venue.Symbol) venue.Symbol {
	s := strings.ToUpper(strings.TrimSpace(string(sym)))
	if s == "" {
		return venue.Symbol("")
	}
	if strings.Contains(s, "-") {
		return venue.Symbol(s)
	}
	canon, _ := binance.New(nil).ToCanonical(s, venue.MarketPerp)
	return canon
}

// isBinanceSymbolHeld 读币安遗留引擎 live_open_hedges.json,判断 symbol 是否仍有活跃对冲(跨所闸门用)。
func isBinanceSymbolHeld(baseRecordDir string, sym venue.Symbol) bool {
	dir := recordDirForVenue(EngineConfig{NetworkConfig: NetworkConfig{RecordDir: baseRecordDir}}, binance.VenueName)
	path := filepath.Join(dir, liveOpenHedgesStateName)
	state, err := loadPersistedOpenHedgeState(path)
	if err != nil {
		return false
	}
	want := normalizeConflictSymbol(sym)
	for _, h := range state.Hedges {
		if normalizeConflictSymbol(venue.Symbol(h.Symbol)) != want {
			continue
		}
		if lifecycle.IsTerminal(h.lifecycleOrDefault()) {
			continue
		}
		return true
	}
	return false
}

// filterTargetsByCrossVenueConflict 剔除跨所引擎已持有的 symbol(币安遗留引擎开仓前守卫)。
func (e *FundingRateSniperEngine) filterTargetsByCrossVenueConflict(targets []*MonitoredTarget) []*MonitoredTarget {
	if e == nil || len(targets) == 0 {
		return targets
	}
	baseRecordDir := e.cfg.NetworkConfig.RecordDir
	out := make([]*MonitoredTarget, 0, len(targets))
	for _, t := range targets {
		if t == nil {
			continue
		}
		sym := venue.Symbol(strings.ToUpper(strings.TrimSpace(t.Candidate.Symbol)))
		if sym == "" {
			continue
		}
		if isCrossVenueSymbolHeld(baseRecordDir, sym) {
			if e.logger != nil {
				e.logger.Printf("跨所冲突守卫:跳过 %s(跨所引擎已持有)", sym)
			}
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
