package app

import (
	"context"
	"strings"
	"sync"
	"time"
)

// borrow_allowlist_cache.go 是 A 方案「主动可借白名单」的状态层:拉取并缓存币安全仓杠杆的可借资产清单
// (/sapi/v1/margin/allAssets 的 isBorrowable),在选币**之前**就排除「根本不在白名单」的负 carry 候选。
//
// 与 borrow_availability_cache.go(反应式:撞 -3045 才缓存)互补:
//   - 本白名单(主动式):一次整批拉取,直接判定「永远借不到」(不在白名单 / isBorrowable=false)的币,
//     无需先撞错误,选币阶段一开始就剔除。
//   - 反应式缓存:管「在白名单但库存被借空」的临时性 -3045。
//   - 实时 MaxBorrowable(R29 监控期):最终精确兜底。
//
// 设计:长 TTL(白名单极少变动);fail-open——拉取失败/未拉过(loaded=false)时**绝不据此排除**,
// 避免一次只读查询失败误杀全部负 carry;独立锁,不与 platformRiskMu 抢临界区。

// borrowableAllowlistTTL 是白名单缓存有效期。可借清单变动很慢(偶有新增/下架),6 小时足够新鲜且省调用。
const borrowableAllowlistTTL = 6 * time.Hour

// borrowableAllowlistState 持有可借白名单缓存。零值即「未加载」(loaded=false → fail-open)。
type borrowableAllowlistState struct {
	mu        sync.RWMutex
	assets    map[string]bool // asset(大写)→ isBorrowable
	fetchedAt time.Time
	loaded    bool
}

// ensureBorrowableAllowlistFresh 在缓存为空或过期时,best-effort 拉取一次可借白名单。
// 仅在「借币容量守卫开启 且 负 carry 启用」时才有意义(正 carry/纯只读无需借币)。失败则保持原状(fail-open)。
func (e *FundingRateSniperEngine) ensureBorrowableAllowlistFresh(ctx context.Context) {
	if e == nil || e.client == nil {
		return
	}
	if !e.cfg.RiskConfig.BorrowCapacityCheckEnabled {
		return
	}
	if !liveFundingDirectionEnabled(e.cfg.StrategyConfig.LiveFundingDirectionMode, fundingCarryDirectionNegative) {
		return
	}
	now := e.serverNow()
	e.borrowableAllowlist.mu.RLock()
	fresh := e.borrowableAllowlist.loaded && now.Sub(e.borrowableAllowlist.fetchedAt) < borrowableAllowlistTTL
	e.borrowableAllowlist.mu.RUnlock()
	if fresh {
		return
	}
	assets, err := e.client.MarginAllAssets(ctx)
	if err != nil {
		if e.logger != nil {
			e.logger.Printf("拉取可借资产白名单失败(fail-open,本轮不据白名单排除负 carry): %v", err)
		}
		return
	}
	e.borrowableAllowlist.mu.Lock()
	e.borrowableAllowlist.assets = assets
	e.borrowableAllowlist.fetchedAt = now
	e.borrowableAllowlist.loaded = true
	e.borrowableAllowlist.mu.Unlock()
	if e.logger != nil {
		borrowable := 0
		for _, ok := range assets {
			if ok {
				borrowable++
			}
		}
		e.logger.Printf("已刷新币安可借资产白名单:共 %d 资产,其中可借 %d", len(assets), borrowable)
	}
}

// isAssetBorrowableByAllowlist 按白名单判定某资产是否可借。返回 (borrowable, known):
//   - known=false:白名单未加载(fail-open,调用方不应据此排除);
//   - known=true:white列表已加载,borrowable 为该资产在白名单中的 isBorrowable(未列出视为 false=不可借)。
func (e *FundingRateSniperEngine) isAssetBorrowableByAllowlist(asset string) (bool, bool) {
	a := strings.ToUpper(strings.TrimSpace(asset))
	if e == nil || a == "" {
		return false, false
	}
	e.borrowableAllowlist.mu.RLock()
	defer e.borrowableAllowlist.mu.RUnlock()
	if !e.borrowableAllowlist.loaded {
		return false, false
	}
	return e.borrowableAllowlist.assets[a], true
}
