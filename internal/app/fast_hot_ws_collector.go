package app

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ===== v5.3 热币微结构采集(2026-07-04,record-only)=====
// 补齐研究面最后两个数据缺口:热币的【连续盘口深度】与【成交流】。
// 此前 books5+trades 只覆盖 shortlist ~15 币;瀑布过程中"深度怎么蒸发"、"成交流形状能否先于
// 官方爆仓推送半秒识别强平"(候选机制)在非核心币上完全不可见。
// 设计:第二条 OKX 公共 WS 连接,动态订阅热币(热度由全市场标尺判定,共享 fastHotSet),
// 复用 fastWSState/fastWSHandle/fastWSBuildEvent,按秒写 fast_hot_micro.jsonl。
// 不下单/不读账户/失败只告警;重开实盘后它同时是低延迟盘口缓存的地基。

const fastHotMicroFile = "fast_hot_micro.jsonl"
const fastHotWSStickySec = 600 // 退订粘性:最后一次热之后保留10min(捕捉余波,防订阅抖动)
const fastHotWSDiffIntervalSec = 10

var (
	fastHotSetMu sync.Mutex
	fastHotSet   = map[string]time.Time{} // sym -> 最后一次判定为热(全市场标尺循环维护)
)

// fastNoteHot 全市场标尺循环调用:登记热币。
func fastNoteHot(sym string, now time.Time) {
	fastHotSetMu.Lock()
	defer fastHotSetMu.Unlock()
	fastHotSet[sym] = now
	if len(fastHotSet) > 4096 {
		for k, v := range fastHotSet {
			if now.Sub(v) > 30*time.Minute {
				delete(fastHotSet, k)
			}
		}
	}
}

func fastHotSetSnapshot() map[string]time.Time {
	fastHotSetMu.Lock()
	defer fastHotSetMu.Unlock()
	out := make(map[string]time.Time, len(fastHotSet))
	for k, v := range fastHotSet {
		out[k] = v
	}
	return out
}

// fastHotWSDesired 纯函数:从热度表算出目标订阅集与退订集。
// 规则:①已订阅且 stickySec 内仍热 → 保留;②空位按"最近热"优先补新币;③超期未热 → 退订。
func fastHotWSDesired(hot map[string]time.Time, current map[string]bool, now time.Time, maxN, stickySec int) (subs, unsubs []string) {
	fresh := func(sym string) bool {
		t, ok := hot[sym]
		return ok && now.Sub(t) <= time.Duration(stickySec)*time.Second
	}
	kept := 0
	for sym := range current {
		if fresh(sym) {
			kept++
		} else {
			unsubs = append(unsubs, sym)
		}
	}
	type cand struct {
		sym string
		t   time.Time
	}
	cands := make([]cand, 0, len(hot))
	for sym, t := range hot {
		if !current[sym] && fresh(sym) {
			cands = append(cands, cand{sym, t})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].t.After(cands[j].t) })
	for _, c := range cands {
		if kept+len(subs) >= maxN {
			break
		}
		subs = append(subs, c.sym)
	}
	sort.Strings(subs)
	sort.Strings(unsubs)
	return
}

func (e *OKXDirectionalEngine) fastHotWSEnabled() bool {
	return e.cfg.FastHotWSEnabled && strings.EqualFold(e.venueName, "okx")
}

func (e *OKXDirectionalEngine) fastHotWSLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_hotws] loop panic 已恢复: %v", r)
		}
	}()
	maxN := e.cfg.FastHotWSMaxSymbols
	if maxN <= 0 {
		maxN = 40
	}
	e.logger.Printf("[fast_hotws] 热币微结构采集启动(record-only)动态订阅上限=%d 粘性=%ds → %s", maxN, fastHotWSStickySec, fastHotMicroFile)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := e.runFastHotWS(ctx, maxN); err != nil && ctx.Err() == nil {
			e.logger.Printf("[fast_hotws] 连接中断,3s 后重连: %v", err)
		}
		if !sleepCtx(ctx, 3*time.Second) {
			return
		}
	}
}

