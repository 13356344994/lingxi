package bybit

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"lingxi/internal/venue"
)

// account.go 实现 Bybit v5 私有只读账户(签名 GET):余额/持仓/费率/杠杆档位/key 自检。
// 全部为只读,Stage 1 可用真实密钥活体校验,零资金风险。

// FetchBalances 拉统一账户(UNIFIED)余额并归一为中性 Balances。交易在 UTA 下进行,故下单/风控以此为准。
func (a *Adapter) FetchBalances(ctx context.Context) (venue.Balances, error) {
	return a.FetchWalletBalanceByType(ctx, "UNIFIED")
}

// FetchWalletBalanceByType 拉指定 accountType(UNIFIED/CONTRACT)的钱包余额,归一为中性 Balances。
// 诊断「钱到底在哪个账户」用;Funding 钱包不在 wallet-balance 接口,见 FetchFundingWalletBalance。
func (a *Adapter) FetchWalletBalanceByType(ctx context.Context, accountType string) (venue.Balances, error) {
	q := url.Values{}
	q.Set("accountType", accountType)
	var res struct {
		List []struct {
			AccountType string `json:"accountType"`
			Coin        []struct {
				Coin                string `json:"coin"`
				WalletBalance       string `json:"walletBalance"`
				Locked              string `json:"locked"`
				AvailableToWithdraw string `json:"availableToWithdraw"`
				USDValue            string `json:"usdValue"`
			} `json:"coin"`
		} `json:"list"`
	}
	if err := a.t.signedGet(ctx, pathWalletBalance, q, &res); err != nil {
		return venue.Balances{}, err
	}
	bal := venue.Balances{ByAsset: make(map[string]venue.Balance)}
	for _, acct := range res.List {
		for _, c := range acct.Coin {
			asset := strings.ToUpper(c.Coin)
			wallet := parseFloat(c.WalletBalance)
			locked := parseFloat(c.Locked)
			free := parseFloat(c.AvailableToWithdraw)
			if free == 0 {
				free = wallet - locked
			}
			bal.ByAsset[asset] = venue.Balance{
				Asset:     asset,
				Free:      free,
				Locked:    locked,
				WalletUSD: parseFloat(c.USDValue),
			}
		}
	}
	return bal, nil
}

// FetchFundingWalletBalance 拉 Funding(资金)钱包某币余额。Bybit 充值默认落在资金账户,需划转到统一交易
// 账户(UTA)才能下单。coin 为空则取全部。只读(资产查询接口)。
func (a *Adapter) FetchFundingWalletBalance(ctx context.Context, coin string) (map[string]float64, error) {
	q := url.Values{}
	q.Set("accountType", "FUND")
	if coin != "" {
		q.Set("coin", strings.ToUpper(coin))
	}
	var res struct {
		Balance []struct {
			Coin            string `json:"coin"`
			WalletBalance   string `json:"walletBalance"`
			TransferBalance string `json:"transferBalance"`
		} `json:"balance"`
	}
	if err := a.t.signedGet(ctx, pathAssetCoinsBalance, q, &res); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(res.Balance))
	for _, b := range res.Balance {
		out[strings.ToUpper(b.Coin)] = parseFloat(b.WalletBalance)
	}
	return out, nil
}

// FetchPositions 拉 linear 持仓(settleCoin=USDT),size 按 side 带符号。
func (a *Adapter) FetchPositions(ctx context.Context) ([]venue.Position, error) {
	q := url.Values{}
	q.Set("category", categoryLinear)
	q.Set("settleCoin", "USDT")
	var res struct {
		List []struct {
			Symbol        string `json:"symbol"`
			Side          string `json:"side"` // Buy/Sell/""
			Size          string `json:"size"`
			AvgPrice      string `json:"avgPrice"`
			MarkPrice     string `json:"markPrice"`
			UnrealisedPnl string `json:"unrealisedPnl"`
			Leverage      string `json:"leverage"`
			TradeMode     int    `json:"tradeMode"` // 0=cross,1=isolated
		} `json:"list"`
	}
	if err := a.t.signedGet(ctx, pathPositionList, q, &res); err != nil {
		return nil, err
	}
	out := make([]venue.Position, 0, len(res.List))
	for _, p := range res.List {
		size := parseFloat(p.Size)
		if size == 0 {
			continue
		}
		if strings.EqualFold(p.Side, "Sell") {
			size = -size
		}
		mode := venue.MarginModeCross
		if p.TradeMode == 1 {
			mode = venue.MarginModeIsolated
		}
		canonical, _ := a.ToCanonical(p.Symbol, venue.MarketPerp)
		out = append(out, venue.Position{
			Symbol:        canonical,
			PositionAmt:   size,
			EntryPrice:    parseFloat(p.AvgPrice),
			MarkPrice:     parseFloat(p.MarkPrice),
			UnrealizedPnL: parseFloat(p.UnrealisedPnl),
			Leverage:      int(parseFloat(p.Leverage)),
			MarginMode:    mode,
		})
	}
	return out, nil
}

