// feeprobe 是「手续费折扣 Phase 2」的独立只读账户费率探针。
//
// 目标:把 records/fee_discount_audit.jsonl 里 Phase 1 标注的 NEED_API 项,用四所私有【只读】
// API 补成真实账户数据(真实 maker/taker 费率、VIP/费率等级、平台币余额、抵扣开启状态)。
//
// 【纪律 — 全程只读,零资金/交易接触】
//   - 只调用签名【只读】端点:OKX trade-fee、Binance commissionRate/feeBurn(查询)、
//     Bybit fee-rate、Gate wallet/fee;复用各所适配器现成的只读能力 + 极薄只读封装。
//   - 绝不下单/撤单、绝不买币(OKB/BNB/GT)、绝不划转、绝不开启/关闭任何抵扣、绝不改交易参数。
//   - 单所失败只告警(写 error 字段),不影响其他所;不接触交易主流程;拿不到的项一律标
//     MANUAL_REQUIRED / UNSUPPORTED,【绝不编造费率/VIP/余额/抵扣状态】。
//   - 不打印任何密钥/密文;凭证只经 app.LoadVenueSecretConfig 加载后传入适配器,从不落日志。
//
// 用法:go run ./cmd/feeprobe [-secret secret.yaml] [-settlements records/okx_directional/settlements.jsonl] [-out records/fee_discount_audit.jsonl]
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"lingxi/internal/app"
	"lingxi/internal/core"
	"lingxi/internal/exchange"
	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

const (
	perVenueTimeout    = 45 * time.Second
	binanceProbeSymbol = "BTCUSDT" // commissionRate 需要 symbol,取主流合约代表账户档
)

// fastAgg 是 settlements 里 fast_v1 结算的只读聚合(用于日均 fee 估算,口径沿用 phase1 脚本)。
type fastAgg struct {
	n    int
	fee  float64
	net  float64
	tmin time.Time
	tmax time.Time
}

