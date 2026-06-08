package bybit

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"

	"lingxi/internal/venue"
)

// capital.go 实现 Bybit 资金管理:资金账户(FUND)↔统一交易账户(UNIFIED)的 USDT 划转 + 「输入总额自动落位」。
//
// UTA 模型:统一账户里现货与合约共用一套保证金,故没有币安那种 25/25/50 的钱包预切——
// 「托管式分配」对 Bybit = 把交易总额放进统一账户、余量留在资金账户;现货/合约 50/50 在下单时分。
//
// 写动作(inter-transfer)需 API key 开「资产划转」权限(canTransfer);绝不触发提现(canWithdraw 期望 false)。

// bybitAccountType 把中性 Wallet 映射成 Bybit accountType。
func bybitAccountType(w venue.Wallet) (string, error) {
	switch w {
	case WalletUnified:
		return "UNIFIED", nil
	case WalletFunding:
		return "FUND", nil
	default:
		return "", fmt.Errorf("bybit: 未知钱包 %q(仅支持 unified/funding)", w)
	}
}

// newUUIDv4 生成 inter-transfer 需要的 transferId(同时充当幂等键)。
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// transferUSDT 在本 UID 的账户类型间划转 USDT。amount<=0 视为无操作。
func (a *Adapter) transferUSDT(ctx context.Context, from, to venue.Wallet, amount float64) (venue.Transfer, error) {
	if amount <= 0 {
		return venue.Transfer{}, fmt.Errorf("bybit: 划转金额必须为正, got %v", amount)
	}
	fromType, err := bybitAccountType(from)
	if err != nil {
		return venue.Transfer{}, err
	}
	toType, err := bybitAccountType(to)
	if err != nil {
		return venue.Transfer{}, err
	}
	id, err := newUUIDv4()
	if err != nil {
		return venue.Transfer{}, err
	}
	body := struct {
		TransferID      string `json:"transferId"`
		Coin            string `json:"coin"`
		Amount          string `json:"amount"`
		FromAccountType string `json:"fromAccountType"`
		ToAccountType   string `json:"toAccountType"`
	}{TransferID: id, Coin: "USDT", Amount: fmtNum(amount), FromAccountType: fromType, ToAccountType: toType}
	var res struct {
		TransferID string `json:"transferId"`
		Status     string `json:"status"`
	}
	if err := a.t.signedPost(ctx, pathInterTransfer, body, &res); err != nil {
		return venue.Transfer{}, err
	}
	return venue.Transfer{From: from, To: to, AssetUSDT: amount, VenueRef: "inter-transfer:" + res.TransferID}, nil
}

// CaptureMovableSnapshot 拉「统一交易账户 + 资金账户」可动用 USDT 快照与合计。
func (a *Adapter) CaptureMovableSnapshot(ctx context.Context) ([]venue.WalletSnapshot, float64, error) {
	uni, err := a.FetchWalletBalanceByType(ctx, "UNIFIED")
	if err != nil {
		return nil, 0, fmt.Errorf("读取统一交易账户失败: %w", err)
	}
	uniUSDT := uni.ByAsset["USDT"].Free
	fund, err := a.FetchFundingWalletBalance(ctx, "USDT")
	if err != nil {
		return nil, 0, fmt.Errorf("读取资金账户失败: %w", err)
	}
	fundUSDT := fund["USDT"]
	snaps := []venue.WalletSnapshot{
		{Wallet: WalletUnified, Label: "统一交易账户", FreeUSDT: math.Max(uniUSDT, 0)},
		{Wallet: WalletFunding, Label: "资金账户", FreeUSDT: math.Max(fundUSDT, 0)},
	}
	return snaps, math.Max(uniUSDT, 0) + math.Max(fundUSDT, 0), nil
}

// PlanAllocation 纯函数:把交易总额全部放进统一交易账户,余量留资金账户。
// total 会被夹到 [0, 不超过可分配] 由调用方保证;此处只生成目标计划,满足一致性套件不变式(总额回填/非负/不超分)。
func (a *Adapter) PlanAllocation(totalUSDT float64, caps venue.Capabilities) venue.CapitalAllocationPlan {
	if totalUSDT < 0 {
		totalUSDT = 0
	}
	return venue.CapitalAllocationPlan{
		TotalTradingUSDT: totalUSDT,
		Targets:          map[venue.Wallet]float64{WalletUnified: totalUSDT},
		RemainderWallet:  WalletFunding,
	}
}

// ExecuteRebalance 按计划把统一交易账户 USDT 调到 plan.Targets[unified],差额与资金账户对划;余量自然留资金账户。
// 真实划转,需 key 有划转权限。返回实际发生的划转记录。
func (a *Adapter) ExecuteRebalance(ctx context.Context, plan venue.CapitalAllocationPlan) ([]venue.Transfer, error) {
	target := plan.Targets[WalletUnified]
	if target < 0 {
		return nil, fmt.Errorf("bybit: 统一账户目标不能为负: %v", target)
	}
	snaps, total, err := a.CaptureMovableSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if target > total+1e-6 {
		return nil, fmt.Errorf("bybit: 目标交易额 %.2f 超过可调总额 %.2f(统一+资金)", target, total)
	}
	var uniNow, fundNow float64
	for _, s := range snaps {
		switch s.Wallet {
		case WalletUnified:
			uniNow = s.FreeUSDT
		case WalletFunding:
			fundNow = s.FreeUSDT
		}
	}
	const eps = 1e-6
	diff := target - uniNow
	switch {
	case diff > eps: // 统一账户不足 → 从资金账户补
		amount := truncateTransferUSDT(math.Min(diff, fundNow))
		if amount <= eps {
			return nil, nil
		}
		tr, err := a.transferUSDT(ctx, WalletFunding, WalletUnified, amount)
		if err != nil {
			return nil, err
		}
		return []venue.Transfer{tr}, nil
	case diff < -eps: // 统一账户超目标 → 多余回资金账户
		amount := truncateTransferUSDT(-diff)
		if amount <= eps {
			return nil, nil
		}
		tr, err := a.transferUSDT(ctx, WalletUnified, WalletFunding, amount)
		if err != nil {
			return nil, err
		}
		return []venue.Transfer{tr}, nil
	default:
		return nil, nil // 已就位,无需划转
	}
}

// truncateTransferUSDT 把划转金额向下截断到 2 位小数(分)。Bybit inter-transfer 的 USDT 金额精度小于 8 位,
// 直接发 8 位小数会撞 retCode=131210 "amount accuracy err: transfer amount scale more than accuracy length";
// 2 位小数普遍在交易所精度内,对资金调仓而言精度足够(亚分级残差忽略不计)。向下截断确保不超过可用余额。
func truncateTransferUSDT(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return math.Floor(v*100) / 100
}
