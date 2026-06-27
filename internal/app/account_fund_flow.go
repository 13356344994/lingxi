package app

import (
	"context"
	"errors"
	"log"
	"math"
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
)

// accountFundFlowReconcileFloorUSDT 当 real 充提源疑似漏检时,触发「无持仓重对账」的最小残差门槛(USDT)。
// 取得足够大以避开正常的已实现/权益对账噪声,只在出现大额(如提现)未解释变动时介入。
const accountFundFlowReconcileFloorUSDT = 10.0

type accountFundFlowLookup func(venueName string) venue.ExternalFundFlowProvider

func defaultAccountFundFlowLookup(venues map[string]venue.Venue) accountFundFlowLookup {
	return func(name string) venue.ExternalFundFlowProvider {
		if venues == nil {
			return nil
		}
		v, ok := venues[name]
		if !ok || v == nil {
			return nil
		}
		p, ok := v.(venue.ExternalFundFlowProvider)
		if !ok {
			return nil
		}
		return p
	}
}

func fetchAccountRealFundFlowUSDT(
	ctx context.Context,
	obs accountEquityObservation,
	lookup accountFundFlowLookup,
	since time.Time,
) (total float64, byVenue map[string]float64, ok bool) {
	if lookup == nil || len(obs.ByVenue) == 0 {
		return 0, nil, false
	}
	byVenue = make(map[string]float64, len(obs.ByVenue))
	for venueName := range obs.ByVenue {
		p := lookup(venueName)
		if p == nil {
			return 0, nil, false
		}
		net, err := p.FetchNetExternalFundFlowUSDT(ctx, since)
		if err != nil {
			if errors.Is(err, venue.ErrUnsupported) {
				return 0, nil, false
			}
			return 0, nil, false
		}
		byVenue[venueName] = net
		total = money.SumUSDT(total, net)
	}
	return total, byVenue, true
}

func applyAccountFundFlowAdjustment(
	ctx context.Context,
	st *accountRiskEquityState,
	obs accountEquityObservation,
	lookup accountFundFlowLookup,
	dayStart time.Time,
	currentTotal float64,
	rawDailyDelta float64,
	dailyRealized float64,
	pos accountRiskPositionSummary,
	settlementCount int,
	minStep float64,
	logger *log.Logger,
) (source string, step float64) {
	if st == nil {
		return "heuristic", 0
	}
	if realTotal, realByVenue, ok := fetchAccountRealFundFlowUSDT(ctx, obs, lookup, dayStart); ok {
		st.DailyFundFlowByVenueUSDT = copyVenueEquityMap(realByVenue)
		// 交叉校验:real 充提源(交易所充提历史)对尚处 pending / 未到 DONE/success 的提现会暂时漏检,
		// 返回 0 却把权益骤降误当成交易亏损/回撤(可触发日损/回撤急停)。
		// 安全信号:positions==0 时权益变动在数学上不可能来自交易(无未实现、已实现单列),
		// 只能来自充提或对账噪声。若 real 之外仍有大额无法解释的残差,则把残差并入充提平移,
		// 使风控口径权益≈已实现交易盈亏,杜绝把提现算成亏损。提现一旦在交易所历史落定(DONE),
		// real 源会自然接管,残差收敛回阈值内,回到纯 real 路径。
		cumulativeUnexplained := money.SumUSDT(rawDailyDelta, -dailyRealized)
		residual := money.SumUSDT(cumulativeUnexplained, -realTotal)
		reconcileFloor := math.Max(minStep, accountFundFlowReconcileFloorUSDT)
		if pos.TotalCount == 0 && math.Abs(residual) >= reconcileFloor {
			prev := st.DailyFundFlowAdjustUSDT
			st.DailyFundFlowAdjustUSDT = cumulativeUnexplained
			step = money.SumUSDT(cumulativeUnexplained, -prev)
			// one-shot:仅在首次进入或平移额真正变化时打印,避免空仓期每 60s tick 重复刷屏(曾累计上百条/日)。
			if logger != nil && (!st.fundFlowSourceLogged || math.Abs(step) > 1e-9) {
				logger.Printf("[账户风控] real充提源疑似漏检(real_cum=%.4f 残差=%.4f positions=0):并入充提平移 cumulative=%.4f(风控口径权益≈已实现 %.4f)",
					realTotal, residual, cumulativeUnexplained, dailyRealized)
			}
			st.fundFlowSourceLogged = true
			st.DailyFundFlowSource = "real+reconcile"
			// 未确权部分 = real 之外被「无持仓推断」并入的残差;面板据此高亮提示用户确认非隐藏亏损。
			st.DailyFundFlowReconcileUSDT = residual
			return "real+reconcile", step
		}
		prev := st.DailyFundFlowAdjustUSDT
		st.DailyFundFlowAdjustUSDT = realTotal
		step = money.SumUSDT(realTotal, -prev)
		st.DailyFundFlowSource = "real"
		st.DailyFundFlowReconcileUSDT = 0
		if logger != nil {
			if !st.fundFlowSourceLogged {
				logger.Printf("[账户风控] 充提流水源=real cumulative=%.4f (首次确认)", realTotal)
				st.fundFlowSourceLogged = true
			} else if step != 0 {
				logger.Printf("[账户风控] 资金流入/流出(source=real) step=%.4f cumulative=%.4f venues=%d",
					step, realTotal, len(obs.ByVenue))
			}
		}
		return "real", step
	}
	step = detectAccountFundFlowStep(
		st,
		currentTotal,
		rawDailyDelta,
		dailyRealized,
		pos,
		settlementCount,
		minStep,
	)
	st.DailyFundFlowByVenueUSDT = nil
	st.DailyFundFlowSource = "heuristic"
	// 纯启发式路径:整笔平移均未经交易所确权,全额标为未证实,面板高亮提示用户核实。
	st.DailyFundFlowReconcileUSDT = st.DailyFundFlowAdjustUSDT
	if logger != nil {
		if !st.fundFlowSourceLogged {
			logger.Printf("[账户风控] 充提流水源=heuristic cumulative=%.4f (首次确认)", st.DailyFundFlowAdjustUSDT)
			st.fundFlowSourceLogged = true
		} else if step != 0 {
			logger.Printf("[账户风控] 资金流入/流出(source=heuristic) step=%.4f cumulative=%.4f", step, st.DailyFundFlowAdjustUSDT)
		}
	}
	return "heuristic", step
}

func crossVenueFundFlowLookup(cv *CrossVenueExecutor) accountFundFlowLookup {
	if cv == nil {
		return nil
	}
	return defaultAccountFundFlowLookup(cv.venues)
}
