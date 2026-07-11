package replay

import (
	"bufio"
	"encoding/json"
	"io"
	"time"
)

// 本文件提供与现有 JSONL 记录(settlements.jsonl / fast_maker_attempts.jsonl)
// 字段对齐的 schema 与流式 loader。loader 只依赖 io.Reader:调用方自行决定
// 打开哪个文件、读多少(生产走文件流,单测用内存小样本,不读巨量文件)。

// SettlementRecord 对齐 settlements.jsonl 的回放子集(字段名与 app 写盘端一致;
// 未列出的字段解码时被忽略,向后兼容扩展)。
type SettlementRecord struct {
	Time         time.Time `json:"time"`
	Symbol       string    `json:"symbol"`
	Side         string    `json:"side"`
	Entry        float64   `json:"entry"`
	Exit         float64   `json:"exit"`
	NotionalUSDT float64   `json:"notional"`
	RealizedUSDT float64   `json:"realized_usdt"`
	HoldSeconds  int       `json:"hold_seconds"`
	HoldMinutes  int       `json:"hold_minutes"`
	FeeUSDT      float64   `json:"fee_usdt"`
	EntrySlipBps float64   `json:"entry_slip_bps"`
	ExitSlipBps  float64   `json:"exit_slip_bps"`
	OpenViaTaker bool      `json:"open_via_taker"`
	FastEventID  string    `json:"fast_event_id"`
	Regime       string    `json:"regime"`
	Reason       string    `json:"reason"`
}

// Trade 把结算行映射为回放 Trade:
//   - realized_usdt 作为毛 PnL 真相覆盖(GrossPnLUSDT),fee_usdt 单独入 fee;
//     若写盘端 realized 已含费,调用方应把 FeeUSDT 清零后再回放,避免双扣;
//   - 入场/出场 mid 由成交价与滑点 bps 反推(slip 正=更差),供滑点归因;
//   - fast_event_id 作为 EpisodeID(同事件多笔=一个相关簇)。
func (r SettlementRecord) Trade() Trade {
	gross := r.RealizedUSDT
	openTime := r.Time
	if r.HoldSeconds > 0 {
		openTime = r.Time.Add(-time.Duration(r.HoldSeconds) * time.Second)
	} else if r.HoldMinutes > 0 {
		openTime = r.Time.Add(-time.Duration(r.HoldMinutes) * time.Minute)
	}
	sign := Side(r.Side).Sign()
	entryMid, exitMid := r.Entry, r.Exit
	if sign != 0 {
		// slip 正=更差:多头入场买贵(price>mid)、出场卖便宜(price<mid);空头反之。
		entryMid = r.Entry / (1 + sign*r.EntrySlipBps/10000)
		exitMid = r.Exit / (1 - sign*r.ExitSlipBps/10000)
	}
	style := ExecMaker
	if r.OpenViaTaker {
		style = ExecTaker
	}
	return Trade{
		OpenTime:     openTime,
		CloseTime:    r.Time,
		Symbol:       r.Symbol,
		Side:         Side(r.Side),
		Style:        style,
		EntryMid:     entryMid,
		EntryPrice:   r.Entry,
		ExitMid:      exitMid,
		ExitPrice:    r.Exit,
		NotionalUSDT: r.NotionalUSDT,
		FillRatio:    1,
		FeeUSDT:      r.FeeUSDT,
		GrossPnLUSDT: &gross,
		EpisodeID:    r.FastEventID,
	}
}

// MakerAttemptRecord 对齐 fast_maker_attempts.jsonl(成交/未成交都记)。
type MakerAttemptRecord struct {
	RecordedAt       time.Time `json:"recorded_at"`
	Venue            string    `json:"venue"`
	Symbol           string    `json:"symbol"`
	Side             string    `json:"side"`
	Score            float64   `json:"score"`
	MakerOrderPrice  float64   `json:"maker_order_price"`
	MakerWaitMs      int       `json:"maker_wait_ms"`
	MakerFilled      bool      `json:"maker_filled"`
	FillPrice        float64   `json:"fill_price"`
	FillMid          float64   `json:"fill_mid"`
	MakerEdgeBps     float64   `json:"maker_edge_bps"`
	SpreadBps        float64   `json:"spread_bps"`
	EntryConfirmPath string    `json:"entry_confirm_path"`
}

// Trade 把 maker 尝试映射为回放 Trade 的入场半边:未成交→FillRatio=0
// (回放为未成交反事实),成交→FillRatio=1。出场侧(ExitMid/ExitPrice/CloseTime)
// 由调用方用后续行情或结算行补齐后才能得到完整 PnL。
func (r MakerAttemptRecord) Trade(notionalUSDT float64) Trade {
	fillRatio := 0.0
	entryPrice := r.MakerOrderPrice
	if r.MakerFilled {
		fillRatio = 1
		if r.FillPrice > 0 {
			entryPrice = r.FillPrice
		}
	}
	return Trade{
		OpenTime:     r.RecordedAt,
		Venue:        r.Venue,
		Symbol:       r.Symbol,
		Side:         Side(r.Side),
		Style:        ExecMaker,
		EntryMid:     r.FillMid,
		EntryPrice:   entryPrice,
		NotionalUSDT: notionalUSDT,
		FillRatio:    fillRatio,
	}
}

// DecodeStats 一次流式解码的统计。
type DecodeStats struct {
	Lines     int // 非空行数
	Decoded   int // 成功解码并被接收的行数
	Malformed int // JSON 解码失败被跳过的行数
	Truncated bool
}

// DecodeJSONL 从 r 流式逐行解码 JSONL:每行调用 accept,accept 返回 false 则
// 提前停止(Truncated=true)。maxLines>0 时最多解码 maxLines 行(同样置 Truncated),
// 防止误把巨量文件整读进内存。坏行跳过并计数,不中断整体回放。
func DecodeJSONL[T any](r io.Reader, maxLines int, accept func(T) bool) (DecodeStats, error) {
	var stats DecodeStats
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		stats.Lines++
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			stats.Malformed++
			continue
		}
		stats.Decoded++
		if !accept(v) {
			stats.Truncated = true
			return stats, nil
		}
		if maxLines > 0 && stats.Decoded >= maxLines {
			stats.Truncated = true
			return stats, nil
		}
	}
	return stats, sc.Err()
}

// LoadSettlementTrades 便捷封装:从 JSONL 流解码结算行并映射为 Trade。
func LoadSettlementTrades(r io.Reader, maxLines int) ([]Trade, DecodeStats, error) {
	var trades []Trade
	stats, err := DecodeJSONL(r, maxLines, func(rec SettlementRecord) bool {
		trades = append(trades, rec.Trade())
		return true
	})
	return trades, stats, err
}
