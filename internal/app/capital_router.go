package app

import (
	"fmt"
	"sort"
	"strings"

	"lingxi/internal/venue"
)

const capitalRouterEpsilon = 1e-9

// CapitalUnfundableReasonCrossVenueGap 是跨所资金缺口导致 unfundable 的可统计前缀(日志/快照解析可 grep)。
const CapitalUnfundableReasonCrossVenueGap = "cross_venue_funding_gap"

// WalletKind 标识交易所内钱包类型。
type WalletKind string

const (
	WalletKindSpot    WalletKind = "spot"
	WalletKindFutures WalletKind = "futures"
	WalletKindMargin  WalletKind = "margin"
)

var capitalRouterWalletKinds = []WalletKind{WalletKindSpot, WalletKindFutures, WalletKindMargin}

// VenueWalletBalances 记录各所各钱包当前可用 USDT。
type VenueWalletBalances map[string]map[WalletKind]float64

// CapitalRouterConfig 资金路由决策层配置。
type CapitalRouterConfig struct {
	PerpLeverage       float64            // 默认 2
	MarginSafetyFactor float64            // 默认 1.5
	ReserveUSDTByVenue map[string]float64 // 每所不可动用保留金
	DefaultReserveUSDT float64
	// UnifiedWalletVenues 标记统一账户所(venue→true):现货/永续/杠杆共用单池,不规划所内划转。
	// 由 CapitalRouterConfigWithVenues 从 venue.Capabilities().UnifiedWallet 填充;单测可显式设置。
	UnifiedWalletVenues map[string]bool
}

// DefaultCapitalRouterConfig 返回默认资金路由配置。
func DefaultCapitalRouterConfig() CapitalRouterConfig {
	return CapitalRouterConfig{
		PerpLeverage:       2,
		MarginSafetyFactor: 1.5,
		ReserveUSDTByVenue: map[string]float64{},
		DefaultReserveUSDT: 0,
	}
}

// WalletTransfer 所内钱包划转（安全、秒到）。
type WalletTransfer struct {
	Venue          string
	From, To       WalletKind
	AmountUSDT     float64
	Reason         string
	OpportunityIDs []string // 依赖此划转备资的机会 ID;执行失败时阻断对应 OPEN
}

// CrossVenueRebalanceSuggestion 跨所缺口建议（仅建议，不执行）。
type CrossVenueRebalanceSuggestion struct {
	FromVenue, ToVenue string
	AmountUSDT         float64
	Reason             string
}

// CapitalRoutingPlan 资金路由计划：所内划转 + 跨所建议 + 无法备资机会。
type CapitalRoutingPlan struct {
	WithinVenueTransfers  []WalletTransfer
	CrossVenueSuggestions []CrossVenueRebalanceSuggestion
	Unfundable            []string // 机会 ID + 原因
}

// PlanCapitalRouting 纯函数：算出为给本轮选中机会的每条腿备资金，所需的钱包划转与跨所建议。
func PlanCapitalRouting(chosen []UnifiedOpportunity, balances VenueWalletBalances, cfg CapitalRouterConfig) CapitalRoutingPlan {
	cfg = normalizeCapitalRouterConfig(cfg)
	aggregated, perOpp := aggregateCapitalRequirements(chosen, cfg)

	working := cloneVenueWalletBalances(balances)
	lockVenueReserves(working, cfg)
	preTransfer := cloneVenueWalletBalances(working)

	var transfers []WalletTransfer
	for venue, req := range aggregated {
		if cfg.isUnifiedVenue(venue) {
			continue
		}
		transfers = append(transfers, planWithinVenueTransfers(venue, req, working[venue])...)
	}
	attachTransferOpportunityIDs(chosen, perOpp, preTransfer, transfers, cfg)
	sortWalletTransfers(transfers)
	applyWithinVenueTransfers(working, transfers)

	crossSuggestions := planCrossVenueSuggestions(aggregated, working)
	unfundable := markUnfundableOpportunities(chosen, perOpp, working, crossSuggestions, cfg)

	return CapitalRoutingPlan{
		WithinVenueTransfers:  transfers,
		CrossVenueSuggestions: crossSuggestions,
		Unfundable:            unfundable,
	}
}

