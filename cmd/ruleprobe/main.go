package main

// ruleprobe:规则洞微实验通用小枪(2026-07-10 枪炮线;RULE_HOLES #1/#3/#5/带沿 的执行工具)。
// 安全:名义硬顶 10U(编码写死)/只 OKX 与 Bybit/全程审计留痕 records/rule_probe_audit.jsonl。
// 用法:
//   go run ./cmd/ruleprobe open  <venue> <SYMBOL> <long|short> <notionalUSDT> [maker]
//   go run ./cmd/ruleprobe close <venue> <SYMBOL>            —— reduce-only IOC 全平
//   go run ./cmd/ruleprobe show  <venue> <SYMBOL>            —— 持仓/盘口/规格
// open 默认 taker(IOC 限价 ±0.4% 保护);带 maker 参数=post-only 挂 best 不追价。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"lingxi/internal/app"
	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

const (
	maxNotional = 10.0 // 硬顶:微实验永远 ≤10U(改此数=过 review)
	slip        = 0.004
	auditPath   = "/root/lingxi/records/rule_probe_audit.jsonl"
)

var secretPath = func() string {
	if p := strings.TrimSpace(os.Getenv("LINGXI_SECRET_PATH")); p != "" {
		return p
	}
	return "/root/lingxi/secret.yaml"
}()

func buildAdapter(name string) (venue.Venue, error) {
	sec, err := app.LoadVenueSecretConfig(secretPath, name)
	if err != nil {
		return nil, err
	}
	cfg := venue.Config{Name: name, APIKey: sec.APIKey, APISecret: sec.Secret, Passphrase: sec.Passphrase, ProxyURL: sec.ProxyURL}
	switch name {
	case okx.VenueName:
		return okx.New(cfg)
	case bybit.VenueName:
		return bybit.New(cfg)
	}
	return nil, fmt.Errorf("ruleprobe 只支持 okx/bybit,得到 %q", name)
}

