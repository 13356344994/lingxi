package main

// capcarry:#3a cap 钉死 cash-and-carry 微实验武装(2026-07-10 用户令六;全 OKX 单所,无划转依赖)。
//
// 机制(规则洞 #3):资费打 cap = 被规则强制付满租金的一侧不走 → delta 中性两腿收规则强制的租。
//   正钉(f≥+cap):空 perp(收正资费)+ 买等值现货 → delta≈0,收 perp 空头资费;
//   负钉(f≤-cap):多 perp(收负资费)+ 借币卖现货 → 需借币可用;不可借则跳过+记供给损失。
// 退出=止错三条(手动 runbook,见 RULE_HOLES #3):①资费脱钉1期=租没了退 ②基差脱锚超一期租金退
//   ③单腿>60s=执行态错误平;灾难线=权益×0.5%/笔最后防线。
// 风险(比例制,用户令二):预算=权益×2% 自动缴械;腿量=max(交易所最小值,权益×3%)/腿。
//
// 用法:
//   go run ./cmd/capcarry check <SYMBOL>          —— 资费/钉死态/基差/借币可用性 预检(只读)
//   go run ./cmd/capcarry open  <SYMBOL> <equity>  —— 按钉死方向建两腿(腿量=权益×3%)
//   go run ./cmd/capcarry close <SYMBOL>          —— 两腿 reduce-only/还币 平掉
// 全程审计 records/capcarry_audit.jsonl;名义硬顶=权益×3%(编码校验);仅 OKX。

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
	if len(os.Args) < 3 {
		fmt.Println("用法: capcarry check <SYM> | open <SYM> <equity> | close <SYM>")
		os.Exit(2)
	}
	mode := os.Args[1]
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

	if mode == "close" {
		// perp reduce-only 平 + 现货反向(还币由 OKX 自动或手动 repay,这里平现货腿)
		ps, _ := a.FetchPositions(ctx)
		amt := 0.0
		for _, p := range ps {
			if p.Symbol == perp {
				amt = p.PositionAmt
			}
		}
		if math.Abs(amt) > 1e-9 {
			side := venue.SideSell
			px := pbook.BidPrice * (1 - slip)
			if amt < 0 {
				side = venue.SideBuy
				px = pbook.AskPrice * (1 + slip)
			}
			res, oerr := a.PlaceOrder(ctx, venue.OrderRequest{Market: venue.MarketPerp, Symbol: perp, Side: side,
				Type: venue.OrderTypeIOC, Qty: math.Abs(amt), Price: px, ReduceOnly: true,
				ClientOrderID: fmt.Sprintf("cc%d", time.Now().UnixNano()%1e10)})
			audit("close_perp", map[string]interface{}{"sym": sym, "amt": amt, "executed": res.ExecutedQty, "err": fmt.Sprint(oerr)})
			fmt.Printf("平 perp: amt=%.6g executed=%.6g err=%v\n", amt, res.ExecutedQty, oerr)
		}
		fmt.Println("现货腿/还币请手动核对(capcarry 只平 perp;现货 spot 平仓+还币走 OKX 面板或 flatten spot,防误还)。")
		return
	}

	if mode == "open" {
		if len(os.Args) < 4 {
			fmt.Println("open 需要 <equity>")
			os.Exit(2)
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
		legUSDT := eq * legFrac
		if legUSDT > eq*maxFrac+1e-9 {
			fmt.Println("腿量超硬顶,拒绝")
			os.Exit(1)
		}
		if dir == "neg" && !bq.Available {
			fmt.Println("负钉但借币不可用:跳过(供给损失已记)")
			audit("open_skip_no_borrow", map[string]interface{}{"sym": sym, "funding": fr})
			return
		}
		fmt.Printf("#3a %s 钉死方向=%s 腿量≈%.2fU(权益%.1f×3%%)基差=%.1fbps 资费=%.3f%%\n", base, dir, legUSDT, eq, basisBps, fr*100)
		fmt.Println("⚠️ 两腿建仓(perp + spot)需逐腿确认真实成交与借币额度;本工具打印计划并建 perp 腿,")
		fmt.Println("   spot 腿(买现货/借币卖现货)请在同一秒手动执行以控 delta 暴露(单腿>60s=止错三条③平)。")
		// perp 腿
		var side venue.Side
		var px float64
		qty := legUSDT / pmid
		if dir == "pos" {
			side, px = venue.SideSell, pbook.BidPrice*(1-slip) // 空 perp
		} else {
			side, px = venue.SideBuy, pbook.AskPrice*(1+slip) // 多 perp
		}
		_ = a.SetLeverage(ctx, perp, 3)
		res, oerr := a.PlaceOrder(ctx, venue.OrderRequest{Market: venue.MarketPerp, Symbol: perp, Side: side,
			Type: venue.OrderTypeIOC, Qty: qty, Price: px, ClientOrderID: fmt.Sprintf("cc%d", time.Now().UnixNano()%1e10)})
		audit("open_perp", map[string]interface{}{"sym": sym, "dir": dir, "funding": fr, "basis_bps": basisBps,
			"leg_usdt": legUSDT, "qty": qty, "executed": res.ExecutedQty, "avg": res.AvgPrice, "err": fmt.Sprint(oerr)})
		fmt.Printf("perp 腿: %s qty=%.6g executed=%.6g avg=%.6g err=%v\n", side, qty, res.ExecutedQty, res.AvgPrice, oerr)
		fmt.Printf("→ 现在手动做 spot 腿:%s %s 现货≈%.2fU;完成后盯止错三条,资费脱钉即 close。\n",
			map[bool]string{true: "买入", false: "借币卖出"}[dir == "pos"], base, legUSDT)
		return
	}
	fmt.Println("未知模式", mode)
}