// fastHotWSSubOp 发送订阅/退订 op(books5+trades 双频道)。
func fastHotWSSubOp(conn *websocket.Conn, op string, syms []string) error {
	if len(syms) == 0 {
		return nil
	}
	args := make([]map[string]string, 0, len(syms)*2)
	for _, s := range syms {
		inst := s + "-SWAP"
		args = append(args, map[string]string{"channel": "trades", "instId": inst})
		args = append(args, map[string]string{"channel": "books5", "instId": inst})
	}
	return conn.WriteJSON(map[string]interface{}{"op": op, "args": args})
}

func (e *OKXDirectionalEngine) runFastHotWS(ctx context.Context, maxN int) error {
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).Dial(okxPublicWSURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))
	st := &fastWSState{trades: map[string][]wsTradePt{}, books: map[string][]wsBookPt{}}
	subbed := map[string]bool{}
	var subMu sync.Mutex
	// 初始订阅
	now := e.nowFn().UTC()
	subs, _ := fastHotWSDesired(fastHotSetSnapshot(), subbed, now, maxN, fastHotWSStickySec)
	if err := fastHotWSSubOp(conn, "subscribe", subs); err != nil {
		return err
	}
	for _, s := range subs {
		subbed[s] = true
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// ping + 差分订阅管理
	go func() {
		ping := time.NewTicker(20 * time.Second)
		diff := time.NewTicker(fastHotWSDiffIntervalSec * time.Second)
		defer ping.Stop()
		defer diff.Stop()
		for {
			select {
			case <-cctx.Done():
				_ = conn.Close() // 防 ReadMessage 挂死
				return
			case <-ping.C:
				_ = conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			case <-diff.C:
				nowD := e.nowFn().UTC()
				subMu.Lock()
				cur := make(map[string]bool, len(subbed))
				for k := range subbed {
					cur[k] = true
				}
				subMu.Unlock()
				add, del := fastHotWSDesired(fastHotSetSnapshot(), cur, nowD, maxN, fastHotWSStickySec)
				if len(add) == 0 && len(del) == 0 {
					continue
				}
				if err := fastHotWSSubOp(conn, "unsubscribe", del); err != nil {
					return
				}
				if err := fastHotWSSubOp(conn, "subscribe", add); err != nil {
					return
				}
				subMu.Lock()
				for _, s := range del {
					delete(subbed, s)
				}
				for _, s := range add {
					subbed[s] = true
				}
				n := len(subbed)
				subMu.Unlock()
				if len(add) > 0 || len(del) > 0 {
					e.logger.Printf("[fast_hotws] 订阅差分 +%d -%d 当前=%d", len(add), len(del), n)
				}
			}
		}
	}()
	// 按秒 emit(只发当前订阅集;复用 fastWSBuildEvent)
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		path := filepath.Join(e.recordDir, fastHotMicroFile)
		for {
			select {
			case <-cctx.Done():
				return
			case <-tk.C:
			}
			nowE := e.nowFn().UTC()
			subMu.Lock()
			syms := make([]string, 0, len(subbed))
			for s := range subbed {
				syms = append(syms, s)
			}
			subMu.Unlock()
			for _, sym := range syms {
				ev := e.fastWSBuildEvent(st, sym, nowE)
				if ev == nil {
					continue
				}
				if werr := appendJSONLRecord(path, *ev); werr != nil {
					e.logger.Printf("[fast_hotws] 落盘失败: %v", werr)
					break
				}
			}
		}
	}()
	// read loop(复用 fastWSHandle 解析 trades/books5)
	for {
		select {
		case <-cctx.Done():
			return cctx.Err()
		default:
		}
		_, payload, rerr := conn.ReadMessage()
		if rerr != nil {
			return rerr
		}
		_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		if len(payload) == 4 && string(payload) == "pong" {
			continue
		}
		e.fastWSHandle(st, payload)
	}
}
