package main

// flatten:应急平仓(reduce-only IOC),按交易所真实持仓自动配方向/数量。
// 用法:go run ./cmd/flatten <SYMBOL> [venue1,venue2,...]
//   例:go run ./cmd/flatten NVO-USDT binance,gate
// 不带 venue 列表则扫描四所。reduce-only 保证绝不平过头/反向裸开。

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"lingxi/internal/app"
	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

const (
	secretPath = "/root/lingxi/secret.yaml"
	slip       = 0.004 // IOC 限价偏移 0.4%(okx 价格保护带 ~0.5%,留余量)
	tol        = 1e-8
)

func buildAdapter(name string) (venue.Venue, error) {
	if name == binance.VenueName {
		sec, err := app.LoadVenueSecretConfig(secretPath, name)
		if err != nil {
			return nil, err
		}
		client, err := app.NewAuthenticatedBinanceClient(sec, app.NetworkConfig{HTTPTimeout: 15 * time.Second})
		if err != nil {
			return nil, err
		}
		return binance.New(client), nil
	}
	sec, err := app.LoadVenueSecretConfig(secretPath, name)
	if err != nil {
		return nil, err
	}
	cfg := venue.Config{Name: name, APIKey: sec.APIKey, APISecret: sec.Secret, Passphrase: sec.Passphrase, ProxyURL: sec.ProxyURL}
	switch name {
	case okx.VenueName:
		return okx.New(cfg)
	case gate.VenueName:
		return gate.New(cfg)
	case bybit.VenueName:
		return bybit.New(cfg)
	}
	return nil, fmt.Errorf("未知 venue %q", name)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: go run ./cmd/flatten <SYMBOL> [venue1,venue2,...]")
		os.Exit(2)
	}
	sym := venue.Symbol(strings.ToUpper(os.Args[1]))
	venues := []string{binance.VenueName, bybit.VenueName, okx.VenueName, gate.VenueName}
	if len(os.Args) > 2 {
		venues = strings.Split(strings.ToLower(os.Args[2]), ",")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, name := range venues {
		name = strings.TrimSpace(name)
		fmt.Printf("\n========== %s %s ==========\n", strings.ToUpper(name), sym)
		v, err := buildAdapter(name)
		if err != nil {
			fmt.Printf("  适配器失败: %v\n", err)
			continue
		}
		ps, err := v.FetchPositions(ctx)
		if err != nil {
			fmt.Printf("  FetchPositions 失败: %v\n", err)
			continue
		}
		var amt float64
		for _, p := range ps {
			if p.Symbol == sym {
				amt = p.PositionAmt
				break
			}
		}
		if math.Abs(amt) < tol {
			fmt.Printf("  无持仓,跳过\n")
			continue
		}
		book, err := v.FetchBookTicker(ctx, venue.MarketPerp, sym)
		if err != nil || book.BidPrice <= 0 || book.AskPrice <= 0 {
			fmt.Printf("  盘口无效: %v\n", err)
			continue
		}
		var side venue.Side
		var price float64
		if amt > 0 { // 多 → 卖平
			side = venue.SideSell
			price = book.BidPrice * (1 - slip)
		} else { // 空 → 买平
			side = venue.SideBuy
			price = book.AskPrice * (1 + slip)
		}
		qty := math.Abs(amt)
		fmt.Printf("  当前 %s %.8f,下 reduce-only IOC %s 量=%.8f 价=%.6f\n", map[bool]string{true: "多", false: "空"}[amt > 0], qty, side, qty, price)
		res, err := v.PlaceOrder(ctx, venue.OrderRequest{
			Market: venue.MarketPerp, Symbol: sym, Side: side, Type: venue.OrderTypeIOC,
			Qty: qty, Price: price, ReduceOnly: true,
			ClientOrderID: fmt.Sprintf("flat-%d", time.Now().UnixNano()%1e10),
		})
		if err != nil {
			fmt.Printf("  下单被拒: %v\n", err)
			continue
		}
		fmt.Printf("  成交=%.8f 均价=%.6f status=%s\n", res.ExecutedQty, res.AvgPrice, res.Status)
	}
	fmt.Println("\n完成。请再跑 cmd/posq 复核真实持仓。")
}
