package main

// capcarry:#3a cap 钉死 cash-and-carry 微实验武装(2026-07-10 用户令六;全 OKX 单所,无划转依赖)。
//
// 机制(规则洞 #3):资费打 cap = 被规则强制付满租金的一侧不走 → delta 中性两腿收规则强制的租。
//   正钉(f≥+cap):空 perp(收正资费)+ 买等值现货 → delta≈0,收 perp 空头资费;
//   负钉(f≤-cap)需要借币卖现货；当前适配器未实现借还，严格 fail-closed。
// 正钉开/平仓由持久化双腿状态机执行：逐单查单确认、60s 单腿回滚、部分成交保留状态重试。
// monitor 对账真实资费并检查：①资费脱钉 ②基差逆行超一期租金 ③权益×0.5% 灾难线。
// 风险(比例制,用户令二):预算=权益×2% 自动缴械;腿量=max(交易所最小值,权益×3%)/腿。
//
// 用法:
//   go run ./cmd/capcarry check <SYMBOL>          —— 资费/钉死态/基差/借币可用性 预检(只读)
//   go run ./cmd/capcarry monitor <SYMBOL>         —— 资费实收/基差/退出条件
//   go run ./cmd/capcarry open <SYMBOL> <equity> --confirm-live
//   go run ./cmd/capcarry close <SYMBOL> --confirm-live
// 全程审计 records/capcarry_audit.jsonl + 状态 records/capcarry_state.json；
// 钱真命令必须显式确认且仍需用户逐次批准。名义硬顶=权益×3%；仅 OKX。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/app"
	"lingxi/internal/venue"
	"lingxi/internal/venue/okx"
)

const (
	slip      = 0.004
	auditPath = "/root/lingxi/records/capcarry_audit.jsonl"
	legFrac   = 0.03 // 腿量=权益×3%(钱包限制类)
	maxFrac   = 0.03 // 硬顶=权益×3%(编码校验)
	capThresh = 0.0095
)

var statePath = "/root/lingxi/records/capcarry_state.json"

type carryState struct {
	Version       int       `json:"version"`
	Status        string    `json:"status"`
	Symbol        string    `json:"symbol"`
	Direction     string    `json:"direction"`
	PerpQty       float64   `json:"perp_qty"`
	SpotQty       float64   `json:"spot_qty"`
	LegUSDT       float64   `json:"leg_usdt"`
	EquityUSDT    float64   `json:"equity_usdt"`
	EntryFunding  float64   `json:"entry_funding"`
	EntryBasisBps float64   `json:"entry_basis_bps"`
	OpenedAt      time.Time `json:"opened_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	PerpOrderID   string    `json:"perp_order_id,omitempty"`
	SpotOrderID   string    `json:"spot_order_id,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
}

var secretPath = func() string {
	if p := strings.TrimSpace(os.Getenv("LINGXI_SECRET_PATH")); p != "" {
		return p
	}
	return "/root/lingxi/secret.yaml"
}()

func adapter() (*okx.Adapter, error) {
	sec, err := app.LoadVenueSecretConfig(secretPath, okx.VenueName)
	if err != nil {
		return nil, err
	}
	return okx.New(venue.Config{Name: okx.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret, Passphrase: sec.Passphrase, ProxyURL: sec.ProxyURL})
}

func audit(action string, d map[string]interface{}) {
	d["ts"] = time.Now().UTC().Format(time.RFC3339)
	d["action"] = action
	b, _ := json.Marshal(d)
	if f, err := os.OpenFile(auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
}

func loadState() (carryState, bool) {
	var st carryState
	b, err := os.ReadFile(statePath)
	if err != nil || json.Unmarshal(b, &st) != nil || st.Symbol == "" {
		return carryState{}, false
	}
	return st, true
}

func saveState(st carryState) error {
	st.Version = 1
	st.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath)
}

