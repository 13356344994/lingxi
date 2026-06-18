package app

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// UnifiedCapitalSnapshot 跨所资金碎片化只读快照(JSON 友好)。
// 纯聚合展示:不触发划转、下单或平仓。
type UnifiedCapitalSnapshot struct {
	GeneratedAt           string                          `json:"generated_at"`
	Venues                []VenueCapitalBreakdown         `json:"venues"`
	PendingOpportunities  []PendingOpportunityCapital     `json:"pending_opportunities,omitempty"`
	Unfundable            []UnfundableOpportunityDetail   `json:"unfundable,omitempty"`
	CrossVenueSuggestions []CrossVenueRebalanceSuggestion `json:"cross_venue_suggestions,omitempty"`
}

// VenueCapitalBreakdown 单所资金分布。
type VenueCapitalBreakdown struct {
	Venue         string               `json:"venue"`
	UnifiedWallet bool                 `json:"unified_wallet"`
	TotalUSDT     float64              `json:"total_usdt"`
	Wallets       []VenueWalletCapital `json:"wallets"`
}

// VenueWalletCapital 单钱包 free/reserved/策略占用。
type VenueWalletCapital struct {
	Wallet             WalletKind         `json:"wallet"`
	FreeUSDT           float64            `json:"free_usdt"`
	ReservedUSDT       float64            `json:"reserved_usdt"`
	OccupiedByStrategy map[string]float64 `json:"occupied_by_strategy"`
}

// PendingOpportunityCapital 待开机会所需资金(来自最近一轮轮动计划;无则省略)。
type PendingOpportunityCapital struct {
	OpportunityID     string                            `json:"opportunity_id"`
	StrategyType      string                            `json:"strategy_type"`
	Symbol            string                            `json:"symbol"`
	RequiredByVenue   map[string][]WalletRequirementUSD `json:"required_by_venue"`
	TotalRequiredUSDT float64                           `json:"total_required_usdt"`
}

// WalletRequirementUSD venue 下单钱包 USDT 需求。
type WalletRequirementUSD struct {
	Wallet WalletKind `json:"wallet"`
	USDT   float64    `json:"usdt"`
}

// UnfundableOpportunityDetail 无法备资机会明细(复用 capital_router 路由结论)。
type UnfundableOpportunityDetail struct {
	OpportunityID        string     `json:"opportunity_id"`
	StrategyType         string     `json:"strategy_type"`
	Reason               string     `json:"reason"`
	Venue                string     `json:"venue,omitempty"`
	Wallet               WalletKind `json:"wallet,omitempty"`
	NeedUSDT             float64    `json:"need_usdt,omitempty"`
	HaveUSDT             float64    `json:"have_usdt,omitempty"`
	ShortfallUSDT        float64    `json:"shortfall_usdt,omitempty"`
	CrossVenueCanResolve bool       `json:"cross_venue_can_resolve"`
}

// CapitalSnapshotInput BuildUnifiedCapitalSnapshot 只读输入。
type CapitalSnapshotInput struct {
	FetchedByVenue       map[string]float64
	Held                 []HeldPosition
	PendingOpportunities []UnifiedOpportunity
	RoutingPlan          CapitalRoutingPlan
	RouterCfg            CapitalRouterConfig
	ExecCfg              CrossVenueExecutorConfig
	Venues               map[string]venue.Venue
	GeneratedAt          time.Time
}