// UnifiedWalletVenuesFromVenues 从已注册 venue 适配器提取 UnifiedWallet 能力。
func UnifiedWalletVenuesFromVenues(venues map[string]venue.Venue) map[string]bool {
	out := map[string]bool{}
	for name, v := range venues {
		if v == nil {
			continue
		}
		if v.Capabilities().UnifiedWallet {
			out[name] = true
		}
	}
	return out
}

// CapitalRouterConfigWithVenues 将 venue 能力合并进资金路由配置(统一账户标记等)。
func CapitalRouterConfigWithVenues(cfg CapitalRouterConfig, venues map[string]venue.Venue) CapitalRouterConfig {
	cfg.UnifiedWalletVenues = UnifiedWalletVenuesFromVenues(venues)
	return normalizeCapitalRouterConfig(cfg)
}

func normalizeCapitalRouterConfig(cfg CapitalRouterConfig) CapitalRouterConfig {
	if cfg.PerpLeverage <= 0 {
		cfg.PerpLeverage = 2
	}
	if cfg.MarginSafetyFactor <= 0 {
		cfg.MarginSafetyFactor = 1.5
	}
	if cfg.ReserveUSDTByVenue == nil {
		cfg.ReserveUSDTByVenue = map[string]float64{}
	}
	return cfg
}

func (cfg CapitalRouterConfig) isUnifiedVenue(venue string) bool {
	if cfg.UnifiedWalletVenues != nil {
		return cfg.UnifiedWalletVenues[venue]
	}
	switch venue {
	case "bybit", "okx", "gate":
		return true
	default:
		return false
	}
}

func (cfg CapitalRouterConfig) venueReserveUSDT(venue string) float64 {
	if reserve, ok := cfg.ReserveUSDTByVenue[venue]; ok {
		return capitalRouterMax(0, reserve)
	}
	return capitalRouterMax(0, cfg.DefaultReserveUSDT)
}

type venueWalletRequirements map[WalletKind]float64

func aggregateCapitalRequirements(chosen []UnifiedOpportunity, cfg CapitalRouterConfig) (map[string]venueWalletRequirements, map[string]map[string]venueWalletRequirements) {
	aggregated := map[string]venueWalletRequirements{}
	perOpp := map[string]map[string]venueWalletRequirements{}

	for _, opp := range chosen {
		reqByVenue := aggregateOpportunityRequirements(opp, cfg)
		perOpp[opp.ID] = reqByVenue
		for venue, walletReq := range reqByVenue {
			if aggregated[venue] == nil {
				aggregated[venue] = venueWalletRequirements{}
			}
			for wallet, amount := range walletReq {
				addWalletRequirement(aggregated[venue], wallet, amount)
			}
		}
	}
	return aggregated, perOpp
}

func aggregateOpportunityRequirements(opp UnifiedOpportunity, cfg CapitalRouterConfig) map[string]venueWalletRequirements {
	reqByVenue := map[string]venueWalletRequirements{}
	perpLegsByVenue := map[string][]UnifiedOpportunityLeg{}

	for _, leg := range opp.Legs {
		venue := leg.Venue
		if reqByVenue[venue] == nil {
			reqByVenue[venue] = venueWalletRequirements{}
		}
		switch strings.ToLower(strings.TrimSpace(leg.Market)) {
		case "spot":
			side := strings.ToLower(strings.TrimSpace(leg.Side))
			switch side {
			case "buy":
				addWalletRequirement(reqByVenue[venue], WalletKindSpot, leg.Notional)
			case "sell", "short":
				addWalletRequirement(reqByVenue[venue], WalletKindMargin, leg.Notional)
			}
		case "perp":
			perpLegsByVenue[venue] = append(perpLegsByVenue[venue], leg)
		}
	}
	for venue, legs := range perpLegsByVenue {
		if reqByVenue[venue] == nil {
			reqByVenue[venue] = venueWalletRequirements{}
		}
		addPerpMarginRequirements(reqByVenue[venue], venue, legs, opp.CapitalRequiredByVenue, cfg)
	}
	return reqByVenue
}

func addPerpMarginRequirements(req venueWalletRequirements, venue string, legs []UnifiedOpportunityLeg, capitalByVenue map[string]float64, cfg CapitalRouterConfig) {
	if len(legs) == 0 {
		return
	}
	totalNotional := 0.0
	for _, leg := range legs {
		totalNotional += capitalRouterMax(0, leg.Notional)
	}
	venueCapital := 0.0
	if capitalByVenue != nil {
		venueCapital = capitalRouterMax(0, capitalByVenue[venue])
	}
	if venueCapital > capitalRouterEpsilon && totalNotional > capitalRouterEpsilon {
		for _, leg := range legs {
			share := venueCapital * (leg.Notional / totalNotional)
			addWalletRequirement(req, WalletKindFutures, share)
		}
		return
	}
	for _, leg := range legs {
		margin := perpLegFallbackMarginUSDT(leg.Notional, cfg)
		addWalletRequirement(req, WalletKindFutures, margin)
	}
}

