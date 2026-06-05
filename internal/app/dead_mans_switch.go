package app

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// dead_mans_switch.go 实现 S7-B「交易所侧 dead-man 开关」(R14):
// 调用 Binance 合约 POST /fapi/v1/countdownCancelAll 为每个活跃合约设置一个倒计时;只要本进程存活,
// 就按 countdown/3 的节奏持续续期(把倒计时重置回满)。一旦进程崩溃/失联/被 kill,倒计时不再续期,
// 交易所会在 countdown 到期后自动撤销该账户该合约的全部挂单 —— 这是「进程死了挂单还在裸奔」的最后兜底。
//
// 续期(arm)与停用(disarm,countdownTime=0)都走签名请求;间隔/币种选择是纯函数,可单测。

const deadManMinRenewInterval = 3 * time.Second

// deadManRenewInterval 由倒计时长度推导续期节奏:countdown/3,保证到期前至少续期两次;下限 deadManMinRenewInterval。
// countdown<=0 → 0(表示不启用)。
func deadManRenewInterval(countdown time.Duration) time.Duration {
	if countdown <= 0 {
		return 0
	}
	interval := countdown / 3
	if interval < deadManMinRenewInterval {
		interval = deadManMinRenewInterval
	}
	if interval >= countdown {
		interval = countdown / 2
	}
	if interval <= 0 {
		interval = deadManMinRenewInterval
	}
	return interval
}

// deadManArmSymbols 从活跃对冲持仓提取需要武装的(去重、有序)合约符号集。
func deadManArmSymbols(hedges []OpenHedge) []string {
	set := make(map[string]struct{}, len(hedges))
	for _, h := range hedges {
		sym := h.Target.Symbol
		if sym == "" {
			continue
		}
		set[sym] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for sym := range set {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

// startDeadMansSwitch 启动 dead-man 续期 goroutine。未启用或倒计时无效则 no-op。
// ctx 取消(优雅停机/进程退出信号传导)时,用一个独立的短超时 context 尽力停用倒计时(countdownTime=0),
// 避免重启后旧倒计时误撤新实例的挂单;崩溃路径无法走到这里 → 由交易所到期兜底撤单(正是所需语义)。
func (e *FundingRateSniperEngine) startDeadMansSwitch(ctx context.Context) {
	if e == nil || !e.cfg.RiskConfig.DeadMansSwitchEnabled {
		return
	}
	countdown := e.cfg.RiskConfig.DeadMansSwitchCountdown
	interval := deadManRenewInterval(countdown)
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		e.renewDeadMansSwitch(ctx, countdown)
		for {
			select {
			case <-ctx.Done():
				disarmCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				e.disarmDeadMansSwitch(disarmCtx)
				cancel()
				return
			case <-ticker.C:
				e.renewDeadMansSwitch(ctx, countdown)
			}
		}
	}()
}

// renewDeadMansSwitch 为当前所有活跃合约续期倒计时。
func (e *FundingRateSniperEngine) renewDeadMansSwitch(ctx context.Context, countdown time.Duration) {
	symbols := deadManArmSymbols(e.snapshotActiveOpenHedges())
	if len(symbols) == 0 {
		return
	}
	ms := countdown.Milliseconds()
	for _, sym := range symbols {
		if err := e.setCountdownCancelAll(ctx, sym, ms); err != nil && e.logger != nil {
			e.logger.Printf("[dead-man] 续期失败 symbol=%s countdown=%dms err=%v", sym, ms, err)
		}
	}
}

// disarmDeadMansSwitch 停用所有活跃合约的倒计时(countdownTime=0),用于优雅停机。
func (e *FundingRateSniperEngine) disarmDeadMansSwitch(ctx context.Context) {
	symbols := deadManArmSymbols(e.snapshotActiveOpenHedges())
	for _, sym := range symbols {
		if err := e.setCountdownCancelAll(ctx, sym, 0); err != nil && e.logger != nil {
			e.logger.Printf("[dead-man] 停用失败 symbol=%s err=%v", sym, err)
		}
	}
	if e.logger != nil && len(symbols) > 0 {
		e.logger.Printf("[dead-man] 优雅停机已停用倒计时: symbols=%v", symbols)
	}
}

// setCountdownCancelAll 调 Binance 合约 countdownCancelAll。countdownTimeMs=0 表示停用倒计时。
func (e *FundingRateSniperEngine) setCountdownCancelAll(ctx context.Context, symbol string, countdownTimeMs int64) error {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("countdownTime", strconv.FormatInt(countdownTimeMs, 10))
	_, err := e.signedRequest(ctx, http.MethodPost, e.client.FuturesBaseURL()+"/fapi/v1/countdownCancelAll", params)
	return err
}
