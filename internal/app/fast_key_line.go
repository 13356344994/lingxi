package app

// ===== 天气钥匙探针线(2026-07-09 晚;四号任务 join 的直接产物,纪律 14j「探针=常开仪表」)=====
//
// join 证据(`_prize_join_study.py` n=2056奖品/4112对照,train/holdout 双侧):
//   where=惯犯暴动计数 AUC 0.91/0.91;when=前15min暴动 0.84/0.85;
//   方向(条件于奖品):空头爆占优→71%/68% 向下;多头爆占优→58%/51% 向上;
//   无条件 EV(`_key_ev_list.py`):key_sqz_fade×30m train +4.3 中位/PF1.19,holdout -3.0——
//   打平边缘。按用户指令七,探针不是考试奖品是常开仪表:上膛后由滚动净值持续投票,
//   热加码(资金阶梯自动)/冷缩底仓/连续冷缴械(治理器自动)。
//
// 两个打法(口径=研究冻结,14f 同源):
//   key_sqz_fade:900s 内空头爆 ≥2 且 > 多头爆(轧空进行中)×惯犯币(24h暴动≥134)→ 做空;
//   key_casc_fade:900s 内多头爆 ≥2 且 > 空头爆(瀑布进行中)×惯犯币 → 做多;
//   资格:点差≤12(REST 复核);同币同源 7200s 去重;剔主流币。
//   出场(死板,纪律 14k):止损 -150bps 封顶 + 固定 30min 到点,永不灵活。
// 实弹=source 进 fast_probe_source 名单(双钥匙;不在名单=record_only_source 照记 shadow)。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	fastKeyWindowSec   = 900
	fastKeyMinLiqs     = 2
	fastKeyHabitMin    = 134   // join 冻结切点(prior24h census 暴动数 75 分位)
	fastKeyCooldownSec = 7200
	fastKeyStopBps     = 150.0 // 死板止损(研究口径;纪律 14k 永不灵活)
	fastKeyHoldSec     = 1800.0
)

// fastKeyMajors 主流币排除集(奖品地图口径:钱在长尾)。
var fastKeyMajors = map[string]bool{
	"BTC-USDT": true, "ETH-USDT": true, "SOL-USDT": true, "XRP-USDT": true, "BNB-USDT": true,
	"DOGE-USDT": true, "ADA-USDT": true, "LTC-USDT": true, "LINK-USDT": true, "AVAX-USDT": true,
	"TRX-USDT": true, "DOT-USDT": true, "TON-USDT": true,
}

// ---- 24h 暴动环(惯犯钥匙的数据地板;census 发射点喂,重启从文件尾回放)----
var (
	fastRiot24Mu   sync.Mutex
	fastRiot24Hist = map[string][]time.Time{}
)

func fastNoteRiot24(sym string, now time.Time) {
	fastRiot24Mu.Lock()
	defer fastRiot24Mu.Unlock()
	cut := now.Add(-24 * time.Hour)
	h := append(fastRiot24Hist[sym], now)
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	fastRiot24Hist[sym] = h[i:]
	if len(fastRiot24Hist) > 4096 {
		for k, v := range fastRiot24Hist {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > 25*time.Hour {
				delete(fastRiot24Hist, k)
			}
		}
	}
}

func fastRiot24Count(sym string, now time.Time) int {
	cut := now.Add(-24 * time.Hour)
	fastRiot24Mu.Lock()
	defer fastRiot24Mu.Unlock()
	n := 0
	for _, t := range fastRiot24Hist[sym] {
		if !t.Before(cut) {
			n++
		}
	}
	return n
}

// fastKeyRestoreRiots 重启回放:census 文件尾部恢复 24h 暴动环(与 fastPondRestore 同哲学:
// 惯犯钥匙是 24h 慢状态,重启失忆=枪线 fail-closed 空转一天)。
// 注:loadTailJSONLFile 的 since 过滤只认识结算类型(匿名结构会被全滤),此处 since 传零值、
// 取尾部 12 万行后自己按 recorded_at 过滤。
func (e *OKXDirectionalEngine) fastKeyRestoreRiots() {
	rows, err := loadTailJSONLFile[struct {
		RecordedAt time.Time `json:"recorded_at"`
		Symbol     string    `json:"symbol"`
	}](filepath.Join(e.recordDir, fastMoverCensusFile), time.Time{}, 120000)
	if err != nil {
		return
	}
	cut := e.nowFn().UTC().Add(-24 * time.Hour)
	n := 0
	for _, r := range rows {
		if r.Symbol != "" && !r.RecordedAt.IsZero() && r.RecordedAt.UTC().After(cut) {
			fastNoteRiot24(r.Symbol, r.RecordedAt.UTC())
			n++
		}
	}
	if n > 0 {
		e.logger.Printf("[fast_key] 24h暴动环回放完成: %d 条(惯犯钥匙重启不失忆)", n)
	}
}