func perpLegFallbackMarginUSDT(notional float64, cfg CapitalRouterConfig) float64 {
	if notional <= 0 {
		return 0
	}
	return notional / cfg.PerpLeverage * cfg.MarginSafetyFactor
}

func addWalletRequirement(req venueWalletRequirements, wallet WalletKind, amount float64) {
	if amount <= capitalRouterEpsilon {
		return
	}
	req[wallet] += amount
}

func cloneVenueWalletBalances(src VenueWalletBalances) VenueWalletBalances {
	dst := VenueWalletBalances{}
	for venue, wallets := range src {
		dst[venue] = map[WalletKind]float64{}
		for wallet, amount := range wallets {
			dst[venue][wallet] = capitalRouterMax(0, amount)
		}
	}
	return dst
}

func lockVenueReserves(working VenueWalletBalances, cfg CapitalRouterConfig) {
	for venue, wallets := range working {
		reserve := cfg.venueReserveUSDT(venue)
		if reserve <= capitalRouterEpsilon {
			continue
		}
		for _, wallet := range capitalRouterWalletKinds {
			if reserve <= capitalRouterEpsilon {
				break
			}
			available := capitalRouterMax(0, wallets[wallet])
			lock := capitalRouterMin(available, reserve)
			wallets[wallet] -= lock
			reserve -= lock
		}
	}
}

func planWithinVenueTransfers(venue string, required venueWalletRequirements, wallets map[WalletKind]float64) []WalletTransfer {
	if len(required) == 0 {
		return nil
	}
	if wallets == nil {
		wallets = map[WalletKind]float64{}
	}

	sim := map[WalletKind]float64{}
	for _, wallet := range capitalRouterWalletKinds {
		sim[wallet] = capitalRouterMax(0, wallets[wallet])
	}

	var transfers []WalletTransfer
	for {
		var deficits []WalletKind
		for _, wallet := range capitalRouterWalletKinds {
			if required[wallet]-sim[wallet] > capitalRouterEpsilon {
				deficits = append(deficits, wallet)
			}
		}
		if len(deficits) == 0 {
			break
		}
		sort.Slice(deficits, func(i, j int) bool { return deficits[i] < deficits[j] })

		progress := false
		for _, to := range deficits {
			need := required[to] - sim[to]
			if need <= capitalRouterEpsilon {
				continue
			}
			for _, from := range capitalRouterWalletKinds {
				if from == to {
					continue
				}
				surplus := sim[from] - required[from]
				if surplus <= capitalRouterEpsilon {
					continue
				}
				amount := capitalRouterMin(need, surplus)
				if amount <= capitalRouterEpsilon {
					continue
				}
				sim[from] -= amount
				sim[to] += amount
				transfers = append(transfers, WalletTransfer{
					Venue:      venue,
					From:       from,
					To:         to,
					AmountUSDT: amount,
					Reason:     fmt.Sprintf("rebalance %s wallet for opportunity funding", to),
				})
				need -= amount
				progress = true
				if need <= capitalRouterEpsilon {
					break
				}
			}
		}
		if !progress {
			break
		}
	}
	return transfers
}

func attachTransferOpportunityIDs(
	chosen []UnifiedOpportunity,
	perOpp map[string]map[string]venueWalletRequirements,
	preTransfer VenueWalletBalances,
	transfers []WalletTransfer,
	cfg CapitalRouterConfig,
) {
	if len(transfers) == 0 {
		return
	}
	for i := range transfers {
		tr := &transfers[i]
		if cfg.isUnifiedVenue(tr.Venue) {
			continue
		}
		wallets := preTransfer[tr.Venue]
		if wallets == nil {
			wallets = map[WalletKind]float64{}
		}
		seen := map[string]struct{}{}
		for _, opp := range chosen {
			reqByVenue := perOpp[opp.ID]
			if reqByVenue == nil {
				continue
			}
			req := reqByVenue[tr.Venue]
			if req == nil {
				continue
			}
			need := req[tr.To]
			if need <= capitalRouterEpsilon {
				continue
			}
			have := capitalRouterMax(0, wallets[tr.To])
			if have+capitalRouterEpsilon >= need {
				continue
			}
			if _, ok := seen[opp.ID]; ok {
				continue
			}
			seen[opp.ID] = struct{}{}
			tr.OpportunityIDs = append(tr.OpportunityIDs, opp.ID)
		}
		sort.Strings(tr.OpportunityIDs)
	}
}

