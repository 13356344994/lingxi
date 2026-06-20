package gate

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"

	"lingxi/internal/venue"
)

// capital.go 实现 Gate 资金管理:现货账户(spot)↔ USDT 永续账户(futures)的 USDT 划转 + 「输入总额自动落位」。
//
// Gate 对冲腿在永续侧;「托管式分配」= 把交易总额放进 futures、余量留 spot。
// 写动作(wallet/transfers)需 API key 开划转权限;绝不触发提币/跨所接口。

// TransferWallet 在所内 spot/futures 钱包之间划转 USDT(Gate wallet/transfers)。
// from/to 取 "spot"/"futures";金额≤0 为 no-op。
func (a *Adapter) TransferWallet(ctx context.Context, from, to string, amountUSDT float64) error {
	if amountUSDT <= 0 {
		return nil
	}
	fromW, toW, err := gateWalletTransferDirection(from, to)
	if err != nil {
		return err
	}
	_, err = a.transferUSDT(ctx, fromW, toW, amountUSDT)
	return err
}

func gateWalletTransferDirection(from, to string) (venue.Wallet, venue.Wallet, error) {
	from = strings.ToLower(strings.TrimSpace(from))
	to = strings.ToLower(strings.TrimSpace(to))
	switch {
	case from == "spot" && to == "futures":
		return WalletSpot, WalletFutures, nil
	case from == "futures" && to == "spot":
		return WalletFutures, WalletSpot, nil
	default:
		return "", "", fmt.Errorf("gate TransferWallet: unsupported %s -> %s", from, to)
	}
}

// gateTransferWallet 把中性 Wallet 映射成 Gate wallet/transfers 的 from/to。
func gateTransferWallet(w venue.Wallet) (string, error) {
	switch w {
	case WalletSpot:
		return "spot", nil
	case WalletFutures:
		return "futures", nil
	default:
		return "", fmt.Errorf("gate: 未知钱包 %q(仅支持 spot/futures)", w)
	}
}

func isSpotFuturesWalletTransfer(from, to venue.Wallet) bool {
	return (from == WalletSpot && to == WalletFutures) || (from == WalletFutures && to == WalletSpot)
}

// isGateUnifiedTransferNoOpError 识别「USDT 永续已迁入统一账户、禁止旧 futures 划转」响应,视为无需搬动。
func isGateUnifiedTransferNoOpError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "PERPETUAL_CONTRACT_TRANSFER_USDT_EXCEPTION") ||
		strings.Contains(msg, "migrated to the Unified Account")
}

func gateUnifiedWalletNoOpTransfer(from, to venue.Wallet) venue.Transfer {
	return venue.Transfer{From: from, To: to, AssetUSDT: 0, VenueRef: "unified-wallet-noop"}
}

// transferUSDT 在本 UID 的 spot↔futures 间划转 USDT。amount<=0 视为无操作。
func (a *Adapter) transferUSDT(ctx context.Context, from, to venue.Wallet, amount float64) (venue.Transfer, error) {
	if amount <= 0 {
		return venue.Transfer{}, fmt.Errorf("gate: 划转金额必须为正, got %v", amount)
	}
	// 统一账户下 spot 与 USDT 永续余额已共享,旧 wallet/transfers 会报 PERPETUAL_CONTRACT_TRANSFER_USDT_EXCEPTION。
	if a.Capabilities().UnifiedWallet && isSpotFuturesWalletTransfer(from, to) {
		return gateUnifiedWalletNoOpTransfer(from, to), nil
	}
	fromW, err := gateTransferWallet(from)
	if err != nil {
		return venue.Transfer{}, err
	}
	toW, err := gateTransferWallet(to)
	if err != nil {
		return venue.Transfer{}, err
	}
	body := struct {
		Currency string `json:"currency"`
		From     string `json:"from"`
		To       string `json:"to"`
		Amount   string `json:"amount"`
		Settle   string `json:"settle"`
	}{Currency: "USDT", From: fromW, To: toW, Amount: formatTransferUSDT(amount), Settle: "usdt"}
	var res struct {
		ID string `json:"id"`
	}
	if err := a.t.signedPost(ctx, pathWalletTransfers, nil, body, &res); err != nil {
		if isGateUnifiedTransferNoOpError(err) {
			return gateUnifiedWalletNoOpTransfer(from, to), nil
		}
		return venue.Transfer{}, err
	}
	ref := "wallet-transfer"
	if res.ID != "" {
		ref = "wallet-transfer:" + res.ID
	}
	return venue.Transfer{From: from, To: to, AssetUSDT: truncateTransferUSDT(amount), VenueRef: ref}, nil
}

