package okx

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"lingxi/internal/venue"
)

// account.go 实现 OKX v5 私有只读账户(签名 GET):余额/持仓/费率。

// FetchBalances 拉交易账户余额并归一为中性 Balances。
func (a *Adapter) FetchBalances(ctx context.Context) (venue.Balances, error) {
	var rows []struct {
		Details []struct {
			Ccy       string `json:"ccy"`
			CashBal   string `json:"cashBal"`
			AvailBal  string `json:"availBal"`
			FrozenBal string `json:"frozenBal"`
			EqUsd     string `json:"eqUsd"`
		} `json:"details"`
	}
	if err := a.t.signedGet(ctx, pathBalance, nil, &rows); err != nil {
		return venue.Balances{}, err
	}
	bal := venue.Balances{ByAsset: make(map[string]venue.Balance)}
	for _, acct := range rows {
		for _, c := range acct.Details {
			asset := strings.ToUpper(c.Ccy)
			free := parseFloat(c.AvailBal)
			locked := parseFloat(c.FrozenBal)
			if free == 0 && locked == 0 {
				free = parseFloat(c.CashBal) - locked
			}
			bal.ByAsset[asset] = venue.Balance{
				Asset:     asset,
				Free:      free,
				Locked:    locked,
				WalletUSD: parseFloat(c.EqUsd),
			}
		}
	}
	return bal, nil
}