func clearState() error {
	err := os.Remove(statePath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func waitOrder(ctx context.Context, a *okx.Adapter, market venue.Market, sym venue.Symbol, clientID string) (venue.OrderResult, error) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	var last venue.OrderResult
	for {
		q, err := a.QueryOrder(ctx, market, sym, clientID)
		if err == nil {
			last = q
			switch q.Status {
			case "filled", "partially_filled", "canceled", "rejected":
				return q, nil
			}
		}
		select {
		case <-ctx.Done():
			if last.ExecutedQty > 0 {
				return last, nil
			}
			return last, ctx.Err()
		case <-t.C:
		}
	}
}

func placeIOC(ctx context.Context, a *okx.Adapter, req venue.OrderRequest) (venue.OrderResult, error) {
	ack, err := a.PlaceOrder(ctx, req)
	if err != nil {
		return venue.OrderResult{}, err
	}
	clientID := ack.ClientOrderID
	if clientID == "" {
		clientID = req.ClientOrderID
	}
	return waitOrder(ctx, a, req.Market, req.Symbol, clientID)
}

func orderID(prefix string) string {
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()%1e12)
}

// fundingNow 逐币公开端点拉当期资费(快,不全量扫描)。
func fundingNow(ctx context.Context, sym string) (float64, error) {
	var r struct {
		Data []struct {
			FundingRate string `json:"fundingRate"`
		} `json:"data"`
	}
	if err := capGetJSON(ctx, "https://www.okx.com/api/v5/public/funding-rate?instId="+sym+"-SWAP", &r); err != nil {
		return 0, err
	}
	if len(r.Data) == 0 {
		return 0, fmt.Errorf("无资费数据 %s", sym)
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(r.Data[0].FundingRate), 64)
	return f, nil
}

