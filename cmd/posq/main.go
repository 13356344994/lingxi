package main

// posq:只读持仓查询(一次性诊断工具)。
// 用法:go run ./cmd/posq [symbolFilter]   例:go run ./cmd/posq NVO
// 构造四所适配器,FetchPositions,打印所有非零持仓(可选按符号过滤)。绝不下单。

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

// secretPath 支持 LINGXI_SECRET_PATH 覆盖(与主程序同名环境变量),默认沿用生产部署路径。
var secretPath = func() string {
	if p := strings.TrimSpace(os.Getenv("LINGXI_SECRET_PATH")); p != "" {
		return p
	}
	return "/root/lingxi/secret.yaml"
}()

func buildAdapter(name string) (venue.Venue, error) {
	if name == binance.VenueName {
		sec, err := app.LoadVenueSecretConfig(secretPath, name)
		if err != nil {
			return nil, fmt.Errorf("凭证: %w", err)
		}
		client, err := app.NewAuthenticatedBinanceClient(sec, app.NetworkConfig{HTTPTimeout: 15 * time.Second})
		if err != nil {
			return nil, err
		}
		return binance.New(client), nil
	}
	sec, err := app.LoadVenueSecretConfig(secretPath, name)
	if err != nil {
		return nil, fmt.Errorf("凭证: %w", err)
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
	filter := ""
	if len(os.Args) > 1 {
		filter = strings.ToUpper(os.Args[1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	instMode := os.Getenv("POSQ_INST") != ""
	for _, name := range []string{binance.VenueName, bybit.VenueName, okx.VenueName, gate.VenueName} {
		fmt.Printf("\n========== %s ==========\n", strings.ToUpper(name))
		v, err := buildAdapter(name)
		if err != nil {
			fmt.Printf("  适配器构造失败: %v\n", err)
			continue
		}
		if instMode {
			instrs, ierr := v.FetchInstruments(ctx)
			if ierr != nil {
				fmt.Printf("  FetchInstruments 失败: %v\n", ierr)
			} else {
				for _, it := range instrs {
					if it.Market == venue.MarketPerp && (filter == "" || strings.Contains(strings.ToUpper(string(it.Symbol)), filter)) {
						fmt.Printf("  [INST] %-14s tick=%v step=%v ctSize=%v minNotional=%v\n", it.Symbol, it.TickSize, it.StepSize, it.ContractSize, it.MinNotional)
					}
				}
			}
			continue
		}
		ps, err := v.FetchPositions(ctx)
		if err != nil {
			fmt.Printf("  FetchPositions 失败: %v\n", err)
			continue
		}
		found := 0
		for _, p := range ps {
			if math.Abs(p.PositionAmt) < 1e-12 {
				continue
			}
			if filter != "" && !strings.Contains(strings.ToUpper(string(p.Symbol)), filter) {
				continue
			}
			found++
			side := "多"
			if p.PositionAmt < 0 {
				side = "空"
			}
			fmt.Printf("  %-14s %s 币数=%.8f 开仓价=%.6f 标记价=%.6f uPnL=%.4f 杠杆=%dx\n",
				p.Symbol, side, math.Abs(p.PositionAmt), p.EntryPrice, p.MarkPrice, p.UnrealizedPnL, p.Leverage)
		}
		if found == 0 {
			fmt.Printf("  无匹配持仓\n")
		}
	}
}
