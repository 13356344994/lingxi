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

// ===== 价差路径环(2026-07-08 ㊳工程队列①「E 决策信息集路径化」)=====
// 陷阱诊断:决策时刻只读 gap 一个标量,把"价差怎么诞生的"全部扔掉。本环以标尺节拍(~3s)
// 记录每币 (gap, okxMid) 路径,触发时刻产出三类路径信息(record-only,零行为改动):
//
//	生龄 AgeSec   —— 价差本轮 ≤-20bps 已持续多久(新裂口 vs 陈年裂口,与新鲜度粒子互补:
//	                 新鲜度问"半小时内正常过没有",生龄问"这次裂开多久了");
//	速度 Vel3/15  —— 3s/15s 价差变化(正=在收敛,负=还在扩大=接刀嫌疑);
//	归因 OkxRet/AnchorRet15 —— 15s 内 OKX 自身 vs Bybit 锚各走了多少:OKX 掉下来(本地卖压,
//	                 对手=恐慌盘)与 Bybit 涨上去(对面先行,对手=迟到的搬砖商)是两种局。
type fastGapPathPt struct {
	ts  time.Time
	gap float64
	okx float64
}

type fastGapPathStats struct {
	OK             bool
	AgeSec         float64 // 本轮 gap≤-20 连续时长(封顶=环窗)
	Vel3Bps        float64 // gap(now) - gap(~3s前);正=收敛中
	Vel15Bps       float64
	OkxRet15Bps    float64 // OKX mid 15s 收益
	AnchorRet15Bps float64 // Bybit 锚 15s 收益(锚=okx/(1+gap/1e4) 反解)
}

const fastGapPathWindowSec = 330.0
const fastGapPathBirthBps = -20.0

var (
	fastGapPathMu sync.Mutex
	fastGapPath   = map[string][]fastGapPathPt{}
)

// fastNoteGapPath 标尺循环逐币调用(有新鲜锚即记,~3s/点)。
func fastNoteGapPath(sym string, now time.Time, gapBps, okxMid float64) {
	fastGapPathMu.Lock()
	defer fastGapPathMu.Unlock()
	h := append(fastGapPath[sym], fastGapPathPt{now, gapBps, okxMid})
	cut := now.Add(-time.Duration(fastGapPathWindowSec) * time.Second)
	i := 0
	for i < len(h) && h[i].ts.Before(cut) {
		i++
	}
	fastGapPath[sym] = h[i:]
	if len(fastGapPath) > 4096 {
		for k, v := range fastGapPath {
			if len(v) == 0 || now.Sub(v[len(v)-1].ts) > time.Hour {
				delete(fastGapPath, k)
			}
		}
	}
}

// fastGapPathAt 纯读:触发时刻的路径统计(须先 fastNoteGapPath 记入当前点)。
func fastGapPathAt(sym string, now time.Time) fastGapPathStats {
	fastGapPathMu.Lock()
	defer fastGapPathMu.Unlock()
	h := fastGapPath[sym]
	if len(h) < 2 {
		return fastGapPathStats{}
	}
	cur := h[len(h)-1]
	anchorOf := func(p fastGapPathPt) float64 { return p.okx / (1 + p.gap/1e4) }
	// 回看点:最近一个距 cur ≥N 秒的样本(标尺 3s 节拍,容忍缺采)。
	lookback := func(sec float64) (fastGapPathPt, bool) {
		for i := len(h) - 2; i >= 0; i-- {
			if cur.ts.Sub(h[i].ts).Seconds() >= sec {
				return h[i], true
			}
		}
		return fastGapPathPt{}, false
	}
	st := fastGapPathStats{OK: true}
	if p3, ok := lookback(3); ok {
		st.Vel3Bps = cur.gap - p3.gap
	}
	if p15, ok := lookback(15); ok {
		st.Vel15Bps = cur.gap - p15.gap
		if p15.okx > 0 {
			st.OkxRet15Bps = (cur.okx/p15.okx - 1) * 1e4
		}
		if a := anchorOf(p15); a > 0 {
			st.AnchorRet15Bps = (anchorOf(cur)/a - 1) * 1e4
		}
	} else {
		st.OK = false // 连 15s 历史都没有 = 路径不可算(fail-closed,如实标注)
	}
	// 生龄:从 cur 往回走,gap≤birth 的连续段起点。
	birthAt := cur.ts
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].gap > fastGapPathBirthBps {
			break
		}
		birthAt = h[i].ts
	}
	st.AgeSec = cur.ts.Sub(birthAt).Seconds()
	return st
}

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
	fastNoteGapPath(sym, now, gap, okxMid) // ㊳①:价差路径环(生龄/速度/归因,3s 节拍)
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
	path := fastGapPathAt(sym, now)                    // 决策时刻的路径信息集(det 时刻冻结)
	e.fastLedgerOnTrigger(sym, okxMid, gap, now, path) // Layer A:粒子评估+虚拟建仓(record-only,同步廉价)
	go e.fastXgapFire(sym, gap, now, path)             // 异步:标尺循环不等 REST
}

// fastXgapFire 建档(REST 盘口+深度)→ 执行层守卫 → shadow 落盘 → fastLiveOpen(实弹闸默认锁死)。
func (e *OKXDirectionalEngine) fastXgapFire(sym string, detGapBps float64, detAt time.Time, path fastGapPathStats) {
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
	if path.OK { // ㊳①:决策时刻路径信息集(生龄/速度/归因),record-only 标注
		ev.GapAgeSec = path.AgeSec
		ev.GapVel3Bps = path.Vel3Bps
		ev.GapVel15Bps = path.Vel15Bps
		ev.GapOkxRet15Bps = path.OkxRet15Bps
		ev.GapAnchorRet15Bps = path.AnchorRet15Bps
	}
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
	e.fastLedgerOnTriggerREST(sym, ev.Mid, ev.Bid, ev.SpreadBps, detGapBps, gapNow, e.nowFn().UTC(), path)
	r := e.fastLiveOpen(ctx, ev)
	e.logger.Printf("[fast_xgap] %s long det=%.0fbps now=%.0fbps age=%dms spread=%.1f → %s", sym, detGapBps, gapNow, ev.EventAgeMs, ev.SpreadBps, r)
}
