package app

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue"
)

const (
	rotationRejectDelisting = "delisting"
	defaultDelistCacheTTL   = time.Hour
)

// DelistGuardConfig 控制统一轮动下市风险守卫。
type DelistGuardConfig struct {
	Enabled  bool
	CacheTTL time.Duration
}

// DefaultDelistGuardConfig 返回默认开启、1 小时 TTL 的下市守卫配置。
func DefaultDelistGuardConfig() DelistGuardConfig {
	return DelistGuardConfig{
		Enabled:  true,
		CacheTTL: defaultDelistCacheTTL,
	}
}

// DelistGuardConfigFromUnifiedRotation 从 core unified_rotation 配置映射守卫参数。
func DelistGuardConfigFromUnifiedRotation(cfg core.UnifiedRotationConfig) DelistGuardConfig {
	out := DefaultDelistGuardConfig()
	out.Enabled = cfg.DelistGuardEnabled
	if cfg.DelistStatusCacheTTL > 0 {
		out.CacheTTL = cfg.DelistStatusCacheTTL
	}
	return out
}

func normalizeDelistGuardConfig(cfg DelistGuardConfig) DelistGuardConfig {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = defaultDelistCacheTTL
	}
	return cfg
}

type delistStatusEntry struct {
	nonTrading   bool
	rawStatus    string
	deliveryTime time.Time
}

type delistVenueCache struct {
	byKey     map[string]delistStatusEntry // key = market/symbol
	loaded    bool
	fetchedAt time.Time
}

// DelistGuard 缓存各 venue 的 instrument 交易状态,供轮动开仓门禁与持仓主动平仓。
// 设计:fail-open——缓存未加载/拉取失败/未知 symbol 不拦截;仅对 NonTrading=true 的明确信号动作。
type DelistGuard struct {
	venues map[string]venue.Venue
	cfg    DelistGuardConfig
	logger *log.Logger
	nowFn  func() time.Time

	mu      sync.RWMutex
	byVenue map[string]*delistVenueCache
}

// NewDelistGuard 构造下市守卫;venues 为 nil 时 guard 恒 no-op。
func NewDelistGuard(venues map[string]venue.Venue, cfg DelistGuardConfig, logger *log.Logger) *DelistGuard {
	if logger == nil {
		logger = log.Default()
	}
	cfg = normalizeDelistGuardConfig(cfg)
	return &DelistGuard{
		venues:  venues,
		cfg:     cfg,
		logger:  logger,
		nowFn:   time.Now,
		byVenue: make(map[string]*delistVenueCache),
	}
}

func delistCacheKey(m venue.Market, sym string) string {
	return m.String() + "/" + strings.ToUpper(strings.TrimSpace(sym))
}

func (g *DelistGuard) enabled() bool {
	return g != nil && g.cfg.Enabled
}

// Refresh 在 TTL 过期时 best-effort 刷新各 venue instrument 状态缓存。
func (g *DelistGuard) Refresh(ctx context.Context) {
	if !g.enabled() || len(g.venues) == 0 {
		return
	}
	now := g.nowFn().UTC()
	for venueName, v := range g.venues {
		if v == nil {
			continue
		}
		if g.venueFresh(venueName, now) {
			continue
		}
		g.refreshVenue(ctx, venueName, v, now)
	}
}

func (g *DelistGuard) venueFresh(venueName string, now time.Time) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	c := g.byVenue[venueName]
	return c != nil && c.loaded && now.Sub(c.fetchedAt) < g.cfg.CacheTTL
}

func (g *DelistGuard) refreshVenue(ctx context.Context, venueName string, v venue.Venue, now time.Time) {
	instrs, err := v.FetchInstruments(ctx)
	if err != nil {
		if g.logger != nil {
			g.logger.Printf("[delist-guard] 拉取 %s instruments 失败(fail-open): %v", venueName, err)
		}
		return
	}
	byKey := make(map[string]delistStatusEntry, len(instrs))
	for _, in := range instrs {
		sym := strings.ToUpper(strings.TrimSpace(string(in.Symbol)))
		if sym == "" {
			continue
		}
		byKey[delistCacheKey(in.Market, sym)] = delistStatusEntry{
			nonTrading:   in.NonTrading,
			rawStatus:    in.RawTradingStatus,
			deliveryTime: in.DeliveryTime,
		}
	}
	g.mu.Lock()
	g.byVenue[venueName] = &delistVenueCache{
		byKey:     byKey,
		loaded:    true,
		fetchedAt: now,
	}
	g.mu.Unlock()
	if g.logger != nil {
		nonTrading := 0
		for _, e := range byKey {
			if e.nonTrading {
				nonTrading++
			}
		}
		g.logger.Printf("[delist-guard] 已刷新 %s instrument 状态: total=%d non_trading=%d", venueName, len(byKey), nonTrading)
	}
}

// IsNonTrading 查询某 venue+symbol+market 是否被明确标记为非交易状态。
// 返回 (nonTrading, known): known=false 表示缓存未命中(fail-open,不应拦截)。
func (g *DelistGuard) IsNonTrading(venueName, symbol string, m venue.Market) (nonTrading, known bool) {
	if !g.enabled() {
		return false, false
	}
	venueName = strings.TrimSpace(venueName)
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if venueName == "" || sym == "" {
		return false, false
	}
	key := delistCacheKey(m, sym)
	g.mu.RLock()
	defer g.mu.RUnlock()
	c := g.byVenue[venueName]
	if c == nil || !c.loaded {
		return false, false
	}
	e, ok := c.byKey[key]
	if !ok {
		return false, false
	}
	return e.nonTrading, true
}

