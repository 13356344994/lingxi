package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

// ===== v6 tape_burst 策略线(2026-07-05 预置;默认关死)=====
// 完整通路:亚秒探测器识别 burst(38ms)→ 入挂起队列 → 官方爆仓推送确认(≤3s 窗)→
// 当刻资格(点差/深度,用 REST 现拉盘口=探针期口径,延迟已在离线 +1s 研究覆盖内)→
// taker 进场 → trail 棘轮出场(复用 liq_follow 已预置的出场分支)。
// 每一步的时间戳落在结算里(执行差距追踪的原料)。开火权:FastTapeLiveEnabled(护栏锚定,
// 默认 false=挂起队列照记 shadow 不下单)+ 探针治理器(fast_probe_source=tape_burst 时预算锁死)。

type fastTapePending struct {
	sym      string
	side     string // long/short(burst 主导方向)
	burstAt  time.Time
	notional float64
	multiple float64
	liqAtDet int  // 探测时刻本币官方爆仓已到笔数(确认=计数超过它)
	sideMatch bool // v6.2:确认推送的被爆方向与 burst 方向是否对号(多数票)
}

var (
	fastTapeMu      sync.Mutex
	fastTapeQueue   []fastTapePending
	fastTapeEvalAt  = map[string]time.Time{} // 同币评估冷却(30s;与探测器冷却一致)
	// v6.4 事件驱动唤醒(2026-07-06 延迟解剖:自身排队脂肪中位75ms/P90 134ms=纯轮询等待):
	// 官方爆仓推送落地即踢醒评估循环,不再等下一个 tick。缓冲1,满了丢=已有唤醒在途。
	fastTapeKick = make(chan struct{}, 1)
)

// fastTapeKickNow 非阻塞唤醒 tape 评估循环(fastNoteLiq=WS 推送线程调用,绝不能卡)。
func fastTapeKickNow() {
	select {
	case fastTapeKick <- struct{}{}:
	default:
	}
}

// fastTapeEnqueue 探测器识别即入队(调用方持 fastWSState.mu;非阻塞)。
func fastTapeEnqueue(sym, side string, burstAt time.Time, notional, multiple float64, liqAtDet int) {
	fastTapeMu.Lock()
	defer fastTapeMu.Unlock()
	fastTapeQueue = append(fastTapeQueue, fastTapePending{sym: sym, side: side, burstAt: burstAt, notional: notional, multiple: multiple, liqAtDet: liqAtDet})
	if len(fastTapeQueue) > 64 {
		fastTapeQueue = append([]fastTapePending{}, fastTapeQueue[len(fastTapeQueue)-64:]...)
	}
}

// fastTapeDrainConfirmed 纯逻辑:取出"已被官方爆仓确认"的挂起项,丢弃过期项,其余留队。
// 确认 = burst 之后该币官方爆仓推送到达 = 这确实是强平流。
// v6.2 方向核对(2026-07-05 用户质询"确认结合了什么信息"后授权):liqSides 返回 burst 后到达的
// 被爆仓方向;sideMatch = 多数票对号(卖方burst应伴随 long 被爆,买方burst应伴随 short 被爆)。
// 定义级核对:对不上号=这次 burst 不是我们以为的那场强平(实测 95 确认中错号 6 笔中位 -10.7 更差)。
func fastTapeDrainConfirmed(now time.Time, windowSec int, liqSides func(sym string, since time.Time) []string) []fastTapePending {
	fastTapeMu.Lock()
	defer fastTapeMu.Unlock()
	var out []fastTapePending
	var rest []fastTapePending
	for _, p := range fastTapeQueue {
		age := now.Sub(p.burstAt)
		if age > time.Duration(windowSec)*time.Second {
			continue // 过期未确认=纯大单(离线:中位+4.2 不过门),丢弃
		}
		sides := liqSides(p.sym, p.burstAt)
		if len(sides) > 0 {
			want := "short" // 买方burst(跟多)← 空头被爆(强平买回)
			if p.side == "short" {
				want = "long" // 卖方burst(跟空)← 多头被爆(强平抛售)
			}
			match := 0
			for _, s := range sides {
				if s == want {
					match++
				}
			}
			p.sideMatch = match*2 >= len(sides)
			out = append(out, p)
		} else {
			rest = append(rest, p)
		}
	}
	fastTapeQueue = rest
	return out
}

// fastTapeSelSkip 纯函数:v6.3 选择层判定(""=通过)。切点=07-05 组合扫描冻结值,由护栏测试锚定。
// ㊀ 磁带活跃(基线=notional/multiple ≥ minBase)× 占市值比(notional/OI名义 ≥ minRatio);
// ㊂ 追加方向侧突破(买侧 pos≥0.9 / 卖侧 pos≤0.1,30min 区间)。
// fail-closed:倍数/OI/区间任一缺失即跳过——探针的每一发都必须站在完整信息集上。
func fastTapeSelSkip(selOn, breakoutOn bool, minBase, minRatio, notional, multiple, oiNotional float64, oiOK bool, pos float64, posOK bool, side string) string {
	if !selOn {
		return ""
	}
	if multiple <= 0 || notional/multiple < minBase {
		return "sel_dead_tape"
	}
	if !oiOK || oiNotional <= 0 || notional/oiNotional < minRatio {
		return "sel_small_vs_oi"
	}
	if breakoutOn {
		if !posOK {
			return "sel_range_warmup"
		}
		if side == "long" && pos < 0.9 {
			return "sel_no_breakout"
		}
		if side == "short" && pos > 0.1 {
			return "sel_no_breakout"
		}
	}
	return ""
}

