package app

import (
	"context"
	"fmt"
	"strings"
)

// borrow_capacity.go 实现 R29「借币容量/利率阶梯 pre-open 守卫」。
//
// 背景:负向 carry(funding 为负)开仓需在全仓杠杆账户**借入 base 资产做空现货**(spotVenue=cross_margin,
// sideEffectType=AUTO_BORROW_REPAY)。若可借量耗尽,交易所会拒单 → 合约腿可能已成交而现货腿失败 → 裸腿;
// 若当前借币利率跳到高阶梯,负向 carry 的真实成本会远超模型 → 看似盈利实则亏损。
//
// 本守卫在**开仓前**(候选筛选阶段)对负向 carry 候选逐个校验:①最大可借量 ≥ 需借量 ×(1+余量);
// ②每小时借币利率 ≤ 上限。不满足者直接从本轮候选剔除(剔除=根本不开,天然无裸腿),并发告警。
// 正向 carry 不借币,恒放行。整个守卫 behind RiskConfig.BorrowCapacityCheckEnabled(默认关闭)。
//
// 决策是纯函数(decideBorrowAdmission),与拉取可借量/利率的 I/O 解耦,便于单测。

// borrowAdmissionInput 是单个负向 carry 候选的借币准入判定输入(全部为已知量,纯数据)。
type borrowAdmissionInput struct {
	Asset              string  // 需借入的 base 资产(如 BTC)
	RequiredBaseQty    float64 // 本次开仓需借入的 base 数量(估算或精确)
	MaxBorrowableQty   float64 // 交易所返回的当前最大可借(base 单位);<=0 视为「未知」(查询失败/未启用)
	MaxBorrowableKnown bool    // 是否成功取到可借量(区分「0=借不到」与「未知」)
	HourlyInterestRate float64 // 当前每小时借币利率(per-asset 真实利率,优先于默认)
}

// borrowAdmissionConfig 是守卫阈值(来自 RiskConfig)。
type borrowAdmissionConfig struct {
	Enabled               bool
	MinHeadroomRate       float64 // 要求 MaxBorrowable ≥ Required×(1+MinHeadroomRate)
	MaxHourlyInterestRate float64 // 利率上限;<=0 表示不限
}

// borrowAdmissionResult 是判定结果。Admit=false 时 Reason 给出剔除原因(用于日志/告警)。
type borrowAdmissionResult struct {
	Admit  bool
	Reason string
}

// decideBorrowAdmission 是借币准入的纯决策:
//   - 守卫关闭 / 无需借币(asset 空或需借量≤0,即正向 carry)→ 放行;
//   - 已知可借量且 < 需借量×(1+余量) → 剔除(借不到/余量不足);
//   - 配了利率上限且当前利率 > 上限 → 剔除(利率阶梯过高);
//   - 其余 → 放行。
//
// 注:可借量「未知」(查询失败)时**不剔除**(MaxBorrowableKnown=false)——守卫是 advisory 增强,不应因
// 一次只读查询失败就误杀机会;真正的兜底仍是开仓时的裸腿守卫与恢复协议。
func decideBorrowAdmission(in borrowAdmissionInput, cfg borrowAdmissionConfig) borrowAdmissionResult {
	if !cfg.Enabled {
		return borrowAdmissionResult{Admit: true}
	}
	if strings.TrimSpace(in.Asset) == "" || in.RequiredBaseQty <= 0 {
		return borrowAdmissionResult{Admit: true, Reason: "无需借币(正向 carry 或需借量为 0)"}
	}
	if in.MaxBorrowableKnown {
		headroom := cfg.MinHeadroomRate
		if headroom < 0 {
			headroom = 0
		}
		need := in.RequiredBaseQty * (1 + headroom)
		if in.MaxBorrowableQty < need {
			return borrowAdmissionResult{
				Admit: false,
				Reason: fmt.Sprintf("可借量不足: %s 最大可借=%.8f 需借(含%.0f%%余量)=%.8f",
					in.Asset, in.MaxBorrowableQty, headroom*100, need),
			}
		}
	}
	if cfg.MaxHourlyInterestRate > 0 && in.HourlyInterestRate > cfg.MaxHourlyInterestRate {
		return borrowAdmissionResult{
			Admit: false,
			Reason: fmt.Sprintf("借币利率过高: %s 每小时利率=%.6f 上限=%.6f",
				in.Asset, in.HourlyInterestRate, cfg.MaxHourlyInterestRate),
		}
	}
	return borrowAdmissionResult{Admit: true}
}

// borrowAdmissionConfigFromRisk 从 RiskConfig 抽出守卫阈值。
func (e *FundingRateSniperEngine) borrowAdmissionConfigFromRisk() borrowAdmissionConfig {
	return borrowAdmissionConfig{
		Enabled:               e.cfg.RiskConfig.BorrowCapacityCheckEnabled,
		MinHeadroomRate:       e.cfg.RiskConfig.BorrowCapacityMinHeadroomRate,
		MaxHourlyInterestRate: e.cfg.RiskConfig.MaxBorrowHourlyInterestRate,
	}
}

