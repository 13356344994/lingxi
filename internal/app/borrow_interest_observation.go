package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/venue/binance"
)

// borrow_interest_observation.go 是一条【只写、best-effort】的可观测性旁路：负 carry 平仓结算后，拉取
// 交易所【真实扣息流水】(/sapi/v1/margin/interestHistory) 并与结算里记的【模型借币成本】(公布利率×持仓小时)
// 对照，打一条结构化、可 grep 的 BORROW_INTEREST_ACTUAL 行，便于实盘后用真实数据校准 AssumedHourlyBorrowInterestRate。
//
// 设计约束（与用户「成本靠真实日志看、暂不据成本挡单」的姿态一致）：
//   - 绝不改动权威 NetPnL / TotalBorrowCosts（仍走模型口径）——避免跨并发借币的误归因，且不改任何熔断/风控行为；
//   - 以 goroutine 旁路执行，对平仓/结算关键路径【零延迟】；
//   - 查询失败/超时仅记日志，绝不影响交易。
//
// 已知口径限制（在日志里显式标注，避免误读）：
//   - interestHistory 是【资产级】窗口扣息，可能含同一资产的其它借贷（当前小档单仓场景下基本等于本 cycle）；
//   - 平仓瞬间最后一个未满计费小时的利息尚未入账，故 actual 可能略小于真实总额（多小时持仓相对影响小）。

const borrowInterestActualLogToken = "BORROW_INTEREST_ACTUAL"

type marginInterestHistoryRow struct {
	Asset    string `json:"asset"`
	Interest string `json:"interest"`
}

type marginInterestHistoryResponse struct {
	Rows []marginInterestHistoryRow `json:"rows"`
}

// sumMarginInterestByAsset 解析 interestHistory 回执，按资产汇总真实扣息（以该资产计价）。纯函数，便于单测。
func sumMarginInterestByAsset(body []byte) (map[string]float64, error) {
	var payload marginInterestHistoryResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析 interestHistory 失败: %w", err)
	}
	out := make(map[string]float64, len(payload.Rows))
	for _, row := range payload.Rows {
		asset := strings.ToUpper(strings.TrimSpace(row.Asset))
		if asset == "" {
			continue
		}
		out[asset] += parseFloat(row.Interest)
	}
	return out, nil
}

// fetchActualMarginInterestByAsset 拉取某资产在 [startMs, endMs] 的真实扣息（以该资产计价）。best-effort。
func (e *FundingRateSniperEngine) fetchActualMarginInterestByAsset(ctx context.Context, asset string, startMs, endMs int64) (float64, error) {
	params := url.Values{}
	if strings.TrimSpace(asset) != "" {
		params.Set("asset", strings.ToUpper(strings.TrimSpace(asset)))
	}
	if startMs > 0 {
		params.Set("startTime", strconv.FormatInt(startMs, 10))
	}
	if endMs > 0 {
		params.Set("endTime", strconv.FormatInt(endMs, 10))
	}
	params.Set("size", "100")
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.SpotBaseURL()+binance.PathMarginInterestHistory, params)
	if err != nil {
		return 0, err
	}
	byAsset, err := sumMarginInterestByAsset(body)
	if err != nil {
		return 0, err
	}
	return byAsset[strings.ToUpper(strings.TrimSpace(asset))], nil
}

// observeActualBorrowInterest 旁路对照「真实扣息 vs 模型借币成本」并打 BORROW_INTEREST_ACTUAL 日志。
// 仅对负 carry（借币做空现货）的本周期对冲生效；正 carry 无借币、直接返回。绝不影响净利/风控。
func (e *FundingRateSniperEngine) observeActualBorrowInterest(parent context.Context, hedges []OpenHedge, settlement CycleSettlement) {
	if e == nil || e.logger == nil {
		return
	}
	type assetObservation struct {
		earliestEntryMs int64
		referencePrice  float64
	}
	obs := make(map[string]assetObservation)
	for _, hedge := range hedges {
		if hedge.carryDirection() != fundingCarryDirectionNegative || hedge.OpenSpotQuoteQty <= 0 {
			continue
		}
		asset := strings.ToUpper(strings.TrimSpace(hedge.Target.SpotBaseAsset))
		if asset == "" {
			continue
		}
		entryMs := int64(0)
		if hedge.SpotOrder.TransactTime > 0 {
			entryMs = hedge.SpotOrder.TransactTime
		}
		price := hedge.LastKnownSpotPrice()
		if price <= 0 && hedge.HedgeQty > 0 {
			price = hedge.OpenSpotQuoteQty / hedge.HedgeQty
		}
		cur, ok := obs[asset]
		if !ok {
			cur = assetObservation{earliestEntryMs: entryMs, referencePrice: price}
		} else {
			if entryMs > 0 && (cur.earliestEntryMs == 0 || entryMs < cur.earliestEntryMs) {
				cur.earliestEntryMs = entryMs
			}
			if cur.referencePrice <= 0 {
				cur.referencePrice = price
			}
		}
		obs[asset] = cur
	}
	if len(obs) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	nowMs := time.Now().UTC().UnixMilli()
	var totalActualUSDT float64
	details := make([]string, 0, len(obs))
	for asset, o := range obs {
		interestInAsset, err := e.fetchActualMarginInterestByAsset(ctx, asset, o.earliestEntryMs, nowMs)
		if err != nil {
			e.logger.Printf("%s 查询失败(仅观测,不影响净利) asset=%s err=%v", borrowInterestActualLogToken, asset, err)
			continue
		}
		usdt := interestInAsset * o.referencePrice
		totalActualUSDT += usdt
		details = append(details, fmt.Sprintf("%s=%.8f(%.6fUSDT)", asset, interestInAsset, usdt))
	}
	model := settlement.TotalBorrowCosts
	e.logger.Printf(
		"%s run=%s cycle=%s actual_usdt=%.6f model_usdt=%.6f diff_usdt=%.6f assets=[%s] 注:资产级窗口扣息(小档单仓≈本cycle),不含最后未结算的部分小时;仅观测不改净利",
		borrowInterestActualLogToken,
		nonEmpty(strings.TrimSpace(settlement.RunID), "-"),
		nonEmpty(strings.TrimSpace(settlement.CycleID), "-"),
		totalActualUSDT, model, totalActualUSDT-model, strings.Join(details, ","),
	)
}
