package app

// E策略「捡便宜货」跨所价差回归·多头腿(v6.5,2026-07-07 入枪)。
//
// 机制(天道条款):OKX 与 Bybit 同一张永续,套利商不会放着价差不搬。OKX mid 比 Bybit 锚
// 便宜 ≥40bps(锚新鲜度≤8s)= 本地卖压过冲,买 OKX 等收敛;收敛≤10bps 或 600s 到点离场,
// -60bps 灾难无条件砍,同币 1800s 冷却一枪。
//
// 离线证据(07-04 22:00 → 07-07 04:00,54h 全市场 mid+锚回放,taker 成本 12bps 已扣):
//   n=1142 净合计+9656bps 净中位+4.6 净PF1.44 胜率54% 中位持有27s;
//   剔除单一顶币(IP)后 n=1067 净+12312 PF1.78——不靠单币;台阶 40/50/60/70 四档全正(单调性);
//   按 6h 分桶 9/10 桶正(时段稳健)。参数 40/10/600/60 为先验预选,非扫描后挑优。
// 判负的变体(同数据同期,防止复活):空头腿(OKX 贵→做空)净-14958(贵=真拉升,逆动量接飞刀);
//   三所共识锚净为负(共识锚的"偏离"多为慢所滞后,不是本所错价)。
// 前向判决(预注册,见 HANDOFF ㉒):影子攒 n≥300 后裁决,净中位≥+3bps 且 PF≥1.2 → 递探针申请。
//
// 执行层守卫(入枪时即定,先于前向数据,非事后调参):REST 盘口点差 > FastTapeMaxSpreadBps
// (默认12bps)跳过——离线回放按 mid 记账,宽点差币的"价差"吃不到;REST 时刻价差已收敛到
// exit 线以内(机会没了)跳过。两者 shadow 照记(GateSkip 标注)= 对照组不丢。
//
// 实弹闸 fast_xgap_live_enabled 默认 false(护栏锚定),开火前提:前向判决过 + 用户明示批探针。

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

var (
	fastXgapMu   sync.Mutex
	fastXgapLast = map[string]time.Time{} // sym -> 上次触发(同币冷却)
)

// fastXgapFreshTol 锚新鲜度容忍(冻结口径,与离线回放的 8s 一致=两个采集节拍)。
const fastXgapFreshTol = 8 * time.Second

// fastXgapShouldFire 纯判定:入场价差触发 + 同币冷却(gapBps 负=OKX 便宜)。
func fastXgapShouldFire(gapBps, entryGapBps float64, last, now time.Time, cooldownSec int) bool {
	if entryGapBps <= 0 || gapBps > -entryGapBps {
		return false
	}
	if !last.IsZero() && now.Sub(last) < time.Duration(cooldownSec)*time.Second {
		return false
	}
	return true
}

// fastXgapCheck 全市场标尺每轮逐币调用(廉价:两次 map 查找;触发才走 REST)。
func (e *OKXDirectionalEngine) fastXgapCheck(sym string, okxMid float64, now time.Time) {
	if !e.cfg.FastXgapShadowEnabled {
		return
	}
	gap, ok := fastXMidGapBpsVenueTol("bybit", sym, okxMid, now, fastXgapFreshTol)
	if !ok {
		return
	}
	if e.cfg.FastParticleLedgerEnabled {
		fastNoteGapHist(sym, now, gap) // Layer A:价差历史环(新鲜度粒子数据地板,60s降采样)
	}
	fastXgapMu.Lock()
	fire := fastXgapShouldFire(gap, e.cfg.FastXgapEntryGapBps, fastXgapLast[sym], now, e.cfg.FastXgapCooldownSeconds)
	if fire {
		fastXgapLast[sym] = now
		if len(fastXgapLast) > 2048 {
			for k, v := range fastXgapLast {
				if now.Sub(v) > 2*time.Hour {
					delete(fastXgapLast, k)
				}
			}
		}
	}
	fastXgapMu.Unlock()
	if !fire {
		return
	}
	e.fastLedgerOnTrigger(sym, okxMid, gap, now) // Layer A:粒子评估+虚拟建仓(record-only,同步廉价)
	go e.fastXgapFire(sym, gap, now)             // 异步:标尺循环不等 REST
}

// fastXgapFire 建档(REST 盘口+深度)→ 执行层守卫 → shadow 落盘 → fastLiveOpen(实弹闸默认锁死)。
func (e *OKXDirectionalEngine) fastXgapFire(sym string, detGapBps float64, detAt time.Time) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_xgap] panic recovered: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := e.nowFn().UTC()
	ev := e.buildFastEvent(ctx, sym, now)
	if ev == nil {
		return
	}
	ev.CandidateSource = "xgap_rev"
	ev.FastCandidateSide = "long" // 只做多头腿(空头腿已判负,见文件头)
	ev.SnapshotID += "|xgap_rev"
	ev.EventAgeMs = e.nowFn().UTC().Sub(detAt).Milliseconds()
	// REST 时刻复核价差(锚同刻):记录进 reason;已收敛到 exit 线以内 = 机会没了。
	gapNow, gapOK := fastXMidGapBpsVenueTol("bybit", sym, ev.Mid, e.nowFn().UTC(), fastXgapFreshTol)
	ev.FastCandidateReason = fmt.Sprintf("xgap_rev_det%.0f_now%.0f", detGapBps, gapNow)
	maxSpread := e.cfg.FastTapeMaxSpreadBps
	if maxSpread <= 0 {
		maxSpread = 12
	}
	skip := ""
	switch {
	case !gapOK || gapNow > -e.cfg.FastXgapExitGapBps:
		skip = "gap_gone"
	case ev.SpreadBps > maxSpread:
		skip = "spread_wide" // 宽点差币的 mid 价差吃不到(离线按 mid 记账的执行修正)
	}
	ev.GateSkip = skip
	_ = appendJSONLRecord(filepath.Join(e.recordDir, fastExecShadowFile), *ev)
	if skip != "" {
		e.logger.Printf("[fast_xgap] %s 跳过(%s) det=%.0fbps now=%.0fbps spread=%.1f", sym, skip, detGapBps, gapNow, ev.SpreadBps)
		return
	}
	// Layer A v1/v2 可执行+maker 双口径:守卫通过=真枪会打的那批,虚拟仓按 REST 价格建仓(record-only)
	e.fastLedgerOnTriggerREST(sym, ev.Mid, ev.Bid, ev.SpreadBps, detGapBps, gapNow, e.nowFn().UTC())
	r := e.fastLiveOpen(ctx, ev)
	e.logger.Printf("[fast_xgap] %s long det=%.0fbps now=%.0fbps age=%dms spread=%.1f → %s", sym, detGapBps, gapNow, ev.EventAgeMs, ev.SpreadBps, r)
}