func applyWithinVenueTransfers(working VenueWalletBalances, transfers []WalletTransfer) {
	for _, tr := range transfers {
		if working[tr.Venue] == nil {
			working[tr.Venue] = map[WalletKind]float64{}
		}
		working[tr.Venue][tr.From] -= tr.AmountUSDT
		working[tr.Venue][tr.To] += tr.AmountUSDT
	}
}

func venueTotalBalance(wallets map[WalletKind]float64) float64 {
	total := 0.0
	for _, wallet := range capitalRouterWalletKinds {
		total += capitalRouterMax(0, wallets[wallet])
	}
	return total
}

func venueTotalRequired(required venueWalletRequirements) float64 {
	total := 0.0
	for _, amount := range required {
		total += amount
	}
	return total
}

func planCrossVenueSuggestions(aggregated map[string]venueWalletRequirements, working VenueWalletBalances) []CrossVenueRebalanceSuggestion {
	type venueBalance struct {
		venue   string
		deficit float64
		surplus float64
	}
	var deficits, surpluses []venueBalance

	for venue, req := range aggregated {
		wallets := working[venue]
		if wallets == nil {
			wallets = map[WalletKind]float64{}
		}
		totalRequired := venueTotalRequired(req)
		totalAvailable := venueTotalBalance(wallets)
		if totalRequired-totalAvailable > capitalRouterEpsilon {
			deficits = append(deficits, venueBalance{venue: venue, deficit: totalRequired - totalAvailable})
		} else if totalAvailable-totalRequired > capitalRouterEpsilon {
			surpluses = append(surpluses, venueBalance{venue: venue, surplus: totalAvailable - totalRequired})
		}
	}

	sort.Slice(deficits, func(i, j int) bool { return deficits[i].venue < deficits[j].venue })
	sort.Slice(surpluses, func(i, j int) bool { return surpluses[i].venue < surpluses[j].venue })

	var suggestions []CrossVenueRebalanceSuggestion
	for _, def := range deficits {
		remaining := def.deficit
		for i := range surpluses {
			if remaining <= capitalRouterEpsilon {
				break
			}
			if surpluses[i].surplus <= capitalRouterEpsilon {
				continue
			}
			amount := capitalRouterMin(remaining, surpluses[i].surplus)
			if amount <= capitalRouterEpsilon {
				continue
			}
			suggestions = append(suggestions, CrossVenueRebalanceSuggestion{
				FromVenue:  surpluses[i].venue,
				ToVenue:    def.venue,
				AmountUSDT: amount,
				Reason:     fmt.Sprintf("venue %s total USDT short after within-venue rebalance", def.venue),
			})
			surpluses[i].surplus -= amount
			remaining -= amount
		}
	}
	sortCrossVenueSuggestions(suggestions)
	return suggestions
}

type crossVenueInflowDetail struct {
	suggestions []CrossVenueRebalanceSuggestion
	totalUSDT   float64
}

func indexCrossVenueInflowSuggestions(suggestions []CrossVenueRebalanceSuggestion) map[string]crossVenueInflowDetail {
	out := map[string]crossVenueInflowDetail{}
	for _, s := range suggestions {
		if s.ToVenue == "" || s.AmountUSDT <= capitalRouterEpsilon {
			continue
		}
		d := out[s.ToVenue]
		d.suggestions = append(d.suggestions, s)
		d.totalUSDT += s.AmountUSDT
		out[s.ToVenue] = d
	}
	return out
}

func formatCrossVenueFundingGapReason(toVenue string, detail crossVenueInflowDetail) string {
	parts := make([]string, 0, len(detail.suggestions))
	for _, s := range detail.suggestions {
		parts = append(parts, fmt.Sprintf("%s→%s:%.4f", s.FromVenue, s.ToVenue, s.AmountUSDT))
	}
	sort.Strings(parts)
	legacy := fmt.Sprintf("requires cross-venue rebalance to %s (not auto-funded)", toVenue)
	return fmt.Sprintf(
		"%s:to=%s:suggested=[%s]:total=%.4f:not_auto_executed; %s",
		CapitalUnfundableReasonCrossVenueGap,
		toVenue,
		strings.Join(parts, ","),
		detail.totalUSDT,
		legacy,
	)
}

