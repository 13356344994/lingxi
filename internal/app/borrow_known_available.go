package app

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// borrowKnownKey 是 venue+base 粒度的借币确认键(venue 小写, asset 大写)。
type borrowKnownKey struct {
	venue string
	asset string
}

// BorrowState 是 execution venue 上 base 资产的借币可得性三态(扫描闸 consult)。
type BorrowState int

const (
	// BorrowStateAvailable 窗口内 confirmed available=true。
	BorrowStateAvailable BorrowState = iota
	// BorrowStateUnavailable 窗口内 confirmed available=false,或 venue 配置禁用借币。
	BorrowStateUnavailable
	// BorrowStateUnknown 未探测/探测失败/记忆过期;扫描层 fail-open,由执行器 R29 预检兜底。
	BorrowStateUnknown
)

// BorrowKnownAvailableRegistry 维护各所 base 资产借币探测 confirmed 记忆(available/unavailable 对称 TTL)。
type BorrowKnownAvailableRegistry struct {
	mu            sync.RWMutex
	freshness     time.Duration
	confirmedAt   map[borrowKnownKey]time.Time
	unavailableAt map[borrowKnownKey]time.Time
}

// NewBorrowKnownAvailableRegistry 构造空 registry;freshness<=0 时默认 24h。
func NewBorrowKnownAvailableRegistry(freshness time.Duration) *BorrowKnownAvailableRegistry {
	if freshness <= 0 {
		freshness = 24 * time.Hour
	}
	return &BorrowKnownAvailableRegistry{
		freshness:     freshness,
		confirmedAt:   make(map[borrowKnownKey]time.Time),
		unavailableAt: make(map[borrowKnownKey]time.Time),
	}
}

// Freshness 返回当前记忆窗口。
func (r *BorrowKnownAvailableRegistry) Freshness() time.Duration {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.freshness
}

// SeedFromBorrowProbesJSONL 启动时从 records/borrow_probes.jsonl 暖启动(只读,不写 records)。
// 缺 scanned_at 的行以 seedNow 作为确认时间;探测失败(err 非空)行跳过,保持 unknown。
func (r *BorrowKnownAvailableRegistry) SeedFromBorrowProbesJSONL(recordDir string, seedNow time.Time) error {
	if r == nil {
		return nil
	}
	recordDir = strings.TrimSpace(recordDir)
	if recordDir == "" {
		return nil
	}
	path := filepath.Join(recordDir, crossVenueBorrowProbesLog)
	rows, err := readJSONLFile[borrowProbeJSONLRecord](path)
	if err != nil {
		return err
	}
	for _, rec := range rows {
		at := rec.ScannedAt.UTC()
		if at.IsZero() {
			at = seedNow.UTC()
		}
		r.observeJSONLRecord(rec, at)
	}
	return nil
}

func (r *BorrowKnownAvailableRegistry) observeJSONLRecord(rec borrowProbeJSONLRecord, at time.Time) {
	if r == nil || strings.TrimSpace(rec.Err) != "" {
		return
	}
	base := strings.ToUpper(strings.TrimSpace(rec.BaseAsset))
	if base == "" {
		base = baseAssetFromCanonicalSymbol(rec.Symbol)
	}
	if rec.Available {
		r.observeConfirmed(rec.Venue, base, at)
	} else {
		r.observeConfirmedUnavailable(rec.Venue, base, at)
	}
}

// ObserveProbes 用一轮 live/cache 借币探测结果刷新记忆。
// available=true → confirmed;available=false 且 err 空 → confirmed-unavailable;err 非空 → 跳过(保持 unknown)。
func (r *BorrowKnownAvailableRegistry) ObserveProbes(probes []CrossVenueBorrowProbe, at time.Time) {
	if r == nil || len(probes) == 0 {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	for _, p := range probes {
		if strings.TrimSpace(p.Err) != "" {
			continue
		}
		base := baseAssetFromCanonicalSymbol(p.Symbol)
		if p.Available {
			r.observeConfirmed(p.Venue, base, at)
		} else {
			r.observeConfirmedUnavailable(p.Venue, base, at)
		}
	}
}

func (r *BorrowKnownAvailableRegistry) observeConfirmed(venue, baseAsset string, at time.Time) {
	venue = strings.ToLower(strings.TrimSpace(venue))
	baseAsset = strings.ToUpper(strings.TrimSpace(baseAsset))
	if venue == "" || baseAsset == "" {
		return
	}
	key := borrowKnownKey{venue: venue, asset: baseAsset}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, ok := r.confirmedAt[key]; ok && prev.After(at) {
		return
	}
	r.confirmedAt[key] = at.UTC()
	delete(r.unavailableAt, key)
}

func (r *BorrowKnownAvailableRegistry) observeConfirmedUnavailable(venue, baseAsset string, at time.Time) {
	venue = strings.ToLower(strings.TrimSpace(venue))
	baseAsset = strings.ToUpper(strings.TrimSpace(baseAsset))
	if venue == "" || baseAsset == "" {
		return
	}
	key := borrowKnownKey{venue: venue, asset: baseAsset}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, ok := r.unavailableAt[key]; ok && prev.After(at) {
		return
	}
	r.unavailableAt[key] = at.UTC()
	delete(r.confirmedAt, key)
}

// IsBorrowable 报告某 execution venue 的 base 是否在 freshness 窗口内被 confirmed available。
func (r *BorrowKnownAvailableRegistry) IsBorrowable(venue, baseAsset string, now time.Time) bool {
	if r == nil {
		return false
	}
	venue = strings.ToLower(strings.TrimSpace(venue))
	baseAsset = strings.ToUpper(strings.TrimSpace(baseAsset))
	if venue == "" || baseAsset == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	key := borrowKnownKey{venue: venue, asset: baseAsset}
	r.mu.RLock()
	at, ok := r.confirmedAt[key]
	freshness := r.freshness
	r.mu.RUnlock()
	if !ok {
		return false
	}
	return now.Sub(at) <= freshness
}

// IsKnownUnavailable 报告某 execution venue 的 base 是否在 freshness 窗口内被 confirmed unavailable。
func (r *BorrowKnownAvailableRegistry) IsKnownUnavailable(venue, baseAsset string, now time.Time) bool {
	if r == nil {
		return false
	}
	venue = strings.ToLower(strings.TrimSpace(venue))
	baseAsset = strings.ToUpper(strings.TrimSpace(baseAsset))
	if venue == "" || baseAsset == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	key := borrowKnownKey{venue: venue, asset: baseAsset}
	r.mu.RLock()
	at, ok := r.unavailableAt[key]
	freshness := r.freshness
	r.mu.RUnlock()
	if !ok {
		return false
	}
	return now.Sub(at) <= freshness
}

// BorrowState 查询 venue+base 在 registry 中的三态(不含 borrowDisabled;由 perpSpotBorrowState 叠加)。
func (r *BorrowKnownAvailableRegistry) BorrowState(venue, baseAsset string, now time.Time) BorrowState {
	if r == nil {
		return BorrowStateUnknown
	}
	if r.IsBorrowable(venue, baseAsset, now) {
		return BorrowStateAvailable
	}
	if r.IsKnownUnavailable(venue, baseAsset, now) {
		return BorrowStateUnavailable
	}
	return BorrowStateUnknown
}

// CountFresh returns the number of venue+asset keys still inside the freshness window (for diagnostics/tests).
func (r *BorrowKnownAvailableRegistry) CountFresh(now time.Time) int {
	if r == nil {
		return 0
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, at := range r.confirmedAt {
		if now.Sub(at) <= r.freshness {
			n++
		}
	}
	return n
}