// BuildUnifiedCapitalSnapshot 纯函数:聚合 venue/wallet 余额、储备金、策略占用与无法备资列表。
func BuildUnifiedCapitalSnapshot(in CapitalSnapshotInput) UnifiedCapitalSnapshot {
	in.RouterCfg = normalizeCapitalRouterConfig(in.RouterCfg)
	unified := UnifiedWalletVenuesFromVenues(in.Venues)
	if in.RouterCfg.UnifiedWalletVenues == nil {
		in.RouterCfg.UnifiedWalletVenues = unified
	}

	gross := rotationWalletBalancesFromAvailable(in.FetchedByVenue, in.Held, in.ExecCfg, in.Venues)
	reserved := allocateVenueReservedUSDT(gross, in.RouterCfg)
	occupied := aggregateOccupiedByStrategy(in.Held, in.ExecCfg)

	venues := buildVenueCapitalBreakdowns(gross, reserved, occupied, unified)

	var pending []PendingOpportunityCapital
	if len(in.PendingOpportunities) > 0 {
		pending = buildPendingOpportunityCapital(in.PendingOpportunities, in.RouterCfg)
	}

	var unfundable []UnfundableOpportunityDetail
	var crossSuggestions []CrossVenueRebalanceSuggestion
	if len(in.RoutingPlan.Unfundable) > 0 || len(in.RoutingPlan.CrossVenueSuggestions) > 0 {
		unfundable = buildUnfundableDetails(in.RoutingPlan.Unfundable, in.PendingOpportunities, in.RoutingPlan)
		crossSuggestions = append([]CrossVenueRebalanceSuggestion(nil), in.RoutingPlan.CrossVenueSuggestions...)
	}

	genAt := in.GeneratedAt
	if genAt.IsZero() {
		genAt = time.Now().UTC()
	}

	return UnifiedCapitalSnapshot{
		GeneratedAt:           genAt.UTC().Format(time.RFC3339),
		Venues:                venues,
		PendingOpportunities:  pending,
		Unfundable:            unfundable,
		CrossVenueSuggestions: crossSuggestions,
	}
}

func buildVenueCapitalBreakdowns(
	gross VenueWalletBalances,
	reserved VenueWalletBalances,
	occupied map[string]map[WalletKind]map[string]float64,
	unified map[string]bool,
) []VenueCapitalBreakdown {
	venueNames := sortedCapitalSnapshotVenues(gross, reserved, occupied)
	out := make([]VenueCapitalBreakdown, 0, len(venueNames))
	for _, venueName := range venueNames {
		wallets := gross[venueName]
		if wallets == nil {
			wallets = map[WalletKind]float64{}
		}
		resWallets := reserved[venueName]
		if resWallets == nil {
			resWallets = map[WalletKind]float64{}
		}
		occWallets := occupied[venueName]
		if occWallets == nil {
			occWallets = map[WalletKind]map[string]float64{}
		}

		walletRows := make([]VenueWalletCapital, 0, len(capitalRouterWalletKinds))
		total := 0.0
		for _, wallet := range capitalRouterWalletKinds {
			grossAmt := capitalRouterMax(0, wallets[wallet])
			if grossAmt <= capitalRouterEpsilon {
				occSum := sumOccupiedForWallet(occWallets[wallet])
				resAmt := capitalRouterMax(0, resWallets[wallet])
				if occSum <= capitalRouterEpsilon && resAmt <= capitalRouterEpsilon {
					continue
				}
			}
			occSum := sumOccupiedForWallet(occWallets[wallet])
			resAmt := capitalRouterMax(0, resWallets[wallet])
			free := grossAmt - occSum - resAmt
			if free < 0 {
				free = 0
			}
			total += grossAmt
			walletRows = append(walletRows, VenueWalletCapital{
				Wallet:             wallet,
				FreeUSDT:           free,
				ReservedUSDT:       resAmt,
				OccupiedByStrategy: cloneStrategyOccupied(occWallets[wallet]),
			})
		}
		out = append(out, VenueCapitalBreakdown{
			Venue:         venueName,
			UnifiedWallet: unified[venueName],
			TotalUSDT:     total,
			Wallets:       walletRows,
		})
	}
	return out
}

