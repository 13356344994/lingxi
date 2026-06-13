package gate

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"lingxi/internal/venue"
)

// account.go 实现 Gate v4 私有只读账户(签名 GET):余额/持仓/费率。

// FetchBalances 合并 USDT 合约账户 + 现货账户余额为中性 Balances。
func (a *Adapter) FetchBalances(ctx context.Context) (venue.Balances, error) {
	bal := venue.Balances{ByAsset: make(map[string]venue.Balance)}

	var futAcct struct {
		Total          string `json:"total"`
		Available      string `json:"available"`
		OrderMargin    string `json:"order_margin"`
		PositionMargin string `json:"position_margin"`
		UnrealisedPnl  string `json:"unrealised_pnl"`
		Currency       string `json:"currency"`
	}
	if err := a.t.signedGet(ctx, pathFuturesAccounts, nil, &futAcct); err == nil {
		asset := strings.ToUpper(strings.TrimSpace(futAcct.Currency))
		if asset == "" {
			asset = "USDT"
		}
		free := parseFloat(futAcct.Available)
		total := parseFloat(futAcct.Total)
		locked := total - free
		if locked < 0 {
			locked = parseFloat(futAcct.OrderMargin) + parseFloat(futAcct.PositionMargin)
		}
		bal.ByAsset[asset] = venue.Balance{
			Asset:     asset,
			Free:      free,
			Locked:    locked,
			WalletUSD: total,
		}
	}

	var spotRows []struct {
		Currency  string `json:"currency"`
		Available string `json:"available"`
		Locked    string `json:"locked"`
	}
	if err := a.t.signedGet(ctx, pathSpotAccounts, nil, &spotRows); err == nil {
		for _, r := range spotRows {
			asset := strings.ToUpper(r.Currency)
			free := parseFloat(r.Available)
			locked := parseFloat(r.Locked)
			existing := bal.ByAsset[asset]
			bal.ByAsset[asset] = venue.Balance{
				Asset:     asset,
				Free:      existing.Free + free,
				Locked:    existing.Locked + locked,
				WalletUSD: existing.WalletUSD + free + locked,
			}
		}
	}
	return bal, nil
}

// FetchPositions 拉 USDT 永续持仓;Gate size 为带符号张数,× quanto_multiplier 换算为币数量。
func (a *Adapter) FetchPositions(ctx context.Context) ([]venue.Position, error) {
	var rows []struct {
		Contract      string `json:"contract"`
		Size          int64  `json:"size"`
		EntryPrice    string `json:"entry_price"`
		MarkPrice     string `json:"mark_price"`
		UnrealisedPnl string `json:"unrealised_pnl"`
		Leverage      string `json:"leverage"`
		Mode          string `json:"mode"`
	}
	if err := a.t.signedGet(ctx, pathFuturesPositions, nil, &rows); err != nil {
		return nil, err
	}
	out := make([]venue.Position, 0, len(rows))
	for _, p := range rows {
		if p.Size == 0 {
			continue
		}
		canonical, _ := a.ToCanonical(p.Contract, venue.MarketPerp)
		inst, _ := a.lookupInstrument(ctx, venue.MarketPerp, canonical)
		mult := inst.ContractSize
		if mult <= 0 {
			mult = 1
		}
		sizeCoin := float64(p.Size) * mult
		mode := venue.MarginModeCross
		if strings.EqualFold(p.Mode, "single") || strings.EqualFold(p.Mode, "isolated") {
			mode = venue.MarginModeIsolated
		}
		out = append(out, venue.Position{
			Symbol:        canonical,
			PositionAmt:   sizeCoin,
			EntryPrice:    parseFloat(p.EntryPrice),
			MarkPrice:     parseFloat(p.MarkPrice),
			UnrealizedPnL: parseFloat(p.UnrealisedPnl),
			Leverage:      int(parseFloat(p.Leverage)),
			MarginMode:    mode,
		})
	}
	return out, nil
}

// FetchTradingCostProfile 拉 /wallet/fee 四个费率字段(现货/合约 maker/taker)。
func (a *Adapter) FetchTradingCostProfile(ctx context.Context, syms []venue.Symbol) (venue.CostProfile, error) {
	var res struct {
		MakerFee        string `json:"maker_fee"`
		TakerFee        string `json:"taker_fee"`
		FuturesMakerFee string `json:"futures_maker_fee"`
		FuturesTakerFee string `json:"futures_taker_fee"`
		GTDiscount      bool   `json:"gt_discount"`
	}
	if err := a.t.signedGet(ctx, pathWalletFee, nil, &res); err != nil {
		return venue.CostProfile{}, err
	}
	return venue.CostProfile{
		SpotTaker:       parseFloat(res.TakerFee),
		SpotMaker:       parseFloat(res.MakerFee),
		FuturesTaker:    parseFloat(res.FuturesTakerFee),
		FuturesMaker:    parseFloat(res.FuturesMakerFee),
		DiscountApplied: res.GTDiscount,
	}, nil
}

// MaxBorrowable Gate 借币做空路径未接入。
func (a *Adapter) MaxBorrowable(ctx context.Context, asset string) (float64, error) {
	return 0, venue.ErrUnsupported
}

// FetchBorrowQuote 拉统一账户借币行情(只读):
// GET /api/v4/unified/borrowable?currency=X(最大可借)
// + GET /api/v4/unified/estimate_rate?currencies=X(预估小时借币利率,官方文档为 hourly rate)。
func (a *Adapter) FetchBorrowQuote(ctx context.Context, asset string) (venue.BorrowQuote, error) {
	base := strings.ToUpper(strings.TrimSpace(asset))
	if base == "" {
		return venue.BorrowQuote{}, fmt.Errorf("gate FetchBorrowQuote: asset 为空")
	}
	bq := url.Values{}
	bq.Set("currency", base)
	var borrowable struct {
		Currency string `json:"currency"`
		Amount   string `json:"amount"`
	}
	if err := a.t.signedGet(ctx, pathUnifiedBorrowable, bq, &borrowable); err != nil {
		return venue.BorrowQuote{}, err
	}
	maxBorrow := parseFloat(borrowable.Amount)
	rq := url.Values{}
	rq.Set("currencies", base)
	var rates map[string]string
	if err := a.t.signedGet(ctx, pathUnifiedEstimateRate, rq, &rates); err != nil {
		return venue.BorrowQuote{}, err
	}
	hourlyRate := parseFloat(rates[base])
	return venue.BorrowQuote{
		Asset:         base,
		MaxBorrowable: maxBorrow,
		HourlyRate:    hourlyRate,
		Available:     maxBorrow > 0,
	}, nil
}

// FetchLeverageBrackets Gate 杠杆档位公共接口未接入本适配器。
func (a *Adapter) FetchLeverageBrackets(ctx context.Context, s venue.Symbol) ([]venue.LeverageBracket, error) {
	return nil, venue.ErrUnsupported
}

// VerifyAPIKeyPosture Gate 无与 Bybit query-api 对等的 key 权限自检端点,返回 ErrUnsupported。
func (a *Adapter) VerifyAPIKeyPosture(ctx context.Context) (venue.KeyPosture, error) {
	return venue.KeyPosture{}, venue.ErrUnsupported
}
