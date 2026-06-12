package okx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"

	"lingxi/internal/venue"
)

// capital.go 实现 OKX 资金管理:资金账户(funding)↔ 统一交易账户(trading)的 USDT 划转 + 「输入总额自动落位」。
//
// 统一账户模型:现货与永续共用 trading 账户保证金;「托管式分配」= 把交易总额放进 trading、余量留 funding。
// 写动作(asset/transfer)需 API key 开「资金划转」权限;绝不触发提币/跨所接口。

// okxTransferAcct 把中性 Wallet 映射成 OKX asset/transfer 的 from/to 账户码。
func okxTransferAcct(w venue.Wallet) (string, error) {
	switch w {
	case WalletFunding:
		return "6", nil // 资金账户
	case WalletTrading:
		return "18", nil // 交易账户(统一账户)
	default:
		return "", fmt.Errorf("okx: 未知钱包 %q(仅支持 funding/trading)", w)
	}
}

// newTransferClientID 生成 asset/transfer 的 clientId(净化后作幂等键)。
func newTransferClientID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return sanitizeClOrdID(hex.EncodeToString(b[:])), nil
}

// transferUSDT 在本 UID 的 funding↔trading 间划转 USDT。amount<=0 视为无操作。
func (a *Adapter) transferUSDT(ctx context.Context, from, to venue.Wallet, amount float64) (venue.Transfer, error) {
	if amount <= 0 {
		return venue.Transfer{}, fmt.Errorf("okx: 划转金额必须为正, got %v", amount)
	}
	fromAcct, err := okxTransferAcct(from)
	if err != nil {
		return venue.Transfer{}, err
	}
	toAcct, err := okxTransferAcct(to)
	if err != nil {
		return venue.Transfer{}, err
	}
	clientID, err := newTransferClientID()
	if err != nil {
		return venue.Transfer{}, err
	}
	amtStr := formatTransferUSDT(amount)
	body := struct {
		Ccy      string `json:"ccy"`
		Amt      string `json:"amt"`
		From     string `json:"from"`
		To       string `json:"to"`
		Type     string `json:"type"`
		ClientID string `json:"clientId"`
	}{Ccy: "USDT", Amt: amtStr, From: fromAcct, To: toAcct, Type: "0", ClientID: clientID}
	var rows []struct {
		TransID string `json:"transId"`
	}
	if err := a.t.signedPost(ctx, pathAssetTransfer, body, &rows); err != nil {
		return venue.Transfer{}, err
	}
	ref := "asset-transfer:" + clientID
	if len(rows) > 0 && rows[0].TransID != "" {
		ref = "asset-transfer:" + rows[0].TransID
	}
	return venue.Transfer{From: from, To: to, AssetUSDT: truncateTransferUSDT(amount), VenueRef: ref}, nil
}

// fetchFundingUSDT 拉资金账户 USDT 可动用余额(availBal,非冻结)。
func (a *Adapter) fetchFundingUSDT(ctx context.Context) (float64, error) {
	q := url.Values{}
	q.Set("ccy", "USDT")
	var rows []struct {
		AvailBal string `json:"availBal"`
		Bal      string `json:"bal"`
	}
	if err := a.t.signedGet(ctx, pathAssetBalances, q, &rows); err != nil {
		return 0, err
	}
	for _, r := range rows {
		free := parseFloat(r.AvailBal)
		if free == 0 {
			free = parseFloat(r.Bal)
		}
		return math.Max(free, 0), nil
	}
	return 0, nil
}

// fetchTradingUSDT 拉统一交易账户 USDT 可动用余额(availBal,非 cashBal/冻结)。
func (a *Adapter) fetchTradingUSDT(ctx context.Context) (float64, error) {
	q := url.Values{}
	q.Set("ccy", "USDT")
	var rows []struct {
		Details []struct {
			Ccy      string `json:"ccy"`
			AvailBal string `json:"availBal"`
		} `json:"details"`
	}
	if err := a.t.signedGet(ctx, pathBalance, q, &rows); err != nil {
		return 0, err
	}
	for _, acct := range rows {
		for _, d := range acct.Details {
			if d.Ccy == "USDT" {
				return math.Max(parseFloat(d.AvailBal), 0), nil
			}
		}
	}
	return 0, nil
}

