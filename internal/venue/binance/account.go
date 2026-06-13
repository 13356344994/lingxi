package binance

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strings"

	"lingxi/internal/venue"
)

const equityDustUSDT = 0.5

var binanceEquityStableCoins = map[string]struct{}{
	"USDT": {}, "USDC": {}, "FDUSD": {}, "DAI": {}, "TUSD": {}, "USDE": {},
}

func binanceStableCoinTotal(asset, free, locked string) float64 {
	name := strings.ToUpper(strings.TrimSpace(asset))
	if name == "" {
		return 0
	}
	if _, ok := binanceEquityStableCoins[name]; !ok {
		return 0
	}
	return parseFloat(free) + parseFloat(locked)
}

func bookTickerMidUSDT(bt venue.BookTicker) (float64, bool) {
	if bt.BidPrice > 0 && bt.AskPrice > 0 {
		return (bt.BidPrice + bt.AskPrice) / 2, true
	}
	if bt.BidPrice > 0 {
		return bt.BidPrice, true
	}
	if bt.AskPrice > 0 {
		return bt.AskPrice, true
	}
	return 0, false
}

func (a *Adapter) equitySpotMidUSDT(ctx context.Context, coin string) (float64, bool) {
	coin = strings.ToUpper(strings.TrimSpace(coin))
	if coin == "" {
		return 0, false
	}
	bt, err := a.FetchBookTicker(ctx, venue.MarketSpot, venue.Symbol(coin+"-USDT"))
	if err != nil {
		slog.Debug("binance equity: spot mid unavailable", "coin", coin, "err", err)
		return 0, false
	}
	if mid, ok := bookTickerMidUSDT(bt); ok {
		return mid, true
	}
	slog.Debug("binance equity: spot mid empty book", "coin", coin)
	return 0, false
}

func (a *Adapter) addNonStableEquityUSDT(ctx context.Context, total *float64, coin string, qty float64) {
	if qty == 0 {
		return
	}
	name := strings.ToUpper(strings.TrimSpace(coin))
	if name == "" {
		return
	}
	if _, ok := binanceEquityStableCoins[name]; ok {
		return
	}
	price, ok := a.equitySpotMidUSDT(ctx, name)
	if !ok {
		return
	}
	value := qty * price
	if math.Abs(value) < equityDustUSDT {
		return
	}
	*total += value
}

// FetchEquityUSDT 拉账户总权益(USDT):U 本位 totalMarginBalance(已含合约保证金折算)
// + 现货/全仓杠杆/资金账户稳定币 + 非稳定币按现货 mid 折算(best-effort)。
func (a *Adapter) FetchEquityUSDT(ctx context.Context) (float64, error) {
	var total float64
	var okCount int
	var lastErr error

	var futAcct struct {
		TotalMarginBalance string `json:"totalMarginBalance"`
	}
	if err := a.signedGetJSON(ctx, a.futuresBaseURL()+PathFuturesAccountV2, nil, &futAcct); err != nil {
		lastErr = err
	} else {
		total += parseFloat(futAcct.TotalMarginBalance)
		okCount++
	}

	var spot struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := a.signedGetJSON(ctx, a.spotBaseURL()+PathSpotAccount, nil, &spot); err != nil {
		if lastErr == nil {
			lastErr = err
		}
	} else {
		for _, row := range spot.Balances {
			total += binanceStableCoinTotal(row.Asset, row.Free, row.Locked)
			a.addNonStableEquityUSDT(ctx, &total, row.Asset, parseFloat(row.Free)+parseFloat(row.Locked))
		}
		okCount++
	}

	var margin struct {
		UserAssets []struct {
			Asset    string `json:"asset"`
			NetAsset string `json:"netAsset"`
		} `json:"userAssets"`
	}
	if err := a.signedGetJSON(ctx, a.spotBaseURL()+PathMarginAccount, nil, &margin); err != nil {
		if lastErr == nil {
			lastErr = err
		}
	} else {
		for _, row := range margin.UserAssets {
			name := strings.ToUpper(strings.TrimSpace(row.Asset))
			net := parseFloat(row.NetAsset)
			if _, ok := binanceEquityStableCoins[name]; ok {
				total += net
				continue
			}
			a.addNonStableEquityUSDT(ctx, &total, name, net)
		}
		okCount++
	}

	var funding []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
	}
	if err := a.signedPostForm(ctx, a.spotBaseURL()+PathAssetFundingAsset, nil, &funding); err != nil {
		if lastErr == nil {
			lastErr = err
		}
	} else {
		for _, row := range funding {
			total += binanceStableCoinTotal(row.Asset, row.Free, row.Locked)
			a.addNonStableEquityUSDT(ctx, &total, row.Asset, parseFloat(row.Free)+parseFloat(row.Locked))
		}
		okCount++
	}

	if okCount == 0 {
		return 0, lastErr
	}
	return total, nil
}

