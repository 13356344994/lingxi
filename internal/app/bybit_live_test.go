package app

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
)

// TestBybitLiveReadOnly 对真实 Bybit API 跑「只读」活体验证(Stage 1 收尾)。
//
// 安全边界:仅公共行情 + 签名只读账户(余额/持仓/费率/key 自检),绝不下单/撤单/划转/借还。
// 凭证经现有 LoadVenueSecretConfig 从 secret.yaml 正规加载(不硬编码)。默认 SKIP,
// 仅当 BYBIT_LIVE=1 时运行,避免普通 `go test ./...` 触网。
//
// 运行:BYBIT_LIVE=1 go test ./internal/app -run TestBybitLiveReadOnly -v
func TestBybitLiveReadOnly(t *testing.T) {
	if os.Getenv("BYBIT_LIVE") != "1" {
		t.Skip("设置 BYBIT_LIVE=1 运行 Bybit 只读活体验证")
	}
	secretPath := os.Getenv("BYBIT_SECRET_PATH")
	if secretPath == "" {
		secretPath = "../../secret.yaml" // 包测试 cwd = internal/app,secret 在仓库根
	}
	sec, err := LoadVenueSecretConfig(secretPath, bybit.VenueName)
	if err != nil {
		t.Fatalf("加载 Bybit 凭证失败: %v", err)
	}
	adp, err := bybit.New(venue.Config{Name: bybit.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret})
	if err != nil {
		t.Fatalf("构造 Bybit 适配器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// 1) 公共:服务器时间(连通性 + 时钟偏移)。
	srvTime, err := adp.ServerTime(ctx)
	if err != nil {
		t.Fatalf("ServerTime 失败: %v", err)
	}
	skew := time.Since(srvTime)
	t.Logf("[公共] ServerTime=%s 本地时钟偏移≈%s", srvTime.Format(time.RFC3339), skew.Round(time.Millisecond))

	// 2) 公共:instruments(永续+现货数量)。
	insts, err := adp.FetchInstruments(ctx)
	if err != nil {
		t.Fatalf("FetchInstruments 失败: %v", err)
	}
	var perp, spot int
	for _, it := range insts {
		switch it.Market {
		case venue.MarketPerp:
			perp++
		case venue.MarketSpot:
			spot++
		}
	}
	t.Logf("[公共] Instruments: perp=%d spot=%d (合计 %d)", perp, spot, len(insts))
	if perp == 0 {
		t.Errorf("永续 instruments 为 0,疑似解析或连通异常")
	}

	// 3) 公共:资金费快照 + 找出最负的几个(验证费率符号与排序合理)。
	snaps, err := adp.FetchFundingSnapshot(ctx)
	if err != nil {
		t.Fatalf("FetchFundingSnapshot 失败: %v", err)
	}
	t.Logf("[公共] FundingSnapshot: %d 个永续", len(snaps))
	var mostNeg venue.FundingSnapshot
	for _, s := range snaps {
		if s.LastFundingRate < mostNeg.LastFundingRate {
			mostNeg = s
		}
	}
	if mostNeg.Symbol != "" {
		t.Logf("[公共] 最负资金费: %s rate=%.4f%% interval=%dh next=%s",
			mostNeg.Symbol, mostNeg.LastFundingRate*100, mostNeg.IntervalHours, mostNeg.NextFundingTime.Format(time.RFC3339))
	}

	// 4) 公共:BTC-USDT 盘口一档。
	bt, err := adp.FetchBookTicker(ctx, venue.MarketPerp, "BTC-USDT")
	if err != nil {
		t.Fatalf("FetchBookTicker 失败: %v", err)
	}
	t.Logf("[公共] BTC-USDT 盘口: bid=%.2f(%.4f) ask=%.2f(%.4f)", bt.BidPrice, bt.BidQty, bt.AskPrice, bt.AskQty)
	if bt.BidPrice <= 0 || bt.AskPrice <= 0 {
		t.Errorf("BTC-USDT 盘口价非正,疑似解析异常")
	}

	// 5) 签名只读:API key 安全姿态(套利 key 期望无提现权限)。
	posture, err := adp.VerifyAPIKeyPosture(ctx)
	if err != nil {
		t.Fatalf("VerifyAPIKeyPosture 失败(签名/权限问题?): %v", err)
	}
	t.Logf("[签名] KeyPosture: canTrade=%v canWithdraw=%v ipRestricted=%v notes=%q",
		posture.CanTrade, posture.CanWithdraw, posture.IPRestricted, posture.Notes)
	if posture.CanWithdraw {
		t.Errorf("⚠️ 该 Bybit key 具有提现权限,套利 key 不应开提现")
	}

	// 6) 签名只读:统一账户余额。
	bal, err := adp.FetchBalances(ctx)
	if err != nil {
		t.Fatalf("FetchBalances 失败(签名问题?): %v", err)
	}
	var totalUSD float64
	for _, b := range bal.ByAsset {
		totalUSD += b.WalletUSD
	}
	t.Logf("[签名] 余额资产数=%d 估值合计≈%.2f USD", len(bal.ByAsset), totalUSD)

	// 7) 签名只读:持仓。
	pos, err := adp.FetchPositions(ctx)
	if err != nil {
		t.Fatalf("FetchPositions 失败: %v", err)
	}
	t.Logf("[签名] 当前持仓数=%d", len(pos))
}

// TestBybitLocateFundsLive 诊断「充值后钱在哪个账户」:对真实账户分别查 UNIFIED / CONTRACT / FUND 钱包。
// 只读。运行:BYBIT_LIVE=1 go test ./internal/app -run TestBybitLocateFundsLive -v
func TestBybitLocateFundsLive(t *testing.T) {
	if os.Getenv("BYBIT_LIVE") != "1" {
		t.Skip("设置 BYBIT_LIVE=1 运行 Bybit 资金落点诊断")
	}
	secretPath := os.Getenv("BYBIT_SECRET_PATH")
	if secretPath == "" {
		secretPath = "../../secret.yaml"
	}
	sec, err := LoadVenueSecretConfig(secretPath, bybit.VenueName)
	if err != nil {
		t.Fatalf("加载 bybit 凭证失败: %v", err)
	}
	adp, err := bybit.New(venue.Config{Name: bybit.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret})
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if bal, err := adp.FetchWalletBalanceByType(ctx, "UNIFIED"); err != nil {
		t.Logf("[UNIFIED] 查询失败: %v", err)
	} else {
		t.Logf("[UNIFIED 统一交易账户] 资产数=%d", len(bal.ByAsset))
		for a, b := range bal.ByAsset {
			t.Logf("  UNIFIED %s: free=%.8f locked=%.8f usd=%.2f", a, b.Free, b.Locked, b.WalletUSD)
		}
	}
	if bal, err := adp.FetchWalletBalanceByType(ctx, "CONTRACT"); err != nil {
		t.Logf("[CONTRACT] 查询失败(UTA 账户该类型通常不可用,可忽略): %v", err)
	} else {
		t.Logf("[CONTRACT 经典合约账户] 资产数=%d", len(bal.ByAsset))
		for a, b := range bal.ByAsset {
			t.Logf("  CONTRACT %s: free=%.8f usd=%.2f", a, b.Free, b.WalletUSD)
		}
	}
	if fund, err := adp.FetchFundingWalletBalance(ctx, ""); err != nil {
		t.Logf("[FUND] 查询失败: %v", err)
	} else {
		t.Logf("[FUND 资金账户] 币种数=%d", len(fund))
		for a, v := range fund {
			t.Logf("  FUND %s: %.8f", a, v)
		}
		if len(fund) == 0 {
			t.Logf("  FUND 资金账户为空")
		}
	}
}

// TestBybitAllocateCapitalLive 是「输入总额→自动落位」的活体执行器(托管式资金管理,对标币安)。
// 把交易总额放进统一交易账户(UTA),余量留资金账户;现货/合约 50/50 在下单时分,不做钱包预切。
// **会真实划转资金**,故双重门控:BYBIT_LIVE=1 且 BYBIT_ALLOCATE_USDT=<目标总额>。
// 运行:BYBIT_LIVE=1 BYBIT_ALLOCATE_USDT=100 go test ./internal/app -run TestBybitAllocateCapitalLive -v
func TestBybitAllocateCapitalLive(t *testing.T) {
	if os.Getenv("BYBIT_LIVE") != "1" {
		t.Skip("设置 BYBIT_LIVE=1 运行")
	}
	targetStr := os.Getenv("BYBIT_ALLOCATE_USDT")
	if targetStr == "" {
		t.Skip("设置 BYBIT_ALLOCATE_USDT=<目标交易总额> 执行划转")
	}
	target, perr := strconv.ParseFloat(targetStr, 64)
	if perr != nil || target < 0 {
		t.Fatalf("BYBIT_ALLOCATE_USDT 非法: %q", targetStr)
	}
	secretPath := os.Getenv("BYBIT_SECRET_PATH")
	if secretPath == "" {
		secretPath = "../../secret.yaml"
	}
	sec, err := LoadVenueSecretConfig(secretPath, bybit.VenueName)
	if err != nil {
		t.Fatalf("加载 bybit 凭证失败: %v", err)
	}
	adp, err := bybit.New(venue.Config{Name: bybit.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret})
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	before, totalBefore, err := adp.CaptureMovableSnapshot(ctx)
	if err != nil {
		t.Fatalf("读取划转前快照失败: %v", err)
	}
	for _, s := range before {
		t.Logf("[划转前] %s(%s): %.4f USDT", s.Label, s.Wallet, s.FreeUSDT)
	}
	t.Logf("[划转前] 可调总额=%.4f USDT,目标交易总额=%.4f USDT", totalBefore, target)

	plan := adp.PlanAllocation(target, adp.Capabilities())
	transfers, err := adp.ExecuteRebalance(ctx, plan)
	if err != nil {
		t.Fatalf("执行划转失败: %v", err)
	}
	if len(transfers) == 0 {
		t.Logf("[划转] 已就位,无需划转")
	}
	for _, tr := range transfers {
		t.Logf("[划转] %s → %s  %.4f USDT (%s)", tr.From, tr.To, tr.AssetUSDT, tr.VenueRef)
	}

	after, totalAfter, err := adp.CaptureMovableSnapshot(ctx)
	if err != nil {
		t.Fatalf("读取划转后快照失败: %v", err)
	}
	for _, s := range after {
		t.Logf("[划转后] %s(%s): %.4f USDT", s.Label, s.Wallet, s.FreeUSDT)
	}
	t.Logf("[划转后] 可调总额=%.4f USDT(应与划转前一致,仅在账户间移动)", totalAfter)
}

// TestBybitShadowExecutorLive 用真实 Bybit 公共行情跑一轮 shadow 规划(B2 端到端,**只读、不下单**)。
// 用公共行情即可(无需签名),验证选币→定量→净利估算在真实数据上自洽。
// 运行:BYBIT_LIVE=1 go test ./internal/app -run TestBybitShadowExecutorLive -v
func TestBybitShadowExecutorLive(t *testing.T) {
	if os.Getenv("BYBIT_LIVE") != "1" {
		t.Skip("设置 BYBIT_LIVE=1 运行 Bybit shadow 活体规划")
	}
	adp, err := bybit.New(venue.Config{Name: bybit.VenueName}) // 公共行情无需密钥
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	cfg := BybitExecConfig{
		AllocatedUSDT:    2000,
		TopN:             5,
		MinFundingRate:   0.0005, // 0.05%/结算
		MinNetProfitRate: 0.0003,
		SpotTakerFee:     0.001, // 兜底静态费率(若 FetchTradingCostProfile 无签名失败)
		PerpTakerFee:     0.00055,
	}
	exec := NewBybitShadowExecutor(adp, cfg, t.Logf)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan, err := exec.PlanCycle(ctx)
	if err != nil {
		t.Fatalf("PlanCycle: %v", err)
	}
	exec.LogShadowPlan(plan)
	t.Logf("[shadow-live] 候选=%d 通过=%d 放弃=%d", len(plan.Candidates), len(plan.Accepted), len(plan.Rejected))
}