func capGetJSON(ctx context.Context, url string, out interface{}) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(b, out)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: capcarry status | check <SYM> | monitor <SYM> | open <SYM> <equity> --confirm-live | close <SYM> --confirm-live")
		os.Exit(2)
	}
	mode := os.Args[1]
	if mode == "status" {
		if st, ok := loadState(); ok {
			b, _ := json.MarshalIndent(st, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Println("无 capcarry 在场状态")
		}
		return
	}
	if len(os.Args) < 3 {
		fmt.Println("缺少 SYMBOL")
		os.Exit(2)
	}
	base := strings.ToUpper(strings.TrimSpace(os.Args[2]))
	sym := base
	if !strings.HasSuffix(sym, "-USDT") {
		sym += "-USDT"
	}
	a, err := adapter()
	if err != nil {
		fmt.Println("适配器失败:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	perp := venue.Symbol(sym)
	pbook, perr := a.FetchBookTicker(ctx, venue.MarketPerp, perp)
	sbook, serr := a.FetchBookTicker(ctx, venue.MarketSpot, venue.Symbol(sym))
	if perr != nil || serr != nil || pbook.BidPrice <= 0 || sbook.BidPrice <= 0 {
		fmt.Printf("盘口失败 perp=%v spot=%v\n", perr, serr)
		os.Exit(1)
	}
	pmid := (pbook.BidPrice + pbook.AskPrice) / 2
	smid := (sbook.BidPrice + sbook.AskPrice) / 2
	basisBps := (pmid/smid - 1) * 1e4
	fr, ferr := fundingNow(ctx, sym)
	if ferr != nil {
		fmt.Println("资费拉取失败:", ferr)
		os.Exit(1)
	}
	bq, _ := a.FetchBorrowQuote(ctx, base)
	pinned := math.Abs(fr) >= capThresh
	dir := ""
	if pinned && fr >= capThresh {
		dir = "pos" // 正钉:空 perp + 买现货
	} else if pinned && fr <= -capThresh {
		dir = "neg" // 负钉:多 perp + 借币卖现货
	}

	if mode == "check" {
		fmt.Printf("%s 资费=%.4f%% 钉死=%v 方向=%s perp基差vs现货=%.1fbps\n", base, fr*100, pinned, dir, basisBps)
		fmt.Printf("  借币: available=%v maxBorrowable=%.4g hourlyRate=%.5f%%\n", bq.Available, bq.MaxBorrowable, bq.HourlyRate*100)
		if dir == "neg" && !bq.Available {
			fmt.Println("  → 负钉但借币不可用:跳过(供给损失,记入报告)")
			audit("check_neg_no_borrow", map[string]interface{}{"sym": sym, "funding": fr, "basis_bps": basisBps})
		}
		return
	}

	if mode == "monitor" {
		st, ok := loadState()
		if !ok || st.Symbol != sym {
			fmt.Println("无该标的 capcarry 状态")
			return
		}
		incomes, ierr := a.FetchRealizedFunding(ctx, perp, st.OpenedAt)
		fundingUSDT := 0.0
		for _, inc := range incomes {
			fundingUSDT += inc.Income
		}
		basisMove := basisBps - st.EntryBasisBps
		approxPnL := fundingUSDT - basisMove*st.LegUSDT/1e4
		exitReason := ""
		if math.Abs(fr) < capThresh {
			exitReason = "funding_unpinned"
		} else if basisMove >= math.Abs(fr)*1e4 {
			exitReason = "basis_adverse_exceeds_one_funding"
		} else if approxPnL <= -st.EquityUSDT*0.005 {
			exitReason = "disaster_budget"
		}
		fmt.Printf("%s status=%s funding=%.4f%% basis=%.1fbps move=%+.1fbps realizedFunding=%+.6fU approxPnL=%+.6fU\n",
			sym, st.Status, fr*100, basisBps, basisMove, fundingUSDT, approxPnL)
		if ierr != nil {
			fmt.Printf("  资费实收对账失败: %v\n", ierr)
		}
		if exitReason != "" {
			fmt.Printf("  → EXIT_REQUIRED: %s（执行 close 仍须 --confirm-live）\n", exitReason)
		}
		audit("monitor", map[string]interface{}{"sym": sym, "funding": fr, "basis_bps": basisBps,
			"basis_move_bps": basisMove, "funding_realized": fundingUSDT, "approx_pnl": approxPnL, "exit_reason": exitReason})
		return
	}

	if mode == "close" {
		if os.Args[len(os.Args)-1] != "--confirm-live" {
			fmt.Println("拒绝真钱平仓：须追加 --confirm-live（仍需用户本轮明确批准）")
			os.Exit(2)
		}
		st, ok := loadState()
		if !ok || st.Symbol != sym {
			fmt.Println("无该标的 capcarry 状态，拒绝盲平")
			os.Exit(1)
		}
		st.Status = "closing"
		closedPerp := 0.0
		if st.PerpQty > 1e-9 {
			cid := orderID("cccp")
			cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
			res, oerr := placeIOC(cctx, a, venue.OrderRequest{Market: venue.MarketPerp, Symbol: perp, Side: venue.SideBuy,
				Type: venue.OrderTypeIOC, Qty: st.PerpQty, Price: pbook.AskPrice * (1 + slip), ReduceOnly: true, ClientOrderID: cid})
			ccancel()
			if oerr != nil || res.ExecutedQty <= 1e-9 {
				st.LastError = fmt.Sprintf("close perp: %v", oerr)
				_ = saveState(st)
				audit("close_perp_failed", map[string]interface{}{"sym": sym, "err": st.LastError})
				fmt.Println("perp 腿未确认成交，保留状态且不动现货:", st.LastError)
				return
			}
			closedPerp = math.Min(st.PerpQty, res.ExecutedQty)
			st.PerpQty -= closedPerp
			if st.PerpQty < 1e-9 {
				st.PerpQty = 0
			}
			audit("close_perp", map[string]interface{}{"sym": sym, "executed": closedPerp, "avg": res.AvgPrice})
		}
		spotToSell := math.Min(st.SpotQty, closedPerp)
		if st.PerpQty == 0 && closedPerp == 0 {
			spotToSell = st.SpotQty // 上轮 perp 已平、只剩现货回滚
		}
		if spotToSell > 1e-9 {
			cid := orderID("cccs")
			cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
			res, serr := placeIOC(cctx, a, venue.OrderRequest{Market: venue.MarketSpot, Symbol: venue.Symbol(sym), Side: venue.SideSell,
				Type: venue.OrderTypeIOC, Qty: spotToSell, Price: sbook.BidPrice * (1 - slip), ClientOrderID: cid})
			ccancel()
			if serr != nil || res.ExecutedQty <= 1e-9 {
				st.LastError = fmt.Sprintf("close spot: %v", serr)
				_ = saveState(st)
				audit("close_spot_failed", map[string]interface{}{"sym": sym, "err": st.LastError})
				fmt.Println("perp 已减但现货未平；保留状态，需再次 close:", st.LastError)
				return
			}
			st.SpotQty -= math.Min(st.SpotQty, res.ExecutedQty)
			if st.SpotQty < 1e-9 {
				st.SpotQty = 0
			}
			audit("close_spot", map[string]interface{}{"sym": sym, "executed": res.ExecutedQty, "avg": res.AvgPrice})
		}
		if st.PerpQty == 0 && st.SpotQty == 0 {
			_ = clearState()
			audit("closed", map[string]interface{}{"sym": sym})
			fmt.Println("双腿均已确认平仓，状态清除")
		} else {
			st.LastError = ""
			_ = saveState(st)
			fmt.Printf("部分平仓已持久化：perp=%.8f spot=%.8f；再次 close 收敛\n", st.PerpQty, st.SpotQty)
		}
		return
	}

	if mode == "open" {
		if len(os.Args) < 5 || os.Args[len(os.Args)-1] != "--confirm-live" {
			fmt.Println("open 需要 <equity> --confirm-live；真钱开火仍须用户本轮明确批准")
			os.Exit(2)
		}
		if st, exists := loadState(); exists {
			fmt.Printf("已有在场状态 %s %s，拒绝重复开仓\n", st.Symbol, st.Status)
			os.Exit(1)
		}
		var eq float64
		fmt.Sscanf(os.Args[3], "%f", &eq)
		if eq <= 0 {
			fmt.Println("equity 必须 >0")
			os.Exit(2)
		}
		if !pinned {
			fmt.Printf("%s 未钉死(f=%.4f%%),不建仓\n", base, fr*100)
			return
		}
		if dir != "pos" {
			fmt.Println("负钉需要借币卖出现货，但 OKX 适配器 BorrowRepay 尚未实现；fail-closed 跳过，绝不伪装 delta 中性")
			audit("open_skip_negative_unsupported", map[string]interface{}{"sym": sym, "funding": fr})
			return
		}
		legUSDT := eq * legFrac
		if legUSDT > eq*maxFrac+1e-9 {
			fmt.Println("腿量超硬顶,拒绝")
			os.Exit(1)
		}
		bal, berr := a.FetchBalances(ctx)
		usdt := bal.ByAsset["USDT"]
		if berr != nil || usdt.Free < legUSDT*1.5 {
			fmt.Printf("余额预检失败: free=%.4f need>=%.4f err=%v\n", usdt.Free, legUSDT*1.5, berr)
			return
		}
		fmt.Printf("#3a %s 钉死方向=%s 腿量≈%.2fU(权益%.1f×3%%)基差=%.1fbps 资费=%.3f%%\n", base, dir, legUSDT, eq, basisBps, fr*100)
		qty := legUSDT / smid
		started := time.Now().UTC()
		spotID := orderID("ccos")
		sctx, scancel := context.WithTimeout(ctx, 20*time.Second)
		spotRes, spotErr := placeIOC(sctx, a, venue.OrderRequest{Market: venue.MarketSpot, Symbol: venue.Symbol(sym), Side: venue.SideBuy,
			Type: venue.OrderTypeIOC, Qty: qty, Price: sbook.AskPrice * (1 + slip), ClientOrderID: spotID})
		scancel()
		if spotErr != nil || spotRes.ExecutedQty <= 1e-9 {
			audit("open_spot_failed", map[string]interface{}{"sym": sym, "err": fmt.Sprint(spotErr)})
			fmt.Println("现货首腿未确认成交，不开 perp:", spotErr)
			return
		}
		_ = a.SetLeverage(ctx, perp, 3)
		perpID := orderID("ccop")
		pctx, pcancel := context.WithTimeout(ctx, time.Until(started.Add(60*time.Second)))
		perpRes, perpErr := placeIOC(pctx, a, venue.OrderRequest{Market: venue.MarketPerp, Symbol: perp, Side: venue.SideSell,
			Type: venue.OrderTypeIOC, Qty: spotRes.ExecutedQty, Price: pbook.BidPrice * (1 - slip), ClientOrderID: perpID})
		pcancel()
		hedged := math.Min(spotRes.ExecutedQty, perpRes.ExecutedQty)
		extraSpot := spotRes.ExecutedQty - hedged
		if extraSpot > 1e-9 {
			rid := orderID("ccrb")
			rctx, rcancel := context.WithTimeout(context.Background(), 20*time.Second)
			rollback, rerr := placeIOC(rctx, a, venue.OrderRequest{Market: venue.MarketSpot, Symbol: venue.Symbol(sym), Side: venue.SideSell,
				Type: venue.OrderTypeIOC, Qty: extraSpot, Price: sbook.BidPrice * (1 - slip), ClientOrderID: rid})
			rcancel()
			extraSpot -= math.Min(extraSpot, rollback.ExecutedQty)
			if rerr != nil {
				audit("rollback_spot_failed", map[string]interface{}{"sym": sym, "qty": extraSpot, "err": fmt.Sprint(rerr)})
			}
		}
		if hedged <= 1e-9 {
			if extraSpot > 1e-9 {
				_ = saveState(carryState{Status: "rollback_required", Symbol: sym, Direction: dir, SpotQty: extraSpot,
					LegUSDT: legUSDT, EquityUSDT: eq, EntryFunding: fr, EntryBasisBps: basisBps, OpenedAt: started, SpotOrderID: spotID,
					LastError: fmt.Sprintf("perp=%v; spot rollback incomplete", perpErr)})
			}
			fmt.Println("perp 腿未成交，现货已回滚或状态已留存；未建立套利仓")
			return
		}
		st := carryState{Status: "active", Symbol: sym, Direction: dir, PerpQty: hedged, SpotQty: hedged + extraSpot,
			LegUSDT: hedged * smid, EquityUSDT: eq, EntryFunding: fr, EntryBasisBps: basisBps, OpenedAt: started,
			PerpOrderID: perpID, SpotOrderID: spotID}
		if extraSpot > 1e-9 {
			st.Status = "rollback_required"
			st.LastError = "unhedged spot remains"
		}
		if err := saveState(st); err != nil {
			fmt.Println("CRITICAL: 双腿已成交但状态落盘失败:", err)
			os.Exit(1)
		}
		audit("opened_pair", map[string]interface{}{"sym": sym, "funding": fr, "basis_bps": basisBps,
			"qty": hedged, "spot_extra": extraSpot, "perp_avg": perpRes.AvgPrice, "spot_avg": spotRes.AvgPrice})
		fmt.Printf("双腿已确认并持久化: spot=%.8f perp(short)=%.8f elapsed=%s status=%s\n",
			st.SpotQty, st.PerpQty, time.Since(started).Round(time.Millisecond), st.Status)
		return
	}
	fmt.Println("未知模式", mode)
}
