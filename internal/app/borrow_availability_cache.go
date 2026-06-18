package app

import (
	"strings"
	"sync"
	"time"
)

// borrow_availability_cache.go 是 A 方案「借币可借性感知选币」的状态层:把 R29 在监控阶段查到的
// 「确定性借不到」(币安 -3045/-3006/-3008)按 base 资产缓存一段时间,供**海选阶段**在 Top-N 截断之前
// 剔除借不到的负 carry 候选。
//
// 为什么要在海选截断前剔除:扫描按 |费率| 排序后截断到 MaxTargets,HOME 这类极端负费率长尾币会霸占
// 唯一名额;但它在币安杠杆池根本借不到,监控完一定被 R29 剔除 → 本轮 0 开,可借的负 carry / 免借币的
// 正 carry 候选反而被截断挤掉。把「已知借不到」前置到截断之前,空槽就能让真正可执行的候选顶上。
//
// 设计:
//   - 纯缓存,不发起任何网络请求(海选热路径零新增 I/O);只消费 R29 既有查询的副产物。
//   - 带 TTL(借币池可能回补),过期即自动重试(候选重新进入监控,R29 再判定)。
//   - 与 symbolCooldowns 同由 platformRiskMu 守护,保持跨状态原子性;map 惰性初始化,兼容直接构造的测试。

// borrowUnavailableCacheTTL 是「确定性借不到」的缓存有效期。资金费每 1h/4h/8h 结算一次,20 分钟内
// 复扫会跳过该资产、把名额让给可执行候选;过期后重新探测,池子回补即可恢复。
const borrowUnavailableCacheTTL = 20 * time.Minute

// crossVenueBorrowProbeCacheTTL 跨所扫描借币探测结果缓存 TTL。
// 与 borrowUnavailableCacheTTL 对齐:资金费每 1h/4h/8h 结算,20 分钟内复扫跳过重复 I/O,
// 借币池回补后 TTL 过期自动重探。
const crossVenueBorrowProbeCacheTTL = 20 * time.Minute

type crossVenueBorrowProbeCacheKey struct {
	venue string
	asset string
}

type crossVenueBorrowProbeCacheEntry struct {
	probe    CrossVenueBorrowProbe
	cachedAt time.Time
}

// crossVenueBorrowProbeCache 缓存跨所扫描器的 FetchBorrowQuote 结果(venue+base 粒度)。
// 纯内存、带 TTL;命中时不触网,供每轮 scan 在 bounded live 预算内渐进覆盖负 carry 候选。
type crossVenueBorrowProbeCache struct {
	mu      sync.Mutex
	entries map[crossVenueBorrowProbeCacheKey]crossVenueBorrowProbeCacheEntry
}

func (c *crossVenueBorrowProbeCache) get(venue, asset string, now time.Time) (CrossVenueBorrowProbe, bool) {
	if c == nil {
		return CrossVenueBorrowProbe{}, false
	}
	venue = strings.ToLower(strings.TrimSpace(venue))
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if venue == "" || asset == "" {
		return CrossVenueBorrowProbe{}, false
	}
	key := crossVenueBorrowProbeCacheKey{venue: venue, asset: asset}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		return CrossVenueBorrowProbe{}, false
	}
	ent, ok := c.entries[key]
	if !ok {
		return CrossVenueBorrowProbe{}, false
	}
	if now.Sub(ent.cachedAt) >= crossVenueBorrowProbeCacheTTL {
		delete(c.entries, key)
		return CrossVenueBorrowProbe{}, false
	}
	return ent.probe, true
}

func (c *crossVenueBorrowProbeCache) put(venue, asset string, probe CrossVenueBorrowProbe, now time.Time) {
	if c == nil {
		return
	}
	venue = strings.ToLower(strings.TrimSpace(venue))
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if venue == "" || asset == "" {
		return
	}
	key := crossVenueBorrowProbeCacheKey{venue: venue, asset: asset}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[crossVenueBorrowProbeCacheKey]crossVenueBorrowProbeCacheEntry)
	}
	c.entries[key] = crossVenueBorrowProbeCacheEntry{probe: probe, cachedAt: now}
}

