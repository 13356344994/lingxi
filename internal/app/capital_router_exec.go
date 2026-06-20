package app

import (
	"context"
	"fmt"
	"log"
	"strings"

	"lingxi/internal/venue"
)

type walletTransferer interface {
	TransferWallet(ctx context.Context, from, to string, amountUSDT float64) error
}

// CapitalRoutingExecResult 是 ExecuteCapitalRouting 的执行结果。
type CapitalRoutingExecResult struct {
	Executed                     int
	Errs                         []error
	FailedTransferOpportunityIDs []string // 所内划转失败、须阻断 OPEN 的机会 ID
}

// ExecuteCapitalRouting 执行资金路由计划中的所内划转;跨所建议仅记录日志,绝不执行。
// live==false 时只打 dry-run 日志,不调用任何划转接口。
func ExecuteCapitalRouting(ctx context.Context, plan CapitalRoutingPlan, venues map[string]venue.Venue, live bool, logger *log.Logger) CapitalRoutingExecResult {
	if logger == nil {
		logger = log.Default()
	}

	result := CapitalRoutingExecResult{}
	failedOpp := map[string]struct{}{}

	recordFailedTransfer := func(tr WalletTransfer) {
		for _, id := range tr.OpportunityIDs {
			if id == "" {
				continue
			}
			if _, ok := failedOpp[id]; ok {
				continue
			}
			failedOpp[id] = struct{}{}
			result.FailedTransferOpportunityIDs = append(result.FailedTransferOpportunityIDs, id)
		}
	}

	for _, tr := range plan.WithinVenueTransfers {
		from := string(tr.From)
		to := string(tr.To)
		if !live {
			logger.Printf("[dry-run] 本应划转 venue=%s %s→%s 金额=%.4f 原因=%s", tr.Venue, from, to, tr.AmountUSDT, tr.Reason)
			continue
		}
		v, ok := venues[tr.Venue]
		if !ok {
			err := fmt.Errorf("capital routing: venue %q not found", tr.Venue)
			result.Errs = append(result.Errs, err)
			recordFailedTransfer(tr)
			continue
		}
		wt, ok := v.(walletTransferer)
		if !ok {
			err := fmt.Errorf("capital routing: venue %q does not support wallet transfer", tr.Venue)
			result.Errs = append(result.Errs, err)
			recordFailedTransfer(tr)
			continue
		}
		// 目标钱包(perp/futures 等)已够则跳过所内划转,避免因 spot 空导致的不必要失败阻断开仓。
		if have, ok := capitalRouterTargetWalletAvailableUSDT(ctx, v, tr.To); ok && have+capitalRouterEpsilon >= tr.AmountUSDT {
			logger.Printf("[资金路由] 跳过划转 venue=%s %s→%s 金额=%.4f: 目标 %s 已有 %.4f ≥ 所需 %.4f",
				tr.Venue, from, to, tr.AmountUSDT, to, have, tr.AmountUSDT)
			continue
		}
		if err := wt.TransferWallet(ctx, from, to, tr.AmountUSDT); err != nil {
			if have, ok := capitalRouterTargetWalletAvailableUSDT(ctx, v, tr.To); ok && have+capitalRouterEpsilon >= tr.AmountUSDT {
				logger.Printf("[资金路由] 划转失败但目标 %s 已够(%.4f≥%.4f),不阻断开仓: venue=%s %s→%s err=%v",
					to, have, tr.AmountUSDT, tr.Venue, from, to, err)
				continue
			}
			result.Errs = append(result.Errs, fmt.Errorf("capital routing: venue %q %s→%s %.4f: %w", tr.Venue, from, to, tr.AmountUSDT, err))
			recordFailedTransfer(tr)
			continue
		}
		result.Executed++
	}

	for _, sug := range plan.CrossVenueSuggestions {
		logger.Printf(
			"[跨所建议][%s] %s→%s 金额=%.4f 原因=%s（不自动执行，需人工或幂等划转模块）",
			CapitalUnfundableReasonCrossVenueGap,
			sug.FromVenue,
			sug.ToVenue,
			sug.AmountUSDT,
			sug.Reason,
		)
	}

	for _, entry := range plan.Unfundable {
		if strings.Contains(entry, CapitalUnfundableReasonCrossVenueGap) {
			logger.Printf("[跨所缺口][unfundable][%s] %s", CapitalUnfundableReasonCrossVenueGap, entry)
		}
	}

	return result
}

// capitalRouterTargetWalletAvailableUSDT 读取所内目标钱包真实可用 USDT(供路由执行前/失败后复核)。
func capitalRouterTargetWalletAvailableUSDT(ctx context.Context, v venue.Venue, wallet WalletKind) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch wallet {
	case WalletKindFutures:
		avail, err := fetchVenuePerpAvailableUSDT(ctx, v)
		return avail, err == nil
	case WalletKindSpot:
		if p, ok := v.(venueSpotAvailableUSDTProvider); ok {
			avail, err := fetchVenueWalletAvailableUSDT(ctx, "spot", p.FetchSpotAvailableUSDT)
			return avail, err == nil
		}
	case WalletKindMargin:
		if p, ok := v.(venueCrossMarginAvailableUSDTProvider); ok {
			avail, err := fetchVenueWalletAvailableUSDT(ctx, "margin", p.FetchCrossMarginAvailableUSDT)
			return avail, err == nil
		}
	}
	return 0, false
}