// CaptureMovableSnapshot 拉「统一交易账户 + 资金账户」可动用 USDT 快照与合计。
func (a *Adapter) CaptureMovableSnapshot(ctx context.Context) ([]venue.WalletSnapshot, float64, error) {
	trading, err := a.fetchTradingUSDT(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("读取统一交易账户失败: %w", err)
	}
	funding, err := a.fetchFundingUSDT(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("读取资金账户失败: %w", err)
	}
	snaps := []venue.WalletSnapshot{
		{Wallet: WalletTrading, Label: "统一交易账户", FreeUSDT: trading},
		{Wallet: WalletFunding, Label: "资金账户", FreeUSDT: funding},
	}
	return snaps, trading + funding, nil
}

// PlanAllocation 纯函数:把交易总额全部放进统一交易账户,余量留资金账户。
func (a *Adapter) PlanAllocation(totalUSDT float64, caps venue.Capabilities) venue.CapitalAllocationPlan {
	if totalUSDT < 0 {
		totalUSDT = 0
	}
	return venue.CapitalAllocationPlan{
		TotalTradingUSDT: totalUSDT,
		Targets:          map[venue.Wallet]float64{WalletTrading: totalUSDT},
		RemainderWallet:  WalletFunding,
	}
}

// ExecuteRebalance 按计划把 trading 账户 USDT 调到 plan.Targets[trading],差额与 funding 对划;余量自然留 funding。
func (a *Adapter) ExecuteRebalance(ctx context.Context, plan venue.CapitalAllocationPlan) ([]venue.Transfer, error) {
	target := plan.Targets[WalletTrading]
	if target < 0 {
		return nil, fmt.Errorf("okx: 交易账户目标不能为负: %v", target)
	}
	snaps, total, err := a.CaptureMovableSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if target > total+1e-6 {
		return nil, fmt.Errorf("okx: 目标交易额 %.2f 超过可调总额 %.2f(trading+funding)", target, total)
	}
	var tradingNow, fundingNow float64
	for _, s := range snaps {
		switch s.Wallet {
		case WalletTrading:
			tradingNow = s.FreeUSDT
		case WalletFunding:
			fundingNow = s.FreeUSDT
		}
	}
	const eps = 1e-6
	diff := target - tradingNow
	switch {
	case diff > eps: // trading 不足 → 从 funding 补
		amount := truncateTransferUSDT(math.Min(diff, fundingNow))
		if amount <= eps {
			return nil, nil
		}
		tr, err := a.transferUSDT(ctx, WalletFunding, WalletTrading, amount)
		if err != nil {
			return nil, err
		}
		return []venue.Transfer{tr}, nil
	case diff < -eps: // trading 超目标 → 多余回 funding
		amount := truncateTransferUSDT(-diff)
		if amount <= eps {
			return nil, nil
		}
		tr, err := a.transferUSDT(ctx, WalletTrading, WalletFunding, amount)
		if err != nil {
			return nil, err
		}
		return []venue.Transfer{tr}, nil
	default:
		return nil, nil
	}
}

// truncateTransferUSDT 把划转金额向下截断到 8 位小数(OKX USDT 精度);向下截断确保不超过可用余额。
func truncateTransferUSDT(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return math.Floor(v*1e8) / 1e8
}

// formatTransferUSDT 格式化为非科学计数法字符串(固定 8 位小数,避免 amt 精度/格式被拒)。
func formatTransferUSDT(v float64) string {
	return fmtNum(truncateTransferUSDT(v))
}