// fastKeyTrigger 纯判定:返回触发的 (source, side);""=不触发。
// 口径=join/EV 研究冻结:该侧爆仓≥2 且 > 对侧;惯犯≥134;非主流。
func fastKeyTrigger(sym string, nLong, nShort, riot24 int) (string, string) {
	if fastKeyMajors[sym] || riot24 < fastKeyHabitMin {
		return "", ""
	}
	if nShort >= fastKeyMinLiqs && nShort > nLong {
		return "key_sqz_fade", "short" // 空头正被轧(价被顶上去)→ 竭尽做空
	}
	if nLong >= fastKeyMinLiqs && nLong > nShort {
		return "key_casc_fade", "long" // 多头正被瀑(价被砸下去)→ 竭尽做多
	}
	return "", ""
}

// fastKeyExit 纯判定:死板出场(纪律 14k;""=持有)。ubps=方向化浮盈。
func fastKeyExit(ubps, holdSec float64) string {
	if ubps <= -fastKeyStopBps {
		return "fast_key_stop"
	}
	if holdSec >= fastKeyHoldSec {
		return "fast_key_fixed30m"
	}
	return ""
}

var (
	fastKeyMu   sync.Mutex
	fastKeyLast = map[string]time.Time{} // source|sym -> 上次触发(7200s 去重)
)

func (e *OKXDirectionalEngine) fastKeyLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_key] loop panic 已恢复: %v", r)
		}
	}()
	e.fastKeyRestoreRiots()
	e.logger.Printf("[OKX方向][fast_key] 天气钥匙线启动(轧空竭尽空/瀑布竭尽多×惯犯币;死板止损%.0f/固定%.0fs;实弹=probe名单)",
		fastKeyStopBps, fastKeyHoldSec)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !e.fastScalperEnabled() || !e.cfg.FastKeyLineEnabled {
				continue
			}
			e.fastKeyTick(ctx)
		}
	}
}

func (e *OKXDirectionalEngine) fastKeyTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_key] tick panic 已恢复: %v", r)
		}
	}()
	now := e.nowFn().UTC()
	// 近 4s 有新爆仓的币才评估(事件驱动;buf 已持 15min)
	fastLiqBufMu.Lock()
	cand := make([]string, 0, 8)
	for sym, arr := range fastLiqBuf {
		if len(arr) > 0 && now.Sub(arr[len(arr)-1].ts) <= 4*time.Second {
			cand = append(cand, sym)
		}
	}
	fastLiqBufMu.Unlock()
	for _, sym := range cand {
		nLong := fastSymLiqSideCount(sym, now, fastKeyWindowSec, "long")
		nShort := fastSymLiqSideCount(sym, now, fastKeyWindowSec, "short")
		src, side := fastKeyTrigger(sym, nLong, nShort, fastRiot24Count(sym, now))
		if src == "" {
			continue
		}
		key := src + "|" + sym
		fastKeyMu.Lock()
		last, ok := fastKeyLast[key]
		if ok && now.Sub(last) < fastKeyCooldownSec*time.Second {
			fastKeyMu.Unlock()
			continue
		}
		fastKeyLast[key] = now
		if len(fastKeyLast) > 4096 {
			for k, v := range fastKeyLast {
				if now.Sub(v) > 3*time.Hour {
					delete(fastKeyLast, k)
				}
			}
		}
		fastKeyMu.Unlock()
		go e.fastKeyFire(ctx, sym, src, side, nLong, nShort, now)
	}
}

// fastSymLiqSideCount 该币窗口内指定方向爆仓笔数。
func fastSymLiqSideCount(sym string, now time.Time, windowSec int, side string) int {
	cut := now.Add(-time.Duration(windowSec) * time.Second)
	fastLiqBufMu.Lock()
	defer fastLiqBufMu.Unlock()
	n := 0
	for _, l := range fastLiqBuf[sym] {
		if !l.ts.Before(cut) && l.posSide == side {
			n++
		}
	}
	return n
}

// fastKeyFire REST 建档 → 资格 → shadow → fastLiveOpen(不在探针名单=record_only_source)。
func (e *OKXDirectionalEngine) fastKeyFire(ctx context.Context, sym, src, side string, nLong, nShort int, detAt time.Time) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_key] fire panic 已恢复: %v", r)
		}
	}()
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ev := e.buildFastEvent(fctx, sym, e.nowFn().UTC())
	if ev == nil {
		return
	}
	ev.CandidateSource = src
	ev.FastCandidateSide = side
	ev.SnapshotID += "|" + src
	ev.EventAgeMs = e.nowFn().UTC().Sub(detAt).Milliseconds()
	ev.FastCandidateReason = fmt.Sprintf("%s_L%d_S%d_r24_%d", src, nLong, nShort, fastRiot24Count(sym, detAt))
	ev.MarketLiq5m = fastMarketLiqRate(e.nowFn().UTC(), 300)
	skip := ""
	if ev.SpreadBps > 12 {
		skip = "spread_wide"
	}
	ev.GateSkip = skip
	_ = appendJSONLRecord(filepath.Join(e.recordDir, fastExecShadowFile), *ev)
	if skip != "" {
		return
	}
	r := e.fastLiveOpen(fctx, ev)
	if r != "record_only_source" || strings.TrimSpace(e.cfg.FastProbeSource) != "" {
		e.logger.Printf("[fast_key] %s %s L%d/S%d spread=%.1f age=%dms → %s", sym, src, nLong, nShort, ev.SpreadBps, ev.EventAgeMs, r)
	}
}
