package main

// livecheck:OKX/Gate 微额实弹自检(一次性诊断工具)。
//
// OFF 模式只能验证决策链,验证不了「交易所收不收单」:账户模式没切、key 权限没勾、
// 精度/最小额算错、IOC 语义差异——这些只有真实订单能炸出来(Bybit 170207 的教训)。
// 本工具在每所做两组微额往返:
//   1) 永续:IOC 限价买开 ~minNotional 名义 → 立即 reduce-only IOC 卖平
//   2) 现货:IOC 限价买 ~5U → 立即 IOC 限价卖回实际成交量
// 全程亏损上限 = 双边手续费+滑点(几毛钱)。任一步失败立即报告并停止该所(残留腿会打印,人工核对)。
//
// 用法:go run ./cmd/livecheck [okx|gate|all]
import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"lingxi/internal/app"
	"lingxi/internal/venue"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// testSymbols 按流动性优先选测试币(必须现货+永续都有、价格低、深度好)。
var testSymbols = []venue.Symbol{"DOGE-USDT", "XRP-USDT", "ADA-USDT"}

const (
	spotNotionalUSDT = 5.0   // 现货测试名义额
	slippageBudget   = 0.01  // 永续 IOC 限价偏移 1%
	spotSlippage     = 0.002 // 现货偏移 0.2%:OKX 现货有价格保护带(51137),超带直接拒单
)