func main() {
	secretPath := flag.String("secret", "secret.yaml", "密钥配置路径(每所一节;绝不打印)")
	settlementsPath := flag.String("settlements", "records/okx_directional/settlements.jsonl", "OKX fast 结算文件(只读,用于日均 fee 估算)")
	outPath := flag.String("out", "records/fee_discount_audit.jsonl", "审计输出(追加一批带 time 的新记录)")
	flag.Parse()

	now := time.Now().UTC()
	nowISO := now.Format(time.RFC3339Nano)

	// OKX fast 日均 fee(当前 fast 仅 OKX 有成交;其余所 0)。只读扫描,失败不致命。
	okxDaily, scanNote := scanDailyFee(*settlementsPath)

	fmt.Println("========================================================================")
	fmt.Println("feeprobe:四所私有【只读】账户费率探针(不下单/不买币/不划转/不开抵扣/不改配置)")
	fmt.Println("========================================================================")

	records := []map[string]any{
		probeOKX(nowISO, *secretPath, okxDaily, scanNote),
		probeBinance(nowISO, *secretPath),
		probeBybit(nowISO, *secretPath),
		probeGate(nowISO, *secretPath),
	}

	if err := appendRecords(*outPath, records); err != nil {
		fmt.Fprintf(os.Stderr, "写审计文件失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n已追加 %d 条真实账户费率记录 -> %s\n", len(records), *outPath)
	fmt.Println("提示:本文件为运行态审计数据,请勿 git commit。")
}

// baseRecord 构造带完整字段骨架的记录;数值/状态项默认 MANUAL_REQUIRED,由各所填真实值。
func baseRecord(nowISO, venueName, discountAsset string) map[string]any {
	return map[string]any{
		"time":                     nowISO,
		"venue":                    venueName,
		"status":                   "MANUAL_REQUIRED",
		"maker_fee":                "MANUAL_REQUIRED",
		"taker_fee":                "MANUAL_REQUIRED",
		"vip_level":                "MANUAL_REQUIRED",
		"discount_asset":           discountAsset,
		"discount_asset_balance":   "MANUAL_REQUIRED",
		"discount_asset_value_usdt": "MANUAL_REQUIRED",
		"discount_enabled":         "MANUAL_REQUIRED",
		"discount_supported":       "MANUAL_REQUIRED",
		"api_can_enable_discount":  "MANUAL_REQUIRED",
		"estimated_daily_fee_usdt": 0.0,
		"estimated_saving_10pct":   0.0,
		"estimated_saving_20pct":   0.0,
		"estimated_saving_25pct":   0.0,
		"action_recommendation":    "",
		"risk_note":                "",
		"error":                    nil,
	}
}

func setSavings(rec map[string]any, daily float64) {
	rec["estimated_daily_fee_usdt"] = round6(daily)
	rec["estimated_saving_10pct"] = round6(daily * 0.10)
	rec["estimated_saving_20pct"] = round6(daily * 0.20)
	rec["estimated_saving_25pct"] = round6(daily * 0.25)
}

// discountAssetValueUSDT 用公开盘口 mid 折算平台币余额价值(只读,失败返回 false)。
func discountAssetValueUSDT(ctx context.Context, v venue.Venue, asset string, qty float64) (float64, bool) {
	if qty == 0 {
		return 0, true
	}
	bt, err := v.FetchBookTicker(ctx, venue.MarketSpot, venue.Symbol(strings.ToUpper(asset)+"-USDT"))
	if err != nil {
		return 0, false
	}
	mid := 0.0
	switch {
	case bt.BidPrice > 0 && bt.AskPrice > 0:
		mid = (bt.BidPrice + bt.AskPrice) / 2
	case bt.BidPrice > 0:
		mid = bt.BidPrice
	case bt.AskPrice > 0:
		mid = bt.AskPrice
	default:
		return 0, false
	}
	return qty * mid, true
}

func balanceOf(bal venue.Balances, asset string) float64 {
	b := bal.ByAsset[strings.ToUpper(asset)]
	return b.Free + b.Locked
}

// ---------------------------------------------------------------------------
// OKX:真实 maker/taker + 费率等级(VIP)+ OKB 余额;OKB 是否直接抵扣合约手续费存疑 → MANUAL_REQUIRED。
// ---------------------------------------------------------------------------
func probeOKX(nowISO, secretPath string, daily float64, scanNote string) map[string]any {
	rec := baseRecord(nowISO, okx.VenueName, "OKB")
	setSavings(rec, daily)
	var errs []string

	sec, err := app.LoadVenueSecretConfig(secretPath, okx.VenueName)
	if err != nil {
		rec["error"] = "load secret: " + err.Error()
		rec["risk_note"] = "凭证加载失败,无法拉取账户级费率;其余项保持 MANUAL_REQUIRED。"
		fmt.Printf("\n[okx] 凭证加载失败: %v\n", err)
		return rec
	}
	adp, err := okx.New(venue.Config{Name: okx.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret, Passphrase: sec.Passphrase})
	if err != nil {
		rec["error"] = "new adapter: " + err.Error()
		return rec
	}
	ctx, cancel := context.WithTimeout(context.Background(), perVenueTimeout)
	defer cancel()

	gotFee := false
	if fs, err := adp.FetchFeeSchedule(ctx); err != nil {
		errs = append(errs, "fee-schedule: "+err.Error())
	} else {
		rec["maker_fee"] = fs.Maker
		rec["taker_fee"] = fs.Taker
		if fs.Level != "" {
			rec["vip_level"] = fs.Level
		}
		gotFee = true
	}

	if bal, err := adp.FetchBalances(ctx); err != nil {
		errs = append(errs, "balances: "+err.Error())
	} else {
		okb := balanceOf(bal, "OKB")
		rec["discount_asset_balance"] = round6(okb)
		if val, ok := discountAssetValueUSDT(ctx, adp, "OKB", okb); ok {
			rec["discount_asset_value_usdt"] = round6(val)
		}
	}

	// OKB 抵扣性质:不一定像 BNB 直接抵扣合约手续费,可能仅体现为 VIP 量级档 → 不编造,标 MANUAL_REQUIRED。
	rec["discount_supported"] = "MANUAL_REQUIRED"
	rec["discount_enabled"] = "MANUAL_REQUIRED"
	rec["api_can_enable_discount"] = "MANUAL_REQUIRED"

	if gotFee {
		rec["status"] = "OK"
	}
	rec["action_recommendation"] = "已拉到 OKX 真实 SWAP maker/taker 与费率等级 + OKB 余额。OKB 是否/如何直接抵扣" +
		"合约手续费仍需人工在 OKX 网页费率页确认(可能仅影响 VIP 量级档);确认前切勿自动买 OKB。"
	risk := "maker/taker/vip 为账户实时;OKB 余额为交易账户口径(资金账户 OKB 未计,best-effort)。" +
		"OKB 抵扣机制/是否已开启/API 能否开启:无对等只读端点确证 → MANUAL_REQUIRED,不编造。"
	if scanNote != "" {
		risk += " [" + scanNote + "]"
	}
	if len(errs) > 0 {
		rec["error"] = strings.Join(errs, "; ")
		risk += " [部分只读调用失败,已隔离]"
	}
	rec["risk_note"] = risk

	fmt.Printf("\n[okx] status=%v maker=%v taker=%v vip=%v okb_bal=%v 日均fee≈%.4fU\n",
		rec["status"], rec["maker_fee"], rec["taker_fee"], rec["vip_level"], rec["discount_asset_balance"], daily)
	return rec
}

// ---------------------------------------------------------------------------
// Binance:真实 futures maker/taker + BNB 余额 + bnbBurn(抵扣是否开启)。只读,不开启、不买 BNB。
// ---------------------------------------------------------------------------
func probeBinance(nowISO, secretPath string) map[string]any {
	rec := baseRecord(nowISO, binance.VenueName, "BNB")

	sec, err := app.LoadVenueSecretConfig(secretPath, binance.VenueName)
	if err != nil {
		rec["error"] = "load secret: " + err.Error()
		rec["risk_note"] = "凭证加载失败,无法拉取账户级费率;其余项保持 MANUAL_REQUIRED。"
		fmt.Printf("\n[binance] 凭证加载失败: %v\n", err)
		return rec
	}
	client, err := exchange.NewAuthenticatedBinanceClient(sec, core.DefaultEngineConfig().NetworkConfig)
	if err != nil {
		rec["error"] = "new client: " + err.Error()
		return rec
	}
	adp := binance.New(client)
	ctx, cancel := context.WithTimeout(context.Background(), perVenueTimeout)
	defer cancel()

	var errs []string
	gotFee := false
	if maker, taker, err := client.FetchFuturesCommissionRateReadOnly(ctx, binanceProbeSymbol); err != nil {
		errs = append(errs, "commission-rate: "+err.Error())
	} else {
		rec["maker_fee"] = maker
		rec["taker_fee"] = taker
		gotFee = true
	}

	// BNB 抵扣合约手续费 -10% 机制明确存在;API 可开启(POST /fapi/v1/feeBurn),本探针只查询不开启。
	rec["discount_supported"] = true
	rec["api_can_enable_discount"] = true
	if feeBurn, err := client.FetchBNBBurnStatus(ctx); err != nil {
		errs = append(errs, "fee-burn: "+err.Error())
	} else {
		rec["discount_enabled"] = feeBurn
	}

	if bal, err := adp.FetchBalances(ctx); err != nil {
		errs = append(errs, "balances: "+err.Error())
	} else {
		bnb := balanceOf(bal, "BNB")
		rec["discount_asset_balance"] = round6(bnb)
		if val, ok := discountAssetValueUSDT(ctx, adp, "BNB", bnb); ok {
			rec["discount_asset_value_usdt"] = round6(val)
		}
	}

	// Binance vip_level 无对等只读端点直给(费率已反映档位);标 MANUAL_REQUIRED,不编造。
	rec["vip_level"] = "MANUAL_REQUIRED"

	if gotFee {
		rec["status"] = "OK"
	}
	rec["action_recommendation"] = "已拉到 Binance 真实 futures maker/taker + BNB 余额 + bnbBurn 抵扣开关状态。" +
		"当前 fast 无 Binance 成交,节省估算为 0;是否开启 BNB 抵扣属人工决策,本探针不自动开启、不买 BNB。"
	risk := "maker/taker 为账户实时(已含 VIP 阶梯与 BNB 抵扣);discount_enabled=bnbBurn 只读查询结果。" +
		"vip_level 无独立只读端点 → MANUAL_REQUIRED。"
	if len(errs) > 0 {
		rec["error"] = strings.Join(errs, "; ")
		risk += " [部分只读调用失败,已隔离]"
	}
	rec["risk_note"] = risk

	fmt.Printf("\n[binance] status=%v maker=%v taker=%v bnb_bal=%v bnbBurn=%v\n",
		rec["status"], rec["maker_fee"], rec["taker_fee"], rec["discount_asset_balance"], rec["discount_enabled"])
	return rec
}

// ---------------------------------------------------------------------------
// Bybit:真实 contract maker/taker;无平台币直接抵扣机制 → discount 相关字段 UNSUPPORTED。
// ---------------------------------------------------------------------------
func probeBybit(nowISO, secretPath string) map[string]any {
	rec := baseRecord(nowISO, bybit.VenueName, "unsupported")

	sec, err := app.LoadVenueSecretConfig(secretPath, bybit.VenueName)
	if err != nil {
		rec["error"] = "load secret: " + err.Error()
		rec["risk_note"] = "凭证加载失败,无法拉取账户级费率;其余项保持 MANUAL_REQUIRED。"
		fmt.Printf("\n[bybit] 凭证加载失败: %v\n", err)
		return rec
	}
	adp, err := bybit.New(venue.Config{Name: bybit.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret})
	if err != nil {
		rec["error"] = "new adapter: " + err.Error()
		return rec
	}
	ctx, cancel := context.WithTimeout(context.Background(), perVenueTimeout)
	defer cancel()

	var errs []string
	gotFee := false
	if profile, err := adp.FetchTradingCostProfile(ctx, nil); err != nil {
		errs = append(errs, "fee-rate: "+err.Error())
	} else {
		rec["maker_fee"] = profile.FuturesMaker
		rec["taker_fee"] = profile.FuturesTaker
		gotFee = true
	}

	// Bybit 无平台币直接抵扣手续费机制 → 抵扣相关字段 UNSUPPORTED(事实层),不编造。
	rec["discount_supported"] = false
	rec["discount_enabled"] = "UNSUPPORTED"
	rec["api_can_enable_discount"] = "UNSUPPORTED"
	rec["discount_asset_balance"] = "UNSUPPORTED"
	rec["discount_asset_value_usdt"] = "UNSUPPORTED"
	rec["vip_level"] = "MANUAL_REQUIRED" // fee-rate 端点不返回 VIP 档,无对等只读端点

	if gotFee {
		rec["status"] = "OK"
	}
	rec["action_recommendation"] = "已拉到 Bybit 真实 contract maker/taker。Bybit 无平台币直接抵扣,费率优化只能走 VIP 量级档,无自动动作建议。"
	risk := "maker/taker 为账户实时;Bybit 无平台币抵扣机制 → discount_* 全 UNSUPPORTED;vip_level 无独立只读端点 → MANUAL_REQUIRED。"
	if len(errs) > 0 {
		rec["error"] = strings.Join(errs, "; ")
		risk += " [部分只读调用失败,已隔离]"
	}
	rec["risk_note"] = risk

	fmt.Printf("\n[bybit] status=%v maker=%v taker=%v discount=UNSUPPORTED\n",
		rec["status"], rec["maker_fee"], rec["taker_fee"])
	return rec
}

// ---------------------------------------------------------------------------
// Gate:真实 futures maker/taker + GT 余额 + gt_discount(抵扣是否开启)。只读,不买 GT。
// ---------------------------------------------------------------------------
func probeGate(nowISO, secretPath string) map[string]any {
	rec := baseRecord(nowISO, gate.VenueName, "GT")

	sec, err := app.LoadVenueSecretConfig(secretPath, gate.VenueName)
	if err != nil {
		rec["error"] = "load secret: " + err.Error()
		rec["risk_note"] = "凭证加载失败,无法拉取账户级费率;其余项保持 MANUAL_REQUIRED。"
		fmt.Printf("\n[gate] 凭证加载失败: %v\n", err)
		return rec
	}
	adp, err := gate.New(venue.Config{Name: gate.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret})
	if err != nil {
		rec["error"] = "new adapter: " + err.Error()
		return rec
	}
	ctx, cancel := context.WithTimeout(context.Background(), perVenueTimeout)
	defer cancel()

	var errs []string
	gotFee := false
	if profile, err := adp.FetchTradingCostProfile(ctx, nil); err != nil {
		errs = append(errs, "wallet-fee: "+err.Error())
	} else {
		rec["maker_fee"] = profile.FuturesMaker
		rec["taker_fee"] = profile.FuturesTaker
		rec["discount_enabled"] = profile.DiscountApplied // gt_discount 只读查询结果
		gotFee = true
	}

	if bal, err := adp.FetchBalances(ctx); err != nil {
		errs = append(errs, "balances: "+err.Error())
	} else {
		gt := balanceOf(bal, "GT")
		rec["discount_asset_balance"] = round6(gt)
		if val, ok := discountAssetValueUSDT(ctx, adp, "GT", gt); ok {
			rec["discount_asset_value_usdt"] = round6(val)
		}
	}

	// GT 可抵扣手续费机制明确存在;是否可经 API 开启存疑(账户设置项)→ MANUAL_REQUIRED,不编造。
	rec["discount_supported"] = true
	rec["api_can_enable_discount"] = "MANUAL_REQUIRED"
	rec["vip_level"] = "MANUAL_REQUIRED" // wallet/fee 不返回 VIP 档

	if gotFee {
		rec["status"] = "OK"
	}
	rec["action_recommendation"] = "已拉到 Gate 真实 futures maker/taker + GT 余额 + gt_discount 抵扣开关状态。" +
		"当前 fast 无 Gate 成交,节省估算为 0;本探针不自动开启抵扣、不买 GT。"
	risk := "maker/taker 为账户实时;discount_enabled=gt_discount 只读查询结果;GT 抵扣能否经 API 开启存疑 → " +
		"MANUAL_REQUIRED;vip_level 无独立只读端点 → MANUAL_REQUIRED。"
	if len(errs) > 0 {
		rec["error"] = strings.Join(errs, "; ")
		risk += " [部分只读调用失败,已隔离]"
	}
	rec["risk_note"] = risk

	fmt.Printf("\n[gate] status=%v maker=%v taker=%v gt_bal=%v gt_discount=%v\n",
		rec["status"], rec["maker_fee"], rec["taker_fee"], rec["discount_asset_balance"], rec["discount_enabled"])
	return rec
}

// scanDailyFee 从 settlements.jsonl 扫描 fast_v1 结算的真实 fee 与时间跨度,推日均 fee(只读文件)。
func scanDailyFee(path string) (daily float64, note string) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "settlements 文件不可读(该所无 fast 记录或路径错误): " + err.Error()
	}
	defer f.Close()

	var agg fastAgg
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"signal_layer":"fast_v1"`) {
			continue
		}
		var d struct {
			SignalLayer  string  `json:"signal_layer"`
			FeeUSDT      float64 `json:"fee_usdt"`
			RealizedUSDT float64 `json:"realized_usdt"`
			Time         string  `json:"time"`
		}
		if err := json.Unmarshal(line, &d); err != nil || d.SignalLayer != "fast_v1" {
			continue
		}
		agg.n++
		agg.fee += d.FeeUSDT
		agg.net += d.RealizedUSDT
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(d.Time)); err == nil {
			if agg.tmin.IsZero() || t.Before(agg.tmin) {
				agg.tmin = t
			}
			if agg.tmax.IsZero() || t.After(agg.tmax) {
				agg.tmax = t
			}
		}
	}
	if agg.n == 0 {
		return 0, "settlements 无 fast_v1 结算记录"
	}
	days := 0.0
	if !agg.tmin.IsZero() && !agg.tmax.IsZero() {
		days = agg.tmax.Sub(agg.tmin).Hours() / 24.0
	}
	if days > 0.05 {
		daily = agg.fee / days
	}
	return daily, fmt.Sprintf("fast_v1 n=%d fee总=%.4fU 跨度=%.2f天", agg.n, agg.fee, days)
}

// appendRecords 以追加方式写入一批带 time 的新记录(不覆盖历史;运行态数据,勿 commit)。
func appendRecords(path string, records []map[string]any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func round6(v float64) float64 {
	return float64(int64(v*1e6+sign(v)*0.5)) / 1e6
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