func markUnfundableOpportunities(
	chosen []UnifiedOpportunity,
	perOpp map[string]map[string]venueWalletRequirements,
	working VenueWalletBalances,
	crossSuggestions []CrossVenueRebalanceSuggestion,
	cfg CapitalRouterConfig,
) []string {
	crossInflowByVenue := indexCrossVenueInflowSuggestions(crossSuggestions)

	available := cloneVenueWalletBalances(working)
	var unfundable []string

	for _, opp := range chosen {
		reqByVenue := perOpp[opp.ID]
		reason := opportunityFundingGapReason(reqByVenue, available, crossInflowByVenue, cfg)
		if reason == "" {
			consumeOpportunityFunding(reqByVenue, available, cfg)
			continue
		}
		unfundable = append(unfundable, opp.ID+": "+reason)
	}
	return unfundable
}

func opportunityFundingGapReason(
	reqByVenue map[string]venueWalletRequirements,
	available VenueWalletBalances,
	crossInflowByVenue map[string]crossVenueInflowDetail,
	cfg CapitalRouterConfig,
) string {
	for venue, walletReq := range reqByVenue {
		if detail, needsCross := crossInflowByVenue[venue]; needsCross {
			return formatCrossVenueFundingGapReason(venue, detail)
		}
		wallets := available[venue]
		if wallets == nil {
			wallets = map[WalletKind]float64{}
		}

		if cfg.isUnifiedVenue(venue) {
			totalNeed := venueTotalRequired(walletReq)
			if totalNeed <= capitalRouterEpsilon {
				continue
			}
			totalHave := venueTotalBalance(wallets)
			if totalHave+capitalRouterEpsilon >= totalNeed {
				continue
			}
			return fmt.Sprintf("insufficient %s unified USDT (need %.4f, have %.4f)", venue, totalNeed, totalHave)
		}

		for _, wallet := range capitalRouterWalletKinds {
			need := walletReq[wallet]
			if need <= capitalRouterEpsilon {
				continue
			}
			have := capitalRouterMax(0, wallets[wallet])
			if have+capitalRouterEpsilon >= need {
				continue
			}
			return fmt.Sprintf("insufficient %s/%s USDT (need %.4f, have %.4f)", venue, wallet, need, have)
		}
	}
	return ""
}

func consumeOpportunityFunding(reqByVenue map[string]venueWalletRequirements, available VenueWalletBalances, cfg CapitalRouterConfig) {
	for venue, walletReq := range reqByVenue {
		if available[venue] == nil {
			available[venue] = map[WalletKind]float64{}
		}
		if cfg.isUnifiedVenue(venue) {
			deductUnifiedPool(available[venue], venueTotalRequired(walletReq))
			continue
		}
		for wallet, need := range walletReq {
			available[venue][wallet] -= need
		}
	}
}

func deductUnifiedPool(wallets map[WalletKind]float64, amount float64) {
	if amount <= capitalRouterEpsilon {
		return
	}
	remaining := amount
	for _, wallet := range capitalRouterWalletKinds {
		if remaining <= capitalRouterEpsilon {
			return
		}
		take := capitalRouterMin(remaining, capitalRouterMax(0, wallets[wallet]))
		wallets[wallet] -= take
		remaining -= take
	}
}

func sortWalletTransfers(transfers []WalletTransfer) {
	sort.Slice(transfers, func(i, j int) bool {
		a, b := transfers[i], transfers[j]
		if a.Venue != b.Venue {
			return a.Venue < b.Venue
		}
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.AmountUSDT < b.AmountUSDT
	})
}

func sortCrossVenueSuggestions(suggestions []CrossVenueRebalanceSuggestion) {
	sort.Slice(suggestions, func(i, j int) bool {
		a, b := suggestions[i], suggestions[j]
		if a.FromVenue != b.FromVenue {
			return a.FromVenue < b.FromVenue
		}
		if a.ToVenue != b.ToVenue {
			return a.ToVenue < b.ToVenue
		}
		return a.AmountUSDT < b.AmountUSDT
	})
}

func capitalRouterMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func capitalRouterMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