func main() {
	target := "all"
	if len(os.Args) > 1 {
		target = strings.ToLower(os.Args[1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	failures := 0
	for _, name := range []string{"okx", "gate"} {
		if target != "all" && target != name {
			continue
		}
		fmt.Printf("\n========== %s 实弹自检 ==========\n", strings.ToUpper(name))
		if err := checkVenue(ctx, name); err != nil {
			failures++
			fmt.Printf("[%s] ❌ 自检失败: %v\n", name, err)
		} else {
			fmt.Printf("[%s] ✅ 全部通过(永续开平 + 现货买卖)\n", name)
		}
	}
	if failures > 0 {
		os.Exit(1)
	}
}

func buildAdapter(name string) (venue.Venue, error) {
	secretPath := strings.TrimSpace(os.Getenv("LINGXI_SECRET_PATH"))
	if secretPath == "" {
		secretPath = "/root/lingxi/secret.yaml"
	}
	sec, err := app.LoadVenueSecretConfig(secretPath, name)
	if err != nil {
		return nil, fmt.Errorf("凭证: %w", err)
	}
	cfg := venue.Config{Name: name, APIKey: sec.APIKey, APISecret: sec.Secret, Passphrase: sec.Passphrase, ProxyURL: sec.ProxyURL}
	if name == "okx" {
		return okx.New(cfg)
	}
	return gate.New(cfg)
}

// pickInstrument 在该所选一个现货+永续都存在的测试币。
func pickInstrument(ctx context.Context, v venue.Venue) (venue.Symbol, venue.Instrument, venue.Instrument, error) {
	all, err := v.FetchInstruments(ctx)
	if err != nil {
		return "", venue.Instrument{}, venue.Instrument{}, fmt.Errorf("拉合约表: %w", err)
	}
	perpBy := map[venue.Symbol]venue.Instrument{}
	spotBy := map[venue.Symbol]venue.Instrument{}
	for _, it := range all {
		switch it.Market {
		case venue.MarketPerp:
			perpBy[it.Symbol] = it
		case venue.MarketSpot:
			spotBy[it.Symbol] = it
		}
	}
	for _, sym := range testSymbols {
		p, okP := perpBy[sym]
		s, okS := spotBy[sym]
		if okP && okS {
			return sym, p, s, nil
		}
	}
	return "", venue.Instrument{}, venue.Instrument{}, fmt.Errorf("测试币(%v)在该所现货/永续不齐", testSymbols)
}

// roundDown 向下对齐步进。
func roundDown(v, step float64) float64 {
	if step <= 0 {
		return v
	}
	return math.Floor(v/step) * step
}

// perpQty 算最小可下的永续币数量:max(1 张, minNotional),再按步进对齐。
func perpQty(inst venue.Instrument, price float64) float64 {
	q := inst.ContractSize // 至少 1 张
	if q <= 0 {
		q = inst.StepSize
	}
	if inst.MinNotional > 0 && q*price < inst.MinNotional {
		q = inst.MinNotional / price * 1.05
	}
	if inst.ContractSize > 0 { // 按张对齐(币数量必须是整张)
		q = math.Ceil(q/inst.ContractSize) * inst.ContractSize
	} else if inst.StepSize > 0 {
		q = math.Ceil(q/inst.StepSize) * inst.StepSize
	}
	return q
}

func placeAndReport(ctx context.Context, v venue.Venue, label string, req venue.OrderRequest) (venue.OrderResult, error) {
	res, err := v.PlaceOrder(ctx, req)
	if err != nil {
		return res, fmt.Errorf("%s 下单被拒: %w", label, err)
	}
	if res.ExecutedQty <= 0 {
		// IOC 未成交:等 2s 查一次终态(部分所异步回执)
		time.Sleep(2 * time.Second)
		if q, qerr := v.QueryOrder(ctx, req.Market, req.Symbol, req.ClientOrderID); qerr == nil {
			res = q
		}
	}
	fmt.Printf("  %-22s status=%-10s 成交=%.6f 均价=%.6f\n", label, res.Status, res.ExecutedQty, res.AvgPrice)
	if res.ExecutedQty <= 0 {
		return res, fmt.Errorf("%s IOC 未成交(深度/价格问题)", label)
	}
	return res, nil
}

func checkVenue(ctx context.Context, name string) error {
	v, err := buildAdapter(name)
	if err != nil {
		return err
	}
	sym, perpInst, spotInst, err := pickInstrument(ctx, v)
	if err != nil {
		return err
	}
	fmt.Printf("  测试币=%s 永续(step=%g ctSize=%g minNotional=%g)\n", sym, perpInst.StepSize, perpInst.ContractSize, perpInst.MinNotional)

	// ---- 永续往返 ----
	if err := v.SetLeverage(ctx, sym, 1); err != nil && err != venue.ErrUnsupported {
		fmt.Printf("  [警告] SetLeverage(1) 失败(可能已设置): %v\n", err)
	}
	book, err := v.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		return fmt.Errorf("永续盘口: %w", err)
	}
	qty := perpQty(perpInst, book.AskPrice)
	fmt.Printf("  永续测试量=%.6f %s(名义≈%.2fU)\n", qty, perpInst.BaseAsset, qty*book.AskPrice)

	ts := time.Now().Unix()
	openRes, err := placeAndReport(ctx, v, "永续 IOC 买开", venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: sym, Side: venue.SideBuy, Type: venue.OrderTypeIOC,
		Qty: qty, Price: book.AskPrice * (1 + slippageBudget),
		ClientOrderID: fmt.Sprintf("lc-p-o-%d", ts),
	})
	if err != nil {
		return err
	}
	book2, err := v.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		return fmt.Errorf("永续平仓盘口: %w(⚠️ 永续多头 %.6f 未平,请人工处理)", err, openRes.ExecutedQty)
	}
	if _, err := placeAndReport(ctx, v, "永续 reduce-only 卖平", venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: sym, Side: venue.SideSell, Type: venue.OrderTypeIOC,
		Qty: openRes.ExecutedQty, Price: book2.BidPrice * (1 - slippageBudget),
		ClientOrderID: fmt.Sprintf("lc-p-c-%d", ts), ReduceOnly: true,
	}); err != nil {
		return fmt.Errorf("%w(⚠️ 永续多头 %.6f 未平,请人工处理)", err, openRes.ExecutedQty)
	}

	// ---- 现货往返 ----
	sbook, err := v.FetchBookTicker(ctx, venue.MarketSpot, sym)
	if err != nil {
		return fmt.Errorf("现货盘口: %w", err)
	}
	// 残留清扫:上一轮买入成功但回执解析失败时会留下基础币,先卖回再测,避免余额不足。
	if bals, berr := v.FetchBalances(ctx); berr == nil {
		if b, ok := bals.ByAsset[perpInst.BaseAsset]; ok && b.Free*sbook.BidPrice > 1.0 {
			fmt.Printf("  [清扫] 发现现货残留 %.6f %s,先卖回\n", b.Free, perpInst.BaseAsset)
			if _, serr := placeAndReport(ctx, v, "现货残留卖出", venue.OrderRequest{
				Market: venue.MarketSpot, Symbol: sym, Side: venue.SideSell, Type: venue.OrderTypeIOC,
				Qty: math.Floor(b.Free), Price: sbook.BidPrice * (1 - spotSlippage),
				ClientOrderID: fmt.Sprintf("lc-s-w-%d", time.Now().Unix()),
			}); serr != nil {
				return fmt.Errorf("残留清扫失败: %w", serr)
			}
		}
	}
	sqty := roundDown(spotNotionalUSDT/sbook.AskPrice, 0)
	buyRes, err := placeAndReport(ctx, v, "现货 IOC 买入", venue.OrderRequest{
		Market: venue.MarketSpot, Symbol: sym, Side: venue.SideBuy, Type: venue.OrderTypeIOC,
		Qty: sqty, Price: sbook.AskPrice * (1 + spotSlippage),
		ClientOrderID: fmt.Sprintf("lc-s-b-%d", ts),
	})
	if err != nil {
		return err
	}
	sbook2, err := v.FetchBookTicker(ctx, venue.MarketSpot, sym)
	if err != nil {
		return fmt.Errorf("现货卖出盘口: %w(⚠️ 现货 %.6f 未卖回,请人工处理)", err, buyRes.ExecutedQty)
	}
	// 卖回数量按实际余额算:OKX 等所现货买入手续费从收到的币里扣,
	// 按成交量原数卖回会「余额不足」(51008,实弹自检发现)。
	sellQty := buyRes.ExecutedQty
	if bals, berr := v.FetchBalances(ctx); berr == nil {
		if b, ok := bals.ByAsset[perpInst.BaseAsset]; ok && b.Free < sellQty {
			sellQty = b.Free
		}
	}
	step := spotInst.StepSize
	if step <= 0 {
		step = 1
	}
	sellQty = roundDown(sellQty, step)
	if _, err := placeAndReport(ctx, v, "现货 IOC 卖回", venue.OrderRequest{
		Market: venue.MarketSpot, Symbol: sym, Side: venue.SideSell, Type: venue.OrderTypeIOC,
		Qty: sellQty, Price: sbook2.BidPrice * (1 - spotSlippage),
		ClientOrderID: fmt.Sprintf("lc-s-s-%d", ts),
	}); err != nil {
		return fmt.Errorf("%w(⚠️ 现货 %.6f 未卖回,请人工处理)", err, sellQty)
	}
	return nil
}