func (e *OKXDirectionalEngine) fastTapeLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_tape] loop panic 已恢复: %v", r)
		}
	}()
	e.logger.Printf("[OKX方向][fast_tape] tape_burst 线启动(burst→官方确认→当刻资格→taker;live=%v 方向核对=%v,关=只记shadow)", e.cfg.FastTapeLiveEnabled, e.cfg.FastTapeSideMatchEnabled)
	t := time.NewTicker(150 * time.Millisecond) // 兜底轮询(唤醒信号丢失时的保险)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-fastTapeKick: // v6.4 事件驱动:确认推送落地即评估(砍掉中位75ms排队脂肪)
			if !e.fastScalperEnabled() {
				continue
			}
			e.fastTapeTick(ctx)
		case <-t.C:
			if !e.fastScalperEnabled() {
				continue
			}
			e.fastTapeTick(ctx)
		}
	}
}

func (e *OKXDirectionalEngine) fastTapeTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_tape] tick panic 已恢复: %v", r)
		}
	}()
	now := e.nowFn().UTC()
	win := e.cfg.FastTapeConfirmWindowSec
	if win <= 0 {
		win = 3
	}
	confirmed := fastTapeDrainConfirmed(now, win, func(sym string, since time.Time) []string {
		var sides []string
		fastLiqBufMu.Lock()
		for _, l := range fastLiqBuf[sym] {
			if l.ts.After(since) {
				sides = append(sides, l.posSide)
			}
		}
		fastLiqBufMu.Unlock()
		return sides
	})
	for _, p := range confirmed {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// 同币评估冷却(30s)
		fastTapeMu.Lock()
		if t0, ok := fastTapeEvalAt[p.sym]; ok && now.Sub(t0) < 30*time.Second {
			fastTapeMu.Unlock()
			continue
		}
		fastTapeEvalAt[p.sym] = now
		if len(fastTapeEvalAt) > 2048 {
			for k, v := range fastTapeEvalAt {
				if now.Sub(v) > 10*time.Minute {
					delete(fastTapeEvalAt, k)
				}
			}
		}
		fastTapeMu.Unlock()
		evalT0 := e.nowFn().UTC()
		ev := e.buildFastEvent(ctx, p.sym, now) // REST 盘口+深度(v6.4 已并发;延迟计入执行差距)
		if ev == nil {
			continue
		}
		ev.EvalRestMs = e.nowFn().UTC().Sub(evalT0).Milliseconds() // v6.4 复测读数:REST组装耗时
		ev.CandidateSource = "tape_burst"
		ev.FastCandidateSide = p.side
		sm := "_sm0"
		if p.sideMatch {
			sm = "_sm1"
		}
		ev.FastCandidateReason = fmt.Sprintf("tape_burst_%.0fx_%.0fU%s", p.multiple, p.notional, sm)
		ev.SnapshotID += "|tape_burst"
		ev.EventAgeMs = now.Sub(p.burstAt).Milliseconds() // burst→评估全链延迟(执行差距核心指标)
		ev.LiqN = p.liqAtDet
		ev.MarketLiq5m = fastMarketLiqRate(now, 300)
		ev.XliqN30s = fastXliqCount(p.sym, now, 30)
		maxSpread := e.cfg.FastTapeMaxSpreadBps
		if maxSpread <= 0 {
			maxSpread = 12
		}
		skip := ""
		switch {
		case e.cfg.FastTapeSideMatchEnabled && !p.sideMatch:
			// v6.2 方向核对闸:错号=不是我们以为的那场强平。shadow 照记(带 _sm0 标记)=对照组不丢。
			skip = "confirm_side_mismatch"
		case ev.SpreadBps > maxSpread:
			skip = "spread_wide"
		case ev.DepthNotional10bps < e.cfg.FastPondDepthLoUSDT:
			skip = "depth_thin"
		case ev.DepthNotional10bps > e.cfg.FastLiqFollowDepthHiUSDT && e.cfg.FastLiqFollowDepthHiUSDT > 0:
			skip = "depth_institutional"
		}
		if skip == "" { // v6.3 选择层(㊀/㊂,切点冻结;fail-closed)
			oiN, oiOK := fastOKXOINotional(p.sym, ev.Mid, now)
			pos, posOK := fastRangePos(p.sym, ev.Mid)
			skip = fastTapeSelSkip(e.cfg.FastTapeSelEnabled, e.cfg.FastTapeSelBreakoutEnabled,
				e.cfg.FastTapeSelMinBaseUSDT, e.cfg.FastTapeSelMinNtlOIRatio,
				p.notional, p.multiple, oiN, oiOK, pos, posOK, p.side)
		}
		ev.GateSkip = skip
		_ = appendJSONLRecord(filepath.Join(e.recordDir, fastExecShadowFile), *ev)
		if skip != "" {
			e.logger.Printf("[fast_tape] %s 跳过(%s) spread=%.1f depth10=%.0f age=%dms", p.sym, skip, ev.SpreadBps, ev.DepthNotional10bps, ev.EventAgeMs)
			continue
		}
		// 多模块账本v0(2026-07-08):全闸通过的卖方burst(火灾)开虚拟空仓,程序S出场,
		// 把火刀格点接进织网(record-only;火刀 live 永远另有来源闸)。
		if p.side == "short" {
			e.fastLedgerOnFireConfirm(p.sym, ev.Mid, ev.SpreadBps, now)
		}
		r := e.fastLiveOpen(ctx, ev)
		e.logger.Printf("[fast_tape] %s %s %.0fx burst+确认 age=%dms spread=%.1f → %s", p.sym, p.side, p.multiple, ev.EventAgeMs, ev.SpreadBps, r)
	}
}