type rotationDelistLeg struct {
	Venue  string
	Market venue.Market
}

func (g *DelistGuard) rejectReasonForLegs(symbol string, legs []rotationDelistLeg) string {
	if !g.enabled() || len(legs) == 0 {
		return ""
	}
	sym := strings.TrimSpace(symbol)
	for _, leg := range legs {
		if nonTrading, known := g.IsNonTrading(leg.Venue, sym, leg.Market); nonTrading && known {
			return rotationRejectDelisting
		}
	}
	return ""
}

func delistLegsFromOpportunity(opp UnifiedOpportunity) []rotationDelistLeg {
	if len(opp.Legs) == 0 {
		return nil
	}
	out := make([]rotationDelistLeg, 0, len(opp.Legs))
	for _, leg := range opp.Legs {
		v := strings.TrimSpace(leg.Venue)
		if v == "" {
			continue
		}
		m := venue.MarketSpot
		if strings.EqualFold(strings.TrimSpace(leg.Market), "perp") {
			m = venue.MarketPerp
		}
		out = append(out, rotationDelistLeg{Venue: v, Market: m})
	}
	return out
}

func (g *DelistGuard) rejectReasonForOpportunity(opp UnifiedOpportunity) string {
	return g.rejectReasonForLegs(opp.Symbol, delistLegsFromOpportunity(opp))
}

func (g *DelistGuard) rejectReasonForHeld(rec rotationHeldRecord) string {
	sym := strings.TrimSpace(rec.Symbol)
	if sym == "" {
		return ""
	}
	return g.rejectReasonForLegs(sym, rec.delistLegs)
}

// applyDelistOpenGateToCandidates 将命中下市/非交易状态的候选改为 reject(leaderboard 可见),并清零 net EV 防开仓。
func applyDelistOpenGateToCandidates(guard *DelistGuard, candidates []UnifiedOpportunity) []UnifiedOpportunity {
	if guard == nil || !guard.enabled() {
		return candidates
	}
	out := make([]UnifiedOpportunity, len(candidates))
	for i, opp := range candidates {
		out[i] = opp
		if len(opp.RejectReasons) > 0 || opp.ExpectedNetEVUSDT <= 0 {
			continue
		}
		if reason := guard.rejectReasonForOpportunity(opp); reason != "" {
			out[i].RejectReasons = append([]string{reason}, out[i].RejectReasons...)
			out[i].ExpectedNetEVUSDT = 0
			out[i].RiskScore = 1
			out[i].DataQualityScore = 0
		}
	}
	return out
}

// appendDelistRejectedCandidates 为扫描 symbol×venue 组合补充 delisting reject 行(尚未被 scanner 产出时)。
func appendDelistRejectedCandidates(
	guard *DelistGuard,
	snapshotID string,
	symbols []string,
	existing []UnifiedOpportunity,
) []UnifiedOpportunity {
	if guard == nil || !guard.enabled() || len(symbols) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing))
	for _, opp := range existing {
		seen[opp.ID] = struct{}{}
	}
	out := existing
	for _, venueName := range crossVenueExecutorVenues {
		for _, sym := range symbols {
			sym = strings.TrimSpace(sym)
			if sym == "" {
				continue
			}
			legs := []rotationDelistLeg{
				{Venue: venueName, Market: venue.MarketSpot},
				{Venue: venueName, Market: venue.MarketPerp},
			}
			if guard.rejectReasonForLegs(sym, legs) == "" {
				continue
			}
			id := unifiedOpportunityID(UnifiedStrategySingleVenueSpotPerp, sym, snapshotID, venueName, venueName)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, singleVenueSpotPerpRejectedOpportunity(snapshotID, sym, venueName, rotationRejectDelisting))
		}
	}
	return out
}

// manageBindingDelistCloses 对 binding 的在场持仓检查下市状态;走 verified-close 主动平仓。
func (g *DelistGuard) manageBindingDelistCloses(ctx context.Context, binding *rotationExecutorBinding, live bool) {
	if !g.enabled() || binding == nil || binding.heldFn == nil {
		return
	}
	for _, rec := range binding.heldFn() {
		reason := g.rejectReasonForHeld(rec)
		if reason == "" {
			continue
		}
		if g.logger != nil {
			g.logger.Printf("[delist-guard] 检测到下市/非交易状态 symbol=%s strategy=%s opp=%s,主动平仓",
				rec.Symbol, rec.StrategyType, rec.OpportunityID)
		}
		_ = live // 下市平仓为硬安全动作,不受 runner 暂停影响
		if binding.closeVerifiedFn == nil {
			continue
		}
		if err := binding.closeVerifiedFn(ctx, rec.OpportunityID, rec.Symbol, reason); err != nil && g.logger != nil {
			g.logger.Printf("[delist-guard] 主动平仓失败(已吞掉) opp=%s: %v", rec.OpportunityID, err)
		}
	}
}
