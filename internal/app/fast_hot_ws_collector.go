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

// ===== v6.1 暴动登记(怪兽坑位优先的依据;2026-07-05)=====
// census 口径(|60s|≥25bps,120s 同币去重)的暴动时刻,30min 滚动窗。
// 由全市场标尺循环喂;热币 WS 差分订阅按它识别"正在连环暴动的币"。

var (
	fastRiotMu   sync.Mutex
	fastRiotHist = map[string][]time.Time{}
)

// fastNoteRiot 普查发射点调用:登记一次暴动(窗口裁剪就地做,防无限涨)。
func fastNoteRiot(sym string, now time.Time) {
	fastRiotMu.Lock()
	defer fastRiotMu.Unlock()
	cut := now.Add(-30 * time.Minute)
	h := append(fastRiotHist[sym], now)
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	fastRiotHist[sym] = h[i:]
	if len(fastRiotHist) > 4096 {
		for k, v := range fastRiotHist {
			if len(v) == 0 || v[len(v)-1].Before(cut) {
				delete(fastRiotHist, k)
			}
		}
	}
}

// fastRiotCounts30m 各币 30min 暴动数快照(怪兽优先驱逐的输入)。
func fastRiotCounts30m(now time.Time) map[string]int {
	cut := now.Add(-30 * time.Minute)
	out := map[string]int{}
	fastRiotMu.Lock()
	defer fastRiotMu.Unlock()
	for sym, h := range fastRiotHist {
		n := 0
		for _, t := range h {
			if !t.Before(cut) {
				n++
			}
		}
		if n > 0 {
			out[sym] = n
		}
	}
	return out
}

// fastHotWSDesired 纯函数:从热度表算出目标订阅集与退订集。
// 规则:①已订阅且 stickySec 内仍热 → 保留;②空位按"最近热"优先补新币;③超期未热 → 退订;
// ④【v6.1 怪兽优先,riotN!=nil 时生效】满员后,30min 暴动数≥5 的新鲜候选可驱逐暴动数少
//   ≥3 的在订者(迟滞防订阅抖动),每轮至多 5 对——07-05 漏斗诊断:H-USDT 139 次暴动 0 秒覆盖,
//   坑位被粘性在订者霸占;怪兽币(连环暴动=杠杆瓦解候选猎场)必须挤得进视野。
func fastHotWSDesired(hot map[string]time.Time, current map[string]bool, now time.Time, maxN, stickySec int, riotN map[string]int) (subs, unsubs []string) {
	fresh := func(sym string) bool {
		t, ok := hot[sym]
		return ok && now.Sub(t) <= time.Duration(stickySec)*time.Second
	}
	kept := make([]string, 0, len(current))
	for sym := range current {
		if fresh(sym) {
			kept = append(kept, sym)
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
	added := map[string]bool{}
	for _, c := range cands {
		if len(kept)+len(subs) >= maxN {
			break
		}
		subs = append(subs, c.sym)
		added[c.sym] = true
	}
	if riotN != nil && len(kept)+len(subs) >= maxN && len(kept) > 0 {
		rest := make([]cand, 0, len(cands))
		for _, c := range cands {
			if !added[c.sym] && riotN[c.sym] >= 5 {
				rest = append(rest, c)
			}
		}
		sort.Slice(rest, func(i, j int) bool { return riotN[rest[i].sym] > riotN[rest[j].sym] })
		sort.Slice(kept, func(i, j int) bool { return riotN[kept[i]] < riotN[kept[j]] })
		ki, evicted := 0, 0
		for _, c := range rest {
			if evicted >= 5 || ki >= len(kept) {
				break
			}
			if riotN[c.sym]-riotN[kept[ki]] < 3 {
				break // rest 降序 kept 升序:此对不满足则后续更不满足
			}
			unsubs = append(unsubs, kept[ki])
			subs = append(subs, c.sym)
			ki++
			evicted++
		}
	}
	sort.Strings(subs)
	sort.Strings(unsubs)
	return
}

// fastHotRiotCounts 引擎侧包装:优先权开关关=nil(纯函数走旧行为)。
func (e *OKXDirectionalEngine) fastHotRiotCounts(now time.Time) map[string]int {
	if !e.cfg.FastHotPriorityEnabled {
		return nil
	}
	return fastRiotCounts30m(now)
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
	e.logger.Printf("[fast_hotws] 热币微结构采集启动(record-only)动态订阅上限=%d 粘性=%ds 怪兽优先=%v → %s", maxN, fastHotWSStickySec, e.cfg.FastHotPriorityEnabled, fastHotMicroFile)
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

// fastHotWSSubOp 发送订阅/退订 op(books5+trades+open-interest 三频道;
// OI=去杠杆总量直读,v5.4"瀑布还剩多少燃料"的原料,爆仓频道节流漏报而 OI 不会)。
func fastHotWSSubOp(conn *websocket.Conn, op string, syms []string) error {
	if len(syms) == 0 {
		return nil
	}
	args := make([]map[string]string, 0, len(syms)*3)
	for _, s := range syms {
		inst := s + "-SWAP"
		args = append(args, map[string]string{"channel": "trades", "instId": inst})
		args = append(args, map[string]string{"channel": "books5", "instId": inst})
		args = append(args, map[string]string{"channel": "open-interest", "instId": inst})
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
	st := &fastWSState{trades: map[string][]wsTradePt{}, books: map[string][]wsBookPt{},
		burst: newFastBurstDetector(e.recordDir)} // v5.9 亚秒 burst 探测(仅热币 WS,record-only)
	subbed := map[string]bool{}
	var subMu sync.Mutex
	// 初始订阅
	now := e.nowFn().UTC()
	subs, _ := fastHotWSDesired(fastHotSetSnapshot(), subbed, now, maxN, fastHotWSStickySec, e.fastHotRiotCounts(now))
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
				add, del := fastHotWSDesired(fastHotSetSnapshot(), cur, nowD, maxN, fastHotWSStickySec, e.fastHotRiotCounts(nowD))
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