// FetchSpotAvailableUSDT 返回现货账户 USDT available(不含 USDT 永续账户)。
func (a *Adapter) FetchSpotAvailableUSDT(ctx context.Context) (float64, error) {
	return a.fetchSpotUSDT(ctx)
}

// fetchSpotUSDT 拉现货账户 USDT 可动用余额(available,非 locked)。
func (a *Adapter) fetchSpotUSDT(ctx context.Context) (float64, error) {
	q := url.Values{}
	q.Set("currency", "USDT")
	var rows []struct {
		Currency  string `json:"currency"`
		Available string `json:"available"`
	}
	if err := a.t.signedGet(ctx, pathSpotAccounts, q, &rows); err != nil {
		return 0, err
	}
	for _, r := range rows {
		if strings.EqualFold(strings.TrimSpace(r.Currency), "USDT") {
			return math.Max(parseFloat(r.Available), 0), nil
		}
	}
	return 0, nil
}

// fetchFuturesUSDT 拉 USDT 永续账户可动用余额(available 字段)。
func (a *Adapter) fetchFuturesUSDT(ctx context.Context) (float64, error) {
	var acct struct {
		Currency  string `json:"currency"`
		Available string `json:"available"`
	}
	if err := a.t.signedGet(ctx, pathFuturesAccounts, nil, &acct); err != nil {
		return 0, err
	}
	return math.Max(parseFloat(acct.Available), 0), nil
}

// CaptureMovableSnapshot 拉「现货 + USDT 永续」可动用 USDT 快照与合计。
func (a *Adapter) CaptureMovableSnapshot(ctx context.Context) ([]venue.WalletSnapshot, float64, error) {
	spot, err := a.fetchSpotUSDT(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("读取现货账户失败: %w", err)
	}
	futures, err := a.fetchFuturesUSDT(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("读取永续账户失败: %w", err)
	}
	snaps := []venue.WalletSnapshot{
		{Wallet: WalletFutures, Label: "USDT 永续账户", FreeUSDT: futures},
		{Wallet: WalletSpot, Label: "现货账户", FreeUSDT: spot},
	}
	return snaps, spot + futures, nil
}

// PlanAllocation 纯函数:把交易总额全部放进 USDT 永续账户,余量留现货账户。
func (a *Adapter) PlanAllocation(totalUSDT float64, caps venue.Capabilities) venue.CapitalAllocationPlan {
	if totalUSDT < 0 {
		totalUSDT = 0
	}
	return venue.CapitalAllocationPlan{
		TotalTradingUSDT: totalUSDT,
		Targets:          map[venue.Wallet]float64{WalletFutures: totalUSDT},
		RemainderWallet:  WalletSpot,
	}
}

// ExecuteRebalance 按计划把 futures 账户 USDT 调到 plan.Targets[futures],差额与 spot 对划;余量自然留 spot。
func (a *Adapter) ExecuteRebalance(ctx context.Context, plan venue.CapitalAllocationPlan) ([]venue.Transfer, error) {
	target := plan.Targets[WalletFutures]
	if target < 0 {
		return nil, fmt.Errorf("gate: 永续账户目标不能为负: %v", target)
	}
	snaps, total, err := a.CaptureMovableSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if target > total+1e-6 {
		return nil, fmt.Errorf("gate: 目标交易额 %.2f 超过可调总额 %.2f(spot+futures)", target, total)
	}
	var futuresNow, spotNow float64
	for _, s := range snaps {
		switch s.Wallet {
		case WalletFutures:
			futuresNow = s.FreeUSDT
		case WalletSpot:
			spotNow = s.FreeUSDT
		}
	}
	const eps = 1e-6
	diff := target - futuresNow
	switch {
	case diff > eps: // futures 不足 → 从 spot 补
		amount := truncateTransferUSDT(math.Min(diff, spotNow))
		if amount <= eps {
			return nil, nil
		}
		tr, err := a.transferUSDT(ctx, WalletSpot, WalletFutures, amount)
		if err != nil {
			return nil, err
		}
		return []venue.Transfer{tr}, nil
	case diff < -eps: // futures 超目标 → 多余回 spot
		amount := truncateTransferUSDT(-diff)
		if amount <= eps {
			return nil, nil
		}
		tr, err := a.transferUSDT(ctx, WalletFutures, WalletSpot, amount)
		if err != nil {
			return nil, err
		}
		return []venue.Transfer{tr}, nil
	default:
		return nil, nil
	}
}

// truncateTransferUSDT 把划转金额向下截断到 8 位小数;向下截断确保不超过可用余额。
func truncateTransferUSDT(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return math.Floor(v*1e8) / 1e8
}

// formatTransferUSDT 格式化为非科学计数法字符串(固定 8 位有效截断后 fmtNum 输出)。
func formatTransferUSDT(v float64) string {
	return fmtNum(truncateTransferUSDT(v))
}