func audit(action string, detail map[string]interface{}) {
	detail["ts"] = time.Now().UTC().Format(time.RFC3339)
	detail["action"] = action
	b, _ := json.Marshal(detail)
	f, err := os.OpenFile(auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
}

func findInstr(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.Instrument, bool) {
	list, err := v.FetchInstruments(ctx)
	if err != nil {
		return venue.Instrument{}, false
	}
	for _, in := range list {
		if in.Market == venue.MarketPerp && in.Symbol == sym {
			return in, true
		}
	}
	return venue.Instrument{}, false
}

// quantize 名义→币量→张步进对齐(张本位面值折算);返回 (下单量, 实际名义)。
func quantize(instr venue.Instrument, notional, px float64) (float64, float64) {
	if px <= 0 {
		return 0, 0
	}
	qty := notional / px
	step := instr.StepSize
	if instr.ContractSize > 0 && instr.QtyUnit == "contract" {
		// 张本位:qty(币)→张,取整步进,再折回币量口径由适配器处理?
		// 本工具统一以"币量"下单前先按张步进×面值对齐(与 minVenueTradableQty 同思路)。
		unit := step * instr.ContractSize
		if unit <= 0 {
			unit = instr.ContractSize
		}
		n := math.Floor(qty/unit + 1e-9)
		if n < 1 {
			n = 1 // 微实验:不足一张就买最小一张(票价即实验成本,≤硬顶校验在外层)
		}
		qty = n * unit
	} else if step > 0 {
		qty = math.Floor(qty/step+1e-9) * step
		if qty < step {
			qty = step
		}
	}
	return qty, qty * px
}

func main() {
	if len(os.Args) < 4 {
		fmt.Println("用法: ruleprobe open <venue> <SYMBOL> <long|short> <notional> [maker] | close <venue> <SYMBOL> | show <venue> <SYMBOL>")
		os.Exit(2)
	}
	mode := os.Args[1]
	vname := strings.ToLower(os.Args[2])
	sym := venue.Symbol(strings.ToUpper(os.Args[3]))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	v, err := buildAdapter(vname)
	if err != nil {
		fmt.Println("适配器失败:", err)
		os.Exit(1)
	}
	book, err := v.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil || book.BidPrice <= 0 {
		fmt.Println("盘口失败:", err)
		os.Exit(1)
	}
	mid := (book.BidPrice + book.AskPrice) / 2
	instr, ok := findInstr(ctx, v, sym)
	if !ok {
		fmt.Println("instrument 未找到")
		os.Exit(1)
	}

	switch mode {
	case "show":
		ps, _ := v.FetchPositions(ctx)
		amt := 0.0
		for _, p := range ps {
			if p.Symbol == sym {
				amt = p.PositionAmt
			}
		}
		fmt.Printf("%s %s mid=%.8g spread=%.2fbps step=%g ctVal=%g minNtl=%g 持仓=%.8g(名义%.2fU)\n",
			vname, sym, mid, (book.AskPrice-book.BidPrice)/mid*1e4, instr.StepSize, instr.ContractSize, instr.MinNotional, amt, math.Abs(amt)*mid)
		return

	case "open":
		if len(os.Args) < 6 {
			fmt.Println("open 需要 <long|short> <notional>")
			os.Exit(2)
		}
		dir := strings.ToLower(os.Args[4])
		var notional float64
		fmt.Sscanf(os.Args[5], "%f", &notional)
		if notional <= 0 || notional > maxNotional {
			fmt.Printf("名义须 ∈(0,%.0f]U(硬顶)\n", maxNotional)
			os.Exit(2)
		}
		maker := len(os.Args) > 6 && strings.EqualFold(os.Args[6], "maker")
		qty, realNtl := quantize(instr, notional, mid)
		if realNtl > 2*maxNotional {
			fmt.Printf("最小票 %.2fU 超硬顶两倍,拒绝\n", realNtl)
			os.Exit(1)
		}
		side := venue.SideBuy
		px := book.AskPrice * (1 + slip)
		otype := venue.OrderTypeIOC
		if dir == "short" {
			side = venue.SideSell
			px = book.BidPrice * (1 - slip)
		}
		if maker {
			otype = venue.OrderTypePostOnly
			if side == venue.SideBuy {
				px = book.BidPrice
			} else {
				px = book.AskPrice
			}
		}
		_ = v.SetLeverage(ctx, sym, 3)
		fmt.Printf("下单 %s %s %s qty=%.8g(≈%.2fU) px=%.8g type=%v\n", vname, sym, dir, qty, realNtl, px, otype)
		res, oerr := v.PlaceOrder(ctx, venue.OrderRequest{Market: venue.MarketPerp, Symbol: sym, Side: side,
			Type: otype, Qty: qty, Price: px, ClientOrderID: fmt.Sprintf("rp%d", time.Now().UnixNano()%1e10)})
		audit("open", map[string]interface{}{"venue": vname, "symbol": string(sym), "dir": dir, "qty": qty,
			"notional": realNtl, "px": px, "maker": maker, "executed": res.ExecutedQty, "avg": res.AvgPrice,
			"status": res.Status, "err": fmt.Sprint(oerr)})
		fmt.Printf("结果: executed=%.8g avg=%.8g status=%s err=%v\n", res.ExecutedQty, res.AvgPrice, res.Status, oerr)
		return

	case "close":
		ps, perr := v.FetchPositions(ctx)
		if perr != nil {
			fmt.Println("FetchPositions 失败:", perr)
			os.Exit(1)
		}
		amt := 0.0
		for _, p := range ps {
			if p.Symbol == sym {
				amt = p.PositionAmt
			}
		}
		if math.Abs(amt)*mid < 0.01 {
			fmt.Println("无持仓")
			return
		}
		side := venue.SideSell
		px := book.BidPrice * (1 - slip)
		if amt < 0 {
			side = venue.SideBuy
			px = book.AskPrice * (1 + slip)
		}
		res, oerr := v.PlaceOrder(ctx, venue.OrderRequest{Market: venue.MarketPerp, Symbol: sym, Side: side,
			Type: venue.OrderTypeIOC, Qty: math.Abs(amt), Price: px, ReduceOnly: true,
			ClientOrderID: fmt.Sprintf("rpc%d", time.Now().UnixNano()%1e10)})
		audit("close", map[string]interface{}{"venue": vname, "symbol": string(sym), "amt": amt,
			"executed": res.ExecutedQty, "avg": res.AvgPrice, "status": res.Status, "err": fmt.Sprint(oerr)})
		fmt.Printf("平仓: executed=%.8g avg=%.8g status=%s err=%v\n", res.ExecutedQty, res.AvgPrice, res.Status, oerr)
		return
	}
	fmt.Println("未知模式", mode)
}
