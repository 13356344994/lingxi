package app

import (
	"fmt"
	"sort"
	"strings"
)

const capitalRouterEpsilon = 1e-9

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
	Venue      string
	From, To   WalletKind
	AmountUSDT float64
	Reason     string
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

	var transfers []WalletTransfer
	for venue, req := range aggregated {
		transfers = append(transfers, planWithinVenueTransfers(venue, req, working[venue])...)
	}
	sortWalletTransfers(transfers)
	applyWithinVenueTransfers(working, transfers)

	crossSuggestions := planCrossVenueSuggestions(aggregated, working)
	unfundable := markUnfundableOpportunities(chosen, perOpp, working, crossSuggestions)

	return CapitalRoutingPlan{
		WithinVenueTransfers:  transfers,
		CrossVenueSuggestions: crossSuggestions,
		Unfundable:            unfundable,
	}
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

func markUnfundableOpportunities(
	chosen []UnifiedOpportunity,
	perOpp map[string]map[string]venueWalletRequirements,
	working VenueWalletBalances,
	crossSuggestions []CrossVenueRebalanceSuggestion,
) []string {
	crossInflowVenues := map[string]struct{}{}
	for _, s := range crossSuggestions {
		if s.ToVenue != "" && s.AmountUSDT > capitalRouterEpsilon {
			crossInflowVenues[s.ToVenue] = struct{}{}
		}
	}

	available := cloneVenueWalletBalances(working)
	var unfundable []string

	for _, opp := range chosen {
		reqByVenue := perOpp[opp.ID]
		reason := opportunityFundingGapReason(reqByVenue, available, crossInflowVenues)
		if reason == "" {
			consumeOpportunityFunding(reqByVenue, available)
			continue
		}
		unfundable = append(unfundable, opp.ID+": "+reason)
	}
	return unfundable
}

func opportunityFundingGapReason(
	reqByVenue map[string]venueWalletRequirements,
	available VenueWalletBalances,
	crossInflowVenues map[string]struct{},
) string {
	for venue, walletReq := range reqByVenue {
		if _, needsCross := crossInflowVenues[venue]; needsCross {
			return fmt.Sprintf("requires cross-venue rebalance to %s (not auto-funded)", venue)
		}
		wallets := available[venue]
		if wallets == nil {
			wallets = map[WalletKind]float64{}
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

func consumeOpportunityFunding(reqByVenue map[string]venueWalletRequirements, available VenueWalletBalances) {
	for venue, walletReq := range reqByVenue {
		if available[venue] == nil {
			available[venue] = map[WalletKind]float64{}
		}
		for wallet, need := range walletReq {
			available[venue][wallet] -= need
		}
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
