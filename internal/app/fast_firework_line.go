package app

// ===== 烟花枪(2026-07-09 三枪会战造枪;用户目标"一天内全系统实盘")=====
//
// 机制:暴力拉升的纯 FOMO 竭尽顶反手做空。对手=最后一批追多的散户(忍不住)+
// 已竭尽的买力(不得不停)。出场=固定 300s(回归局,trail 已证是绞肉机)+ -40bps 灾难线。
//
// 三特征闸(K 折 8/8 过检,n=106 中位-0.1/PF1.36,`_firework_kfold_study.py` 冻结口径):
//   ①非惯犯:此前 24h 同币烟花细胞 <3 次(惯犯=控盘反复拉砸币,灾难率 77%)
//   ②完全力竭:ret15 < 5%×ret60(买力彻底枯竭;细胞门 25% 只是"减速")
//   ③无本币爆仓:5min 官方爆仓=0(有空头爆=轧空回补流未走完,反手=接刀)
// 切点=冻结值(改=作弊,护栏锚定);点差≤6 在细胞层已闸。
//
// 开火权:FastFireworkLiveEnabled(默认 false 护栏锚定)+ firework_pure 进探针名单
// (双钥匙制);live 关死时全流程照跑只记 shadow=对照组不丢。

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

var (
	fastFwFireMu   sync.Mutex
	fastFwFireLast = map[string]time.Time{} // 同币冷却(600s,与 K 折研究去重同口径)
)

// fastFireworkPureSkip 纯判定:三特征闸(""=通过;冻结切点 3/0.05/0)。
func fastFireworkPureSkip(prior24h int, ret15, ret60 float64, symLiq5m int) string {
	if prior24h >= 3 {
		return "fw_habitual" // 惯犯币:控盘反复拉砸,反手=站在庄家对面
	}
	if ret60 <= 0 || ret15 >= 0.05*ret60 {
		return "fw_not_exhausted" // 买力未彻底枯竭(细胞层 25% 只算减速)
	}
	if symLiq5m > 0 {
		return "fw_squeeze_flow" // 有空头爆仓=轧空回补流未走完
	}
	return ""
}

// fastFireworkFire 烟花枪入口(标尺循环细胞命中处调用;live 锁死时只记 shadow)。
func (e *OKXDirectionalEngine) fastFireworkFire(sym string, mid, ret15, ret60, spreadBps float64, symLiq5m int, now time.Time) {
	// 同币 600s 冷却(与 K 折研究口径一致;细胞层 120s 去重对枪太密)
	fastFwFireMu.Lock()
	if last, ok := fastFwFireLast[sym]; ok && now.Sub(last) < 600*time.Second {
		fastFwFireMu.Unlock()
		return
	}
	fastFwFireLast[sym] = now
	if len(fastFwFireLast) > 4096 {
		for k, v := range fastFwFireLast {
			if now.Sub(v) > 2*time.Hour {
				delete(fastFwFireLast, k)
			}
		}
	}
	fastFwFireMu.Unlock()
	prior := 0
	fastLedgerFwTrigMu.Lock()
	cut := now.Add(-24 * time.Hour)
	for _, t := range fastLedgerFwTrig[sym] {
		if !t.Before(cut) {
			prior++
		}
	}
	fastLedgerFwTrigMu.Unlock()
	if prior > 0 {
		prior-- // 账本触发环刚记过本次细胞,剔除自身
	}
	skip := fastFireworkPureSkip(prior, ret15, ret60, symLiq5m)
	go e.fastFireworkExec(sym, ret15, ret60, spreadBps, prior, symLiq5m, skip, now)
}

// fastFireworkExec 异步:REST 建档 → shadow 落盘 → fastLiveOpen(live 锁死=record_only_source)。
func (e *OKXDirectionalEngine) fastFireworkExec(sym string, ret15, ret60, spreadBps float64, prior, symLiq5m int, skip string, detAt time.Time) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_firework] panic recovered: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ev := e.buildFastEvent(ctx, sym, e.nowFn().UTC())
	if ev == nil {
		return
	}
	ev.CandidateSource = "firework_pure"
	ev.FastCandidateSide = "short"
	ev.SnapshotID += "|firework_pure"
	ev.EventAgeMs = e.nowFn().UTC().Sub(detAt).Milliseconds()
	ev.FastCandidateReason = fmt.Sprintf("fw_r60=%.0f_r15=%.0f_prior=%d_liq=%d", ret60, ret15, prior, symLiq5m)
	if skip == "" && ev.SpreadBps > 6 { // REST 复核点差(细胞层是标尺口径,REST 是真实盘口)
		skip = "spread_wide"
	}
	ev.GateSkip = skip
	_ = appendJSONLRecord(filepath.Join(e.recordDir, fastExecShadowFile), *ev)
	if skip != "" {
		return // 三特征闸拦下:shadow 已记(对照组),不进 live 通路
	}
	r := e.fastLiveOpen(ctx, ev)
	e.logger.Printf("[fast_firework] %s short r60=%.0f r15=%.0f prior=%d liq=%d age=%dms → %s",
		sym, ret60, ret15, prior, symLiq5m, ev.EventAgeMs, r)
}