func (c *crossVenueBorrowProbeCache) allValid(now time.Time) []CrossVenueBorrowProbe {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) == 0 {
		return nil
	}
	out := make([]CrossVenueBorrowProbe, 0, len(c.entries))
	for key, ent := range c.entries {
		if now.Sub(ent.cachedAt) >= crossVenueBorrowProbeCacheTTL {
			delete(c.entries, key)
			continue
		}
		out = append(out, ent.probe)
	}
	return out
}

// markBorrowAssetUnavailable 记录某 base 资产此刻确定性借不到(TTL 内海选阶段跳过)。资产名空则忽略。
// 由 R29 queryMaxBorrowable 命中确定性借不到错误时调用。线程安全(自取 platformRiskMu)。
func (e *FundingRateSniperEngine) markBorrowAssetUnavailable(asset string) {
	a := strings.ToUpper(strings.TrimSpace(asset))
	if e == nil || a == "" {
		return
	}
	until := e.serverNow().Add(borrowUnavailableCacheTTL)
	e.platformRiskMu.Lock()
	if e.borrowUnavailableUntil == nil {
		e.borrowUnavailableUntil = make(map[string]time.Time)
	}
	e.borrowUnavailableUntil[a] = until
	e.platformRiskMu.Unlock()
}

// isBorrowAssetKnownUnavailable 报告某 base 资产是否在 TTL 内被标记为确定性借不到。过期项顺手清理。
// 线程安全(自取 platformRiskMu)。
func (e *FundingRateSniperEngine) isBorrowAssetKnownUnavailable(asset string) bool {
	a := strings.ToUpper(strings.TrimSpace(asset))
	if e == nil || a == "" {
		return false
	}
	now := e.serverNow()
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	until, ok := e.borrowUnavailableUntil[a]
	if !ok {
		return false
	}
	if !until.After(now) {
		delete(e.borrowUnavailableUntil, a)
		return false
	}
	return true
}

// dropBorrowUnavailableNegativeCarry 在海选 Top-N 截断之前,剔除「base 资产借不到」的负 carry 候选,
// 两条来源:① 主动白名单(根本不在可借清单 / isBorrowable=false);② 反应式缓存(近期撞 -3045 等确定性借不到)。
// 正 carry(免借币)与可借性未知者恒放行(fail-open)。纯过滤,不触网。返回过滤后的候选 + 被剔除数(供日志)。
func (e *FundingRateSniperEngine) dropBorrowUnavailableNegativeCarry(candidates []FundingCandidate) ([]FundingCandidate, int) {
	if e == nil || len(candidates) == 0 {
		return candidates, 0
	}
	kept := make([]FundingCandidate, 0, len(candidates))
	dropped := 0
	for _, c := range candidates {
		if resolvedFundingCarryDirection(c.Direction, c.EstimatedRate) != fundingCarryDirectionNegative {
			kept = append(kept, c) // 正 carry 免借币,恒放行
			continue
		}
		asset := strings.ToUpper(strings.TrimSpace(c.SpotBaseAsset))
		if reason := e.borrowExclusionReason(asset); reason != "" {
			dropped++
			if e.logger != nil {
				e.logger.Printf("海选剔除借不到的负 carry 候选 %s(base=%s %s,名额让给可执行候选)", c.Symbol, asset, reason)
			}
			continue
		}
		kept = append(kept, c)
	}
	return kept, dropped
}

// borrowExclusionReason 返回某 base 资产「应在选币阶段排除」的原因串;可借/未知则返回空串(放行)。
// 主动白名单优先(永远借不到),其次反应式缓存(近期确定性借不到,TTL 内)。
func (e *FundingRateSniperEngine) borrowExclusionReason(asset string) string {
	if borrowable, known := e.isAssetBorrowableByAllowlist(asset); known && !borrowable {
		return "不在币安可借白名单"
	}
	if e.isBorrowAssetKnownUnavailable(asset) {
		return "近期确定性借不到,TTL内跳过"
	}
	return ""
}