// estimateRequiredBorrowBaseQty 估算一个候选开仓需借入的 base 数量。优先用已定的下单量(OrderQuantity),
// 否则用配置的单币名义 / 参考价做保守估算。仅对负向 carry 有意义(正向不借币 → 0)。
func (e *FundingRateSniperEngine) estimateRequiredBorrowBaseQty(target *MonitoredTarget) float64 {
	if target == nil {
		return 0
	}
	candidate := target.Candidate
	if resolvedFundingCarryDirection(candidate.Direction, candidate.EstimatedRate) != fundingCarryDirectionNegative {
		return 0
	}
	if candidate.OrderQuantity > 0 {
		return candidate.OrderQuantity
	}
	price := candidate.MarkPrice
	if price <= 0 {
		price = target.Candidate.MarkPrice
	}
	if price <= 0 {
		return 0
	}
	// 兜底估算(正常路径 OrderQuantity 已定,几乎不会走到这里):用配置单币名义 / 参考价。
	notional := e.cfg.StrategyConfig.PerSymbolQuoteUSDT
	if notional <= 0 {
		return 0
	}
	return notional / price
}

// filterTargetsByBorrowCapacity 在开仓前剔除「借不到 / 利率过高」的负向 carry 候选(R29)。守卫关闭时原样返回。
// 对每个负向 carry 候选:估算需借量 → 查最大可借量(best-effort)→ 取真实每小时利率 → 纯决策 decideBorrowAdmission。
// 被剔除者记日志 + 告警;查询失败不剔除(advisory 不误杀,裸腿仍由开仓守卫兜底)。
func (e *FundingRateSniperEngine) filterTargetsByBorrowCapacity(ctx context.Context, targets []*MonitoredTarget) []*MonitoredTarget {
	cfg := e.borrowAdmissionConfigFromRisk()
	if !cfg.Enabled || len(targets) == 0 {
		return targets
	}
	kept := make([]*MonitoredTarget, 0, len(targets))
	for _, target := range targets {
		need := e.estimateRequiredBorrowBaseQty(target)
		if need <= 0 {
			kept = append(kept, target) // 正向 carry / 无需借币
			continue
		}
		asset := strings.ToUpper(strings.TrimSpace(target.Candidate.SpotBaseAsset))
		maxBorrowable, known := e.queryMaxBorrowable(ctx, asset)
		res := decideBorrowAdmission(borrowAdmissionInput{
			Asset:              asset,
			RequiredBaseQty:    need,
			MaxBorrowableQty:   maxBorrowable,
			MaxBorrowableKnown: known,
			HourlyInterestRate: e.hourlyBorrowInterestRateForAsset(asset),
		}, cfg)
		if res.Admit {
			kept = append(kept, target)
			continue
		}
		if e.logger != nil {
			e.logger.Printf("R29 借币容量守卫剔除候选 %s: %s", target.Candidate.Symbol, res.Reason)
		}
		e.notifyBorrowCapacityBlock(target.Candidate.Symbol, res.Reason)
	}
	return kept
}

// queryMaxBorrowable best-effort 查询某资产当前最大可借量。返回 (qty, known):
//   - 成功 → (qty, true);
//   - 查询返回**确定性「借不到」**错误(币安 -3045 系统无可借 / -3006 超最大可借 / -3008 禁止借入)→ (0, true):
//     这不是「未知」,而是交易所明确告诉你「此刻最大可借≈0」,据此剔除候选(选币阶段干净跳过,绝不再 committed
//     合约腿后撞 -3045 白开又回补、污染冷却)。HOME 这类币安杠杆池无可借资产的长尾负费率币会在此被根本性跳过。
//   - 其它(网络抖动/限频/超时等)真·未知 → (0, false):advisory 不剔除,避免一次只读查询失败误杀机会,
//     裸腿仍由开仓守卫与恢复协议兜底。
func (e *FundingRateSniperEngine) queryMaxBorrowable(ctx context.Context, asset string) (float64, bool) {
	if e == nil || e.client == nil || strings.TrimSpace(asset) == "" {
		return 0, false
	}
	qty, err := e.client.MaxBorrowable(ctx, asset)
	if err != nil {
		if isBorrowUnavailableError(err) {
			if e.logger != nil {
				e.logger.Printf("R29 查最大可借量返回确定性借不到(剔除该候选,不再开合约腿) asset=%s err=%v", asset, err)
			}
			// 缓存「确定性借不到」(A 方案):下轮海选在 Top-N 截断前就跳过该资产,把名额让给可执行候选,
			// 不再让 HOME 这类长尾币每轮霸占监控名额(详见 borrow_availability_cache.go)。
			e.markBorrowAssetUnavailable(asset)
			return 0, true // 确定性借不到:最大可借≈0 → decideBorrowAdmission 据此剔除
		}
		if e.logger != nil {
			e.logger.Printf("R29 查询最大可借量失败(真·未知,不据此剔除) asset=%s err=%v", asset, err)
		}
		return 0, false
	}
	return qty, true
}

// notifyBorrowCapacityBlock 推送借币容量守卫剔除候选的告警(warning,经 S5-A 出口)。
func (e *FundingRateSniperEngine) notifyBorrowCapacityBlock(symbol, reason string) {
	if e == nil || e.alertDispatcher == nil {
		return
	}
	e.alertDispatcher.Notify(Alert{
		Level:      EventLevelWarning,
		Title:      "借币容量守卫剔除开仓候选",
		Detail:     symbol + ": " + reason,
		Symbol:     symbol,
		OccurredAt: e.serverNow(),
	})
}