// FetchBalances 拉 USDT 永续钱包(fapi/v2/balance)与现货账户(/api/v3/account)并合并。
//
// 口径:同一 asset 的 Free/Locked 跨市场相加(现货 free/locked + 合约 availableBalance/locked 等),
// 反映该 API Key 可见的「现货+U 本位永续」总可用/冻结;不含资金账户/全仓杠杆/理财等其它钱包。
func (a *Adapter) FetchBalances(ctx context.Context) (venue.Balances, error) {
	bal := venue.Balances{ByAsset: make(map[string]venue.Balance)}

	var futures []struct {
		Asset              string `json:"asset"`
		Balance            string `json:"balance"`
		AvailableBalance   string `json:"availableBalance"`
		CrossWalletBalance string `json:"crossWalletBalance"`
	}
	if err := a.signedGetJSON(ctx, a.futuresBaseURL()+PathFuturesBalance, nil, &futures); err != nil {
		return venue.Balances{}, err
	}
	for _, row := range futures {
		asset := strings.ToUpper(strings.TrimSpace(row.Asset))
		if asset == "" {
			continue
		}
		wallet := parseFloat(row.Balance)
		if wallet == 0 {
			wallet = parseFloat(row.CrossWalletBalance)
		}
		free := parseFloat(row.AvailableBalance)
		locked := wallet - free
		if locked < 0 {
			locked = 0
		}
		mergeBalance(bal.ByAsset, asset, free, locked, 0)
	}

	var spot struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := a.signedGetJSON(ctx, a.spotBaseURL()+PathSpotAccount, nil, &spot); err != nil {
		return venue.Balances{}, err
	}
	for _, row := range spot.Balances {
		asset := strings.ToUpper(strings.TrimSpace(row.Asset))
		if asset == "" {
			continue
		}
		free := parseFloat(row.Free)
		locked := parseFloat(row.Locked)
		if free == 0 && locked == 0 {
			continue
		}
		mergeBalance(bal.ByAsset, asset, free, locked, 0)
	}
	return bal, nil
}

func mergeBalance(byAsset map[string]venue.Balance, asset string, free, locked, walletUSD float64) {
	prev := byAsset[asset]
	byAsset[asset] = venue.Balance{
		Asset:     asset,
		Free:      prev.Free + free,
		Locked:    prev.Locked + locked,
		WalletUSD: prev.WalletUSD + walletUSD,
	}
}

// FetchPositions 拉 U 本位永续持仓(positionRisk),positionAmt 带符号(正=多,负=空),过滤零仓。
func (a *Adapter) FetchPositions(ctx context.Context) ([]venue.Position, error) {
	var rows []struct {
		Symbol           string `json:"symbol"`
		PositionAmt      string `json:"positionAmt"`
		EntryPrice       string `json:"entryPrice"`
		MarkPrice        string `json:"markPrice"`
		UnRealizedProfit string `json:"unRealizedProfit"`
		Leverage         string `json:"leverage"`
		MarginType       string `json:"marginType"`
	}
	if err := a.signedGetJSON(ctx, a.futuresBaseURL()+PathFuturesPositionRisk, nil, &rows); err != nil {
		return nil, err
	}
	out := make([]venue.Position, 0, len(rows))
	for _, p := range rows {
		amt := parseFloat(p.PositionAmt)
		if amt == 0 {
			continue
		}
		mode := venue.MarginModeCross
		if strings.EqualFold(strings.TrimSpace(p.MarginType), "isolated") {
			mode = venue.MarginModeIsolated
		}
		canonical, _ := a.ToCanonical(p.Symbol, venue.MarketPerp)
		out = append(out, venue.Position{
			Symbol:        canonical,
			PositionAmt:   amt,
			EntryPrice:    parseFloat(p.EntryPrice),
			MarkPrice:     parseFloat(p.MarkPrice),
			UnrealizedPnL: parseFloat(p.UnRealizedProfit),
			Leverage:      int(parseFloat(p.Leverage)),
			MarginMode:    mode,
		})
	}
	return out, nil
}

