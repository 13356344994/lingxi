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
	liqAtDet int // 探测时刻本币官方爆仓已到笔数(确认=计数超过它)
}

var (
	fastTapeMu      sync.Mutex
	fastTapeQueue   []fastTapePending
	fastTapeEvalAt  = map[string]time.Time{} // 同币评估冷却(30s;与探测器冷却一致)
)

// fastTapeEnqueue 探测器识别即入队(调用方持 fastWSState.mu;非阻塞)。
func fastTapeEnqueue(sym, side string, burstAt time.Time, notional, multiple float64, liqAtDet int) {
	fastTapeMu.Lock()
	defer fastTapeMu.Unlock()
	fastTapeQueue = append(fastTapeQueue, fastTapePending{sym, side, burstAt, notional, multiple, liqAtDet})
	if len(fastTapeQueue) > 64 {
		fastTapeQueue = append([]fastTapePending{}, fastTapeQueue[len(fastTapeQueue)-64:]...)
	}
}

// fastTapeDrainConfirmed 纯逻辑:取出"已被官方爆仓确认"的挂起项,丢弃过期项,其余留队。
// 确认 = 该币官方爆仓计数(30s窗)> 探测时刻计数,即 burst 之后官方推送到达 = 这确实是强平流。
func fastTapeDrainConfirmed(now time.Time, windowSec int, liqCount func(sym string, since time.Time) int) []fastTapePending {
	fastTapeMu.Lock()
	defer fastTapeMu.Unlock()
	var out []fastTapePending
	var rest []fastTapePending
	for _, p := range fastTapeQueue {
		age := now.Sub(p.burstAt)
		if age > time.Duration(windowSec)*time.Second {
			continue // 过期未确认=纯大单(离线:中位+4.2 不过门),丢弃
		}
		if liqCount(p.sym, p.burstAt) > 0 {
			out = append(out, p)
		} else {
			rest = append(rest, p)
		}
	}
	fastTapeQueue = rest
	return out
}

func (e *OKXDirectionalEngine) fastTapeLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_tape] loop panic 已恢复: %v", r)
		}
	}()
	e.logger.Printf("[OKX方向][fast_tape] tape_burst 线启动(burst→官方确认→当刻资格→taker;live=%v,关=只记shadow)", e.cfg.FastTapeLiveEnabled)
	t := time.NewTicker(150 * time.Millisecond) // 亚秒消费:确认到达后 ≤150ms 内评估
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
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
	confirmed := fastTapeDrainConfirmed(now, win, func(sym string, since time.Time) int {
		n := 0
		fastLiqBufMu.Lock()
		for _, l := range fastLiqBuf[sym] {
			if l.ts.After(since) {
				n++
			}
		}
		fastLiqBufMu.Unlock()
		return n
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
		ev := e.buildFastEvent(ctx, p.sym, now) // REST 盘口+深度(探针期口径;延迟计入执行差距)
		if ev == nil {
			continue
		}
		ev.CandidateSource = "tape_burst"
		ev.FastCandidateSide = p.side
		ev.FastCandidateReason = fmt.Sprintf("tape_burst_%.0fx_%.0fU", p.multiple, p.notional)
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
		case ev.SpreadBps > maxSpread:
			skip = "spread_wide"
		case ev.DepthNotional10bps < e.cfg.FastPondDepthLoUSDT:
			skip = "depth_thin"
		case ev.DepthNotional10bps > e.cfg.FastLiqFollowDepthHiUSDT && e.cfg.FastLiqFollowDepthHiUSDT > 0:
			skip = "depth_institutional"
		}
		ev.GateSkip = skip
		_ = appendJSONLRecord(filepath.Join(e.recordDir, fastExecShadowFile), *ev)
		if skip != "" {
			e.logger.Printf("[fast_tape] %s 跳过(%s) spread=%.1f depth10=%.0f age=%dms", p.sym, skip, ev.SpreadBps, ev.DepthNotional10bps, ev.EventAgeMs)
			continue
		}
		r := e.fastLiveOpen(ctx, ev)
		e.logger.Printf("[fast_tape] %s %s %.0fx burst+确认 age=%dms spread=%.1f → %s", p.sym, p.side, p.multiple, ev.EventAgeMs, ev.SpreadBps, r)
	}
}