func sortedCapitalSnapshotVenues(gross VenueWalletBalances, reserved VenueWalletBalances, occupied map[string]map[WalletKind]map[string]float64) []string {
	seen := map[string]struct{}{}
	for v := range gross {
		seen[v] = struct{}{}
	}
	for v := range reserved {
		seen[v] = struct{}{}
	}
	for v := range occupied {
		seen[v] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for v := range seen {
		names = append(names, v)
	}
	sort.Strings(names)
	return names
}

func sumOccupiedForWallet(byStrategy map[string]float64) float64 {
	if len(byStrategy) == 0 {
		return 0
	}
	total := 0.0
	for _, amt := range byStrategy {
		total += amt
	}
	return total
}

func cloneStrategyOccupied(src map[string]float64) map[string]float64 {
	if len(src) == 0 {
		return map[string]float64{}
	}
	dst := make(map[string]float64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// allocateVenueReservedUSDT 按 lockVenueReserves 同样顺序锁定储备金(只读镜像)。
func allocateVenueReservedUSDT(gross VenueWalletBalances, cfg CapitalRouterConfig) VenueWalletBalances {
	reserved := VenueWalletBalances{}
	for venueName, wallets := range gross {
		reserve := cfg.venueReserveUSDT(venueName)
		if reserve <= capitalRouterEpsilon {
			continue
		}
		if wallets == nil {
			continue
		}
		reserved[venueName] = map[WalletKind]float64{}
		remaining := reserve
		for _, wallet := range capitalRouterWalletKinds {
			if remaining <= capitalRouterEpsilon {
				break
			}
			available := capitalRouterMax(0, wallets[wallet])
			lock := capitalRouterMin(available, remaining)
			if lock > capitalRouterEpsilon {
				reserved[venueName][wallet] = lock
			}
			remaining -= lock
		}
	}
	return reserved
}

func aggregateOccupiedByStrategy(held []HeldPosition, execCfg CrossVenueExecutorConfig) map[string]map[WalletKind]map[string]float64 {
	out := map[string]map[WalletKind]map[string]float64{}
	for _, pos := range held {
		strategy := strings.TrimSpace(pos.StrategyType)
		if strategy == "" {
			strategy = "unknown"
		}
		for venueName, walletReq := range rotationHeldWalletRequirements(pos, execCfg) {
			if out[venueName] == nil {
				out[venueName] = map[WalletKind]map[string]float64{}
			}
			for wallet, amt := range walletReq {
				if amt <= capitalRouterEpsilon {
					continue
				}
				if out[venueName][wallet] == nil {
					out[venueName][wallet] = map[string]float64{}
				}
				out[venueName][wallet][strategy] += amt
			}
		}
	}
	return out
}

func buildPendingOpportunityCapital(pending []UnifiedOpportunity, cfg CapitalRouterConfig) []PendingOpportunityCapital {
	out := make([]PendingOpportunityCapital, 0, len(pending))
	for _, opp := range pending {
		reqByVenue := aggregateOpportunityRequirements(opp, cfg)
		required := map[string][]WalletRequirementUSD{}
		total := 0.0
		for venueName, walletReq := range reqByVenue {
			rows := make([]WalletRequirementUSD, 0, len(walletReq))
			for wallet, amt := range walletReq {
				if amt <= capitalRouterEpsilon {
					continue
				}
				rows = append(rows, WalletRequirementUSD{Wallet: wallet, USDT: amt})
				total += amt
			}
			if len(rows) > 0 {
				sort.Slice(rows, func(i, j int) bool { return rows[i].Wallet < rows[j].Wallet })
				required[venueName] = rows
			}
		}
		out = append(out, PendingOpportunityCapital{
			OpportunityID:     opp.ID,
			StrategyType:      opp.StrategyType,
			Symbol:            opp.Symbol,
			RequiredByVenue:   required,
			TotalRequiredUSDT: total,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OpportunityID < out[j].OpportunityID })
	return out
}

func buildUnfundableDetails(unfundable []string, pending []UnifiedOpportunity, plan CapitalRoutingPlan) []UnfundableOpportunityDetail {
	pendingByID := indexUnifiedOpportunitiesByID(pending)
	out := make([]UnfundableOpportunityDetail, 0, len(unfundable))
	for _, entry := range unfundable {
		out = append(out, parseUnfundableEntry(entry, pendingByID, plan))
	}
	return out
}

func parseUnfundableEntry(entry string, pendingByID map[string]UnifiedOpportunity, plan CapitalRoutingPlan) UnfundableOpportunityDetail {
	id, reason := entry, ""
	if i := strings.Index(entry, ":"); i > 0 {
		id = strings.TrimSpace(entry[:i])
		reason = strings.TrimSpace(entry[i+1:])
	}
	detail := UnfundableOpportunityDetail{
		OpportunityID: id,
		Reason:        reason,
	}
	if opp, ok := pendingByID[id]; ok {
		detail.StrategyType = opp.StrategyType
	}
	parseUnfundableFundingGap(reason, &detail)
	detail.CrossVenueCanResolve = unfundableCrossVenueCanResolve(detail, plan)
	return detail
}

func parseUnfundableFundingGap(reason string, detail *UnfundableOpportunityDetail) {
	if detail == nil || reason == "" {
		return
	}
	if strings.Contains(reason, "requires cross-venue rebalance to ") {
		const prefix = "requires cross-venue rebalance to "
		if idx := strings.Index(reason, prefix); idx >= 0 {
			rest := strings.TrimSpace(reason[idx+len(prefix):])
			if j := strings.Index(rest, " "); j > 0 {
				detail.Venue = rest[:j]
			} else {
				detail.Venue = strings.TrimSuffix(rest, "(not auto-funded)")
			}
			detail.Venue = strings.TrimSpace(strings.TrimSuffix(detail.Venue, "("))
		}
		return
	}
	// insufficient venue/wallet USDT (need X, have Y) 或 unified 变体
	const insufficientPrefix = "insufficient "
	if !strings.HasPrefix(reason, insufficientPrefix) {
		return
	}
	body := strings.TrimPrefix(reason, insufficientPrefix)
	parenIdx := strings.Index(body, " (need ")
	if parenIdx <= 0 {
		return
	}
	loc := strings.TrimSpace(body[:parenIdx])
	if strings.Contains(loc, " unified USDT") {
		detail.Venue = strings.TrimSpace(strings.TrimSuffix(loc, " unified USDT"))
	} else if parts := strings.Split(loc, "/"); len(parts) == 2 {
		detail.Venue = strings.TrimSpace(parts[0])
		detail.Wallet = WalletKind(strings.TrimSpace(parts[1]))
	}
	need, have, ok := parseNeedHaveFromReason(reason)
	if ok {
		detail.NeedUSDT = need
		detail.HaveUSDT = have
		if need > have {
			detail.ShortfallUSDT = need - have
		}
	}
}

func parseNeedHaveFromReason(reason string) (need, have float64, ok bool) {
	const marker = "(need "
	idx := strings.Index(reason, marker)
	if idx < 0 {
		return 0, 0, false
	}
	rest := reason[idx+len(marker):]
	comma := strings.Index(rest, ", have ")
	if comma < 0 {
		return 0, 0, false
	}
	needStr := strings.TrimSpace(rest[:comma])
	haveStr := strings.TrimSpace(rest[comma+len(", have "):])
	haveStr = strings.TrimSuffix(haveStr, ")")
	var err error
	need, err = strconv.ParseFloat(needStr, 64)
	if err != nil {
		return 0, 0, false
	}
	have, err = strconv.ParseFloat(haveStr, 64)
	if err != nil {
		return 0, 0, false
	}
	return need, have, true
}

func unfundableCrossVenueCanResolve(detail UnfundableOpportunityDetail, plan CapitalRoutingPlan) bool {
	if detail.Venue == "" {
		return false
	}
	if strings.Contains(detail.Reason, "requires cross-venue rebalance") {
		for _, s := range plan.CrossVenueSuggestions {
			if s.ToVenue == detail.Venue && s.AmountUSDT > capitalRouterEpsilon {
				return true
			}
		}
		return false
	}
	if detail.ShortfallUSDT <= capitalRouterEpsilon {
		return false
	}
	inflow := 0.0
	for _, s := range plan.CrossVenueSuggestions {
		if s.ToVenue == detail.Venue {
			inflow += s.AmountUSDT
		}
	}
	return inflow+capitalRouterEpsilon >= detail.ShortfallUSDT
}