// VerifyAPIKeyPosture 拉 /sapi/v1/account/apiRestrictions,映射交易/提现/IP 白名单姿态。
func (a *Adapter) VerifyAPIKeyPosture(ctx context.Context) (venue.KeyPosture, error) {
	var payload struct {
		IPRestrict                 bool `json:"ipRestrict"`
		EnableWithdrawals          bool `json:"enableWithdrawals"`
		EnableReading              bool `json:"enableReading"`
		EnableSpotAndMarginTrading bool `json:"enableSpotAndMarginTrading"`
		EnableFutures              bool `json:"enableFutures"`
	}
	if err := a.signedGetJSON(ctx, a.spotBaseURL()+PathAPIRestrictions, nil, &payload); err != nil {
		return venue.KeyPosture{}, err
	}
	canTrade := payload.EnableSpotAndMarginTrading || payload.EnableFutures
	notes := fmt.Sprintf(
		"binance apiRestrictions: spotMargin=%t futures=%t reading=%t",
		payload.EnableSpotAndMarginTrading, payload.EnableFutures, payload.EnableReading,
	)
	return venue.KeyPosture{
		CanTrade:     canTrade,
		CanWithdraw:  payload.EnableWithdrawals,
		IPRestricted: payload.IPRestrict,
		Notes:        notes,
	}, nil
}

// FetchTradingCostProfile 委托 client.ResolveAccountTradingCostProfile,翻译成中性 CostProfile。
// 币安返回的是账户级真实吃单费率(已反映 VIP 阶梯与 BNB 抵扣),故 DiscountApplied=true。
func (a *Adapter) FetchTradingCostProfile(ctx context.Context, syms []venue.Symbol) (venue.CostProfile, error) {
	spotSymbols := make([]string, 0, len(syms))
	futuresSymbols := make([]string, 0, len(syms))
	for _, s := range syms {
		if spot, err := a.ToVenue(s, venue.MarketSpot); err == nil {
			spotSymbols = append(spotSymbols, spot)
		}
		if perp, err := a.ToVenue(s, venue.MarketPerp); err == nil {
			futuresSymbols = append(futuresSymbols, perp)
		}
	}
	profile, err := a.client.ResolveAccountTradingCostProfile(ctx, spotSymbols, futuresSymbols, nil)
	if err != nil {
		return venue.CostProfile{}, err
	}
	return venue.CostProfile{
		SpotTaker:       profile.SpotTakerFeeRate,
		FuturesTaker:    profile.FuturesTakerFeeRate,
		DiscountApplied: true,
	}, nil
}

// MaxBorrowable 委托 client.MaxBorrowable(负 carry 借币用)。
func (a *Adapter) MaxBorrowable(ctx context.Context, asset string) (float64, error) {
	return a.client.MaxBorrowable(ctx, asset)
}

// FetchBorrowQuote 拉全仓杠杆借币行情(只读):GET /sapi/v1/margin/crossMarginData(vipLevel=0,日利率)
// + GET /sapi/v1/margin/maxBorrowable(可借额度)。HourlyRate=dailyInterest/24。
func (a *Adapter) FetchBorrowQuote(ctx context.Context, asset string) (venue.BorrowQuote, error) {
	base := strings.ToUpper(strings.TrimSpace(asset))
	if base == "" {
		return venue.BorrowQuote{}, fmt.Errorf("binance FetchBorrowQuote: asset 为空")
	}
	q := url.Values{}
	q.Set("vipLevel", "0")
	q.Set("coin", base)
	var feeRows []struct {
		Coin          string `json:"coin"`
		Borrowable    bool   `json:"borrowable"`
		DailyInterest string `json:"dailyInterest"`
	}
	if err := a.signedGetJSON(ctx, a.spotBaseURL()+PathMarginCrossMarginData, q, &feeRows); err != nil {
		return venue.BorrowQuote{}, err
	}
	borrowable := false
	dailyRate := 0.0
	for _, row := range feeRows {
		if strings.EqualFold(row.Coin, base) {
			borrowable = row.Borrowable
			dailyRate = parseFloat(row.DailyInterest)
			break
		}
	}
	mq := url.Values{}
	mq.Set("asset", base)
	mq.Set("isIsolated", "FALSE")
	var maxRow struct {
		Amount string `json:"amount"`
	}
	if err := a.signedGetJSON(ctx, a.spotBaseURL()+PathMarginMaxBorrowable, mq, &maxRow); err != nil {
		return venue.BorrowQuote{}, err
	}
	maxBorrow := parseFloat(maxRow.Amount)
	return venue.BorrowQuote{
		Asset:         base,
		MaxBorrowable: maxBorrow,
		HourlyRate:    dailyRate / 24.0,
		Available:     borrowable && maxBorrow > 0,
	}, nil
}

// FetchLeverageBrackets 尚未下沉至本包,由 app 层 engine_risk_util 直连;跨所引擎暂不需要。
func (a *Adapter) FetchLeverageBrackets(ctx context.Context, s venue.Symbol) ([]venue.LeverageBracket, error) {
	return nil, venue.ErrUnsupported
}