// FetchTradingCostProfile 拉 linear(+spot 尽力)实拉手续费率。syms 为空则取账户级首条。
func (a *Adapter) FetchTradingCostProfile(ctx context.Context, syms []venue.Symbol) (venue.CostProfile, error) {
	profile := venue.CostProfile{}
	linQ := url.Values{}
	linQ.Set("category", categoryLinear)
	if len(syms) > 0 {
		if raw, _ := a.ToVenue(syms[0], venue.MarketPerp); raw != "" {
			linQ.Set("symbol", raw)
		}
	}
	if taker, maker, err := a.fetchFeeRate(ctx, linQ); err == nil {
		profile.FuturesTaker = taker
		profile.FuturesMaker = maker
	} else {
		return profile, err
	}
	// 现货费率尽力而为(失败不致命)。
	spotQ := url.Values{}
	spotQ.Set("category", "spot")
	if len(syms) > 0 {
		if raw, _ := a.ToVenue(syms[0], venue.MarketSpot); raw != "" {
			spotQ.Set("symbol", raw)
		}
	}
	if taker, maker, err := a.fetchFeeRate(ctx, spotQ); err == nil {
		profile.SpotTaker = taker
		profile.SpotMaker = maker
	}
	return profile, nil
}

// fetchFeeRate 返回某 category(可带 symbol)的首条 taker/maker 费率。
func (a *Adapter) fetchFeeRate(ctx context.Context, q url.Values) (taker, maker float64, err error) {
	var res struct {
		List []struct {
			Symbol        string `json:"symbol"`
			TakerFeeRate  string `json:"takerFeeRate"`
			MakerFeeRate  string `json:"makerFeeRate"`
		} `json:"list"`
	}
	if e := a.t.signedGet(ctx, pathFeeRate, q, &res); e != nil {
		return 0, 0, e
	}
	if len(res.List) == 0 {
		return 0, 0, nil
	}
	return parseFloat(res.List[0].TakerFeeRate), parseFloat(res.List[0].MakerFeeRate), nil
}

// MaxBorrowable:Stage 1 未接负 carry(Capabilities.SupportsNegativeCarry=false),返回 ErrUnsupported。
func (a *Adapter) MaxBorrowable(ctx context.Context, asset string) (float64, error) {
	return 0, venue.ErrUnsupported // TODO(Stage3)
}

// FetchLeverageBrackets 拉 risk-limit 档位(公共接口,经签名通道也可)。
func (a *Adapter) FetchLeverageBrackets(ctx context.Context, s venue.Symbol) ([]venue.LeverageBracket, error) {
	raw, _ := a.ToVenue(s, venue.MarketPerp)
	q := url.Values{}
	q.Set("category", categoryLinear)
	q.Set("symbol", raw)
	var res struct {
		List []struct {
			RiskLimitValue    string `json:"riskLimitValue"`
			MaintenanceMargin string `json:"maintenanceMargin"`
			MaxLeverage       string `json:"maxLeverage"`
		} `json:"list"`
	}
	if err := a.t.publicGet(ctx, "/v5/market/risk-limit", q, &res); err != nil {
		return nil, err
	}
	out := make([]venue.LeverageBracket, 0, len(res.List))
	for _, b := range res.List {
		out = append(out, venue.LeverageBracket{
			NotionalCap:     parseFloat(b.RiskLimitValue),
			MaxLeverage:     int(parseFloat(b.MaxLeverage)),
			MaintMarginRate: parseFloat(b.MaintenanceMargin),
		})
	}
	return out, nil
}

// VerifyAPIKeyPosture 拉 /v5/user/query-api,核对交易/提现权限与 IP 白名单(套利 key 期望:无提现、IP 受限)。
func (a *Adapter) VerifyAPIKeyPosture(ctx context.Context) (venue.KeyPosture, error) {
	var res struct {
		ReadOnly    int      `json:"readOnly"` // 1=只读
		IPs         []string `json:"ips"`
		Permissions struct {
			Wallet        []string `json:"Wallet"`
			Spot          []string `json:"Spot"`
			ContractTrade []string `json:"ContractTrade"`
			Derivatives   []string `json:"Derivatives"`
			Options       []string `json:"Options"`
		} `json:"permissions"`
	}
	if err := a.t.signedGet(ctx, pathQueryAPI, nil, &res); err != nil {
		return venue.KeyPosture{}, err
	}
	canTrade := res.ReadOnly == 0 && (len(res.Permissions.ContractTrade) > 0 ||
		len(res.Permissions.Spot) > 0 || len(res.Permissions.Derivatives) > 0)
	canWithdraw := false
	canTransfer := false
	for _, w := range res.Permissions.Wallet {
		lw := strings.ToLower(w)
		if strings.Contains(lw, "withdraw") {
			canWithdraw = true
		}
		if strings.Contains(lw, "transfer") { // AccountTransfer / SubMemberTransfer
			canTransfer = true
		}
	}
	ipRestricted := false
	for _, ip := range res.IPs {
		if strings.TrimSpace(ip) != "" && ip != "*" {
			ipRestricted = true
		}
	}
	notes := fmt.Sprintf("bybit api key posture; canTransfer=%t; wallet=%v", canTransfer, res.Permissions.Wallet)
	if res.ReadOnly == 1 {
		notes = "read-only key"
	}
	return venue.KeyPosture{
		CanTrade:     canTrade,
		CanWithdraw:  canWithdraw,
		IPRestricted: ipRestricted,
		Notes:        notes,
	}, nil
}
