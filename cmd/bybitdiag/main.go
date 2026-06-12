// bybitdiag 是 Bybit 账户只读诊断工具:打印逐币抵押开关/可借状态 + 统一账户余额概览。
// 用途:负 carry(借币做空现货)开不出单时,核对币种是否开了抵押、是否可借(170037 类错误)。
// 只读,不下单、不划转。用法: ./bybitdiag [-secret secret.yaml]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"lingxi/internal/app"
	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
)

func main() {
	secretPath := flag.String("secret", "secret.yaml", "密钥配置路径")
	enableAll := flag.Bool("enable-all-collateral", false, "把平台支持但未开抵押的币全部打开(账户设置写,不动资金)")
	flag.Parse()

	sec, err := app.LoadVenueSecretConfig(*secretPath, bybit.VenueName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载密钥失败: %v\n", err)
		os.Exit(1)
	}
	adp, err := bybit.New(venue.Config{Name: bybit.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret})
	if err != nil {
		fmt.Fprintf(os.Stderr, "构造适配器失败: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	infos, err := adp.FetchCollateralInfo(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "拉抵押信息失败: %v\n", err)
		os.Exit(1)
	}
	if *enableAll {
		okCnt, failCnt := 0, 0
		for _, in := range infos {
			if in.CollateralSwitch || !in.MarginCollateral || in.Coin == "USDT" || in.Coin == "USDC" {
				continue
			}
			if err := adp.SetCollateralSwitch(ctx, in.Coin, true); err != nil {
				failCnt++
				if failCnt <= 5 {
					fmt.Fprintf(os.Stderr, "开启 %s 抵押失败: %v\n", in.Coin, err)
				}
			} else {
				okCnt++
			}
			time.Sleep(60 * time.Millisecond) // 限速保护
		}
		fmt.Printf("批量开启抵押完成: 成功 %d 个, 失败 %d 个\n\n", okCnt, failCnt)
		if infos, err = adp.FetchCollateralInfo(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "复查抵押信息失败: %v\n", err)
			os.Exit(1)
		}
	}
	bals, err := adp.FetchBalances(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "拉余额失败: %v\n", err)
		os.Exit(1)
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].Coin < infos[j].Coin })
	on, offButSupported := 0, []string{}
	fmt.Printf("%-10s %-8s %-8s %-10s %s\n", "币种", "抵押开关", "可借", "余额", "小时借率")
	for _, in := range infos {
		bal := bals.ByAsset[in.Coin]
		if !in.CollateralSwitch && in.MarginCollateral {
			offButSupported = append(offButSupported, in.Coin)
		}
		if in.CollateralSwitch {
			on++
		}
		// 只打印「开了抵押」或「有余额」的币,避免几百行刷屏。
		if !in.CollateralSwitch && bal.Free <= 0 {
			continue
		}
		sw := "OFF"
		if in.CollateralSwitch {
			sw = "ON"
		}
		borrow := "-"
		if in.Borrowable {
			borrow = "可借"
		}
		fmt.Printf("%-10s %-8s %-8s %-10.4f %s\n", in.Coin, sw, borrow, bal.Free, in.HourlyBorrowRate)
	}
	fmt.Printf("\n合计: 抵押开关已开 %d 个;平台支持但未开 %d 个", on, len(offButSupported))
	if n := len(offButSupported); n > 0 && n <= 20 {
		fmt.Printf("(%v)", offButSupported)
	}
	fmt.Println()
}