// FetchPositions 拉 SWAP 持仓,pos 为张数,翻译为币数量(pos × ctVal)。
func (a *Adapter) FetchPositions(ctx context.Context) ([]venue.Position, error) {
	q := url.Values{}
	q.Set("instType", instTypeSwap)
	var rows []struct {
		InstID  string `json:"instId"`
		Pos     string `json:"pos"`
		AvgPx   string `json:"avgPx"`
		MarkPx  string `json:"markPx"`
		Upl     string `json:"upl"`
		Lever   string `json:"lever"`
		MgnMode string `json:"mgnMode"`
		PosSide string `json:"posSide"`
	}
	if err := a.t.signedGet(ctx, pathPositions, q, &rows); err != nil {
		return nil, err
	}
	out := make([]venue.Position, 0, len(rows))
	for _, p := range rows {
		contracts := parseFloat(p.Pos)
		if contracts == 0 {
			continue
		}
		canonical, _ := a.ToCanonical(p.InstID, venue.MarketPerp)
		inst, _ := a.lookupInstrument(ctx, venue.MarketPerp, canonical)
		ctVal := inst.ContractSize
		if ctVal <= 0 {
			ctVal = 1
		}
		size := contracts * ctVal
		if strings.EqualFold(p.PosSide, "short") || contracts < 0 {
			size = -mathAbs(size)
		}
		mode := venue.MarginModeCross
		if strings.EqualFold(p.MgnMode, "isolated") {
			mode = venue.MarginModeIsolated
		}
		out = append(out, venue.Position{
			Symbol:        canonical,
			PositionAmt:   size,
			EntryPrice:    parseFloat(p.AvgPx),
			MarkPrice:     parseFloat(p.MarkPx),
			UnrealizedPnL: parseFloat(p.Upl),
			Leverage:      int(parseFloat(p.Lever)),
			MarginMode:    mode,
		})
	}
	return out, nil
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// FetchTradingCostProfile 拉 SWAP(+SPOT 尽力)实拉手续费率。syms 为空则取账户级首条。
func (a *Adapter) FetchTradingCostProfile(ctx context.Context, syms []venue.Symbol) (venue.CostProfile, error) {
	profile := venue.CostProfile{}
	swapQ := url.Values{}
	swapQ.Set("instType", instTypeSwap)
	if len(syms) > 0 {
		if raw, _ := a.ToVenue(syms[0], venue.MarketPerp); raw != "" {
			swapQ.Set("instId", raw)
		}
	}
	if taker, maker, err := a.fetchTradeFee(ctx, swapQ); err == nil {
		profile.FuturesTaker = taker
		profile.FuturesMaker = maker
	} else {
		return profile, err
	}
	spotQ := url.Values{}
	spotQ.Set("instType", instTypeSpot)
	if len(syms) > 0 {
		if raw, _ := a.ToVenue(syms[0], venue.MarketSpot); raw != "" {
			spotQ.Set("instId", raw)
		}
	}
	if taker, maker, err := a.fetchTradeFee(ctx, spotQ); err == nil {
		profile.SpotTaker = taker
		profile.SpotMaker = maker
	}
	return profile, nil
}

func (a *Adapter) fetchTradeFee(ctx context.Context, q url.Values) (taker, maker float64, err error) {
	var rows []struct {
		Taker string `json:"taker"`
		Maker string `json:"maker"`
	}
	if e := a.t.signedGet(ctx, pathTradeFee, q, &rows); e != nil {
		return 0, 0, e
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	return parseFloat(rows[0].Taker), parseFloat(rows[0].Maker), nil
}

// MaxBorrowable OKX 借币做空路径未接入。
func (a *Adapter) MaxBorrowable(ctx context.Context, asset string) (float64, error) {
	return 0, venue.ErrUnsupported
}

// FetchBorrowQuote 拉市场借币行情(只读):GET /api/v5/account/interest-limits?type=2&ccy=X
// rate 字段为日利率,HourlyRate=rate/24;surplusLmt 为剩余可借额度(base 单位)。
func (a *Adapter) FetchBorrowQuote(ctx context.Context, asset string) (venue.BorrowQuote, error) {
	base := strings.ToUpper(strings.TrimSpace(asset))
	if base == "" {
		return venue.BorrowQuote{}, fmt.Errorf("okx FetchBorrowQuote: asset 为空")
	}
	q := url.Values{}
	q.Set("type", "2")
	q.Set("ccy", base)
	var rows []struct {
		Records []struct {
			Ccy        string `json:"ccy"`
			Rate       string `json:"rate"`
			SurplusLmt string `json:"surplusLmt"`
		} `json:"records"`
	}
	if err := a.t.signedGet(ctx, pathInterestLimits, q, &rows); err != nil {
		return venue.BorrowQuote{}, err
	}
	if len(rows) == 0 || len(rows[0].Records) == 0 {
		return venue.BorrowQuote{Asset: base}, nil
	}
	rec := rows[0].Records[0]
	maxBorrow := parseFloat(rec.SurplusLmt)
	dailyRate := parseFloat(rec.Rate)
	return venue.BorrowQuote{
		Asset:         base,
		MaxBorrowable: maxBorrow,
		HourlyRate:    dailyRate / 24.0,
		Available:     maxBorrow > 0,
	}, nil
}

// FetchLeverageBrackets OKX 杠杆档位公共接口未接入本适配器。
func (a *Adapter) FetchLeverageBrackets(ctx context.Context, s venue.Symbol) ([]venue.LeverageBracket, error) {
	return nil, venue.ErrUnsupported
}

// VerifyAPIKeyPosture 拉 /api/v5/account/config 做 key 权限自检。
// perm 是逗号分隔的权限组合(如 "read_only,trade" / 含 "withdraw");带提现权限的套利 key
// 是高危配置,必须如实上报而非硬编码 false 盖住真实风险。ip 为绑定的白名单(空串=未绑定)。
func (a *Adapter) VerifyAPIKeyPosture(ctx context.Context) (venue.KeyPosture, error) {
	var rows []struct {
		AcctLv  string `json:"acctLv"`
		PosMode string `json:"posMode"`
		Perm    string `json:"perm"`
		IP      string `json:"ip"`
	}
	if err := a.t.signedGet(ctx, pathAccountConfig, nil, &rows); err != nil {
		return venue.KeyPosture{}, err
	}
	if len(rows) == 0 {
		return venue.KeyPosture{}, fmt.Errorf("okx: account config 响应为空")
	}
	r := rows[0]
	canTrade, canWithdraw := false, false
	for _, p := range strings.Split(r.Perm, ",") {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "trade":
			canTrade = true
		case "withdraw":
			canWithdraw = true
		}
	}
	return venue.KeyPosture{
		CanTrade:     canTrade,
		CanWithdraw:  canWithdraw,
		IPRestricted: strings.TrimSpace(r.IP) != "",
		Notes:        fmt.Sprintf("okx acctLv=%s posMode=%s perm=%q ip=%q", r.AcctLv, r.PosMode, r.Perm, r.IP),
	}, nil
}
