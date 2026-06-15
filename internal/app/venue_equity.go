package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
)

const venueEquityCacheTTL = 20 * time.Second

type venueEquitySnapshot struct {
	WalletUSDT        float64
	UnrealizedPnLUSDT float64
}

type venueEquityCacheEntry struct {
	snapshot  venueEquitySnapshot
	fetchedAt time.Time
	err       string
}

// walletUSDTFromBalances 面板「全部资金(本金)」口径:
// USDT 资产 = Free+Locked(含占用保证金);非 USDT = WalletUSD(已折美元);不含持仓浮盈亏。
func walletUSDTFromBalances(bal venue.Balances) float64 {
	var total float64
	for asset, b := range bal.ByAsset {
		if strings.EqualFold(asset, "USDT") {
			total = money.SumUSDT(total, b.Free+b.Locked)
		} else {
			total = money.SumUSDT(total, b.WalletUSD)
		}
	}
	return total
}

func unrealizedPnLUSDTFromPositions(positions []venue.Position) float64 {
	var total float64
	for _, p := range positions {
		total = money.SumUSDT(total, p.UnrealizedPnL)
	}
	return total
}

func venueEquityFromBalancesAndPositions(bal venue.Balances, positions []venue.Position) venueEquitySnapshot {
	return venueEquitySnapshot{
		WalletUSDT:        walletUSDTFromBalances(bal),
		UnrealizedPnLUSDT: unrealizedPnLUSDTFromPositions(positions),
	}
}

// venueAccountAdapter 复用编排器/跨所执行器已注册的账户适配器,避免面板侧重复构造(尤其 binance)。
func (s *DashboardServer) venueAccountAdapter(rt *venueRuntime) venue.AccountData {
	if rt == nil || rt.name == "" {
		return nil
	}
	if s != nil && s.crossVenueExecutor != nil {
		if adp := s.crossVenueExecutor.venues[rt.name]; adp != nil {
			return adp
		}
	}
	return crossVenueAdapterFromOrchestrator(s.orchestrator, rt.name)
}

func (s *DashboardServer) loadVenueEquityCache(name string) (venueEquityCacheEntry, bool) {
	if s == nil || name == "" {
		return venueEquityCacheEntry{}, false
	}
	s.venueEquityCacheMu.Lock()
	defer s.venueEquityCacheMu.Unlock()
	if s.venueEquityCache == nil {
		return venueEquityCacheEntry{}, false
	}
	ent, ok := s.venueEquityCache[name]
	if !ok || ent.fetchedAt.IsZero() || time.Since(ent.fetchedAt) > venueEquityCacheTTL {
		return venueEquityCacheEntry{}, false
	}
	return ent, true
}

func (s *DashboardServer) storeVenueEquityCache(name string, snap venueEquitySnapshot, err error) {
	if s == nil || name == "" {
		return
	}
	ent := venueEquityCacheEntry{snapshot: snap, fetchedAt: time.Now()}
	if err != nil {
		ent.err = err.Error()
	}
	s.venueEquityCacheMu.Lock()
	defer s.venueEquityCacheMu.Unlock()
	if s.venueEquityCache == nil {
		s.venueEquityCache = make(map[string]venueEquityCacheEntry)
	}
	s.venueEquityCache[name] = ent
}

func (s *DashboardServer) fetchVenueEquitySnapshot(ctx context.Context, rt *venueRuntime) (venueEquitySnapshot, error) {
	if rt == nil || rt.name == "" {
		return venueEquitySnapshot{}, fmt.Errorf("venue 未指定")
	}
	if ent, ok := s.loadVenueEquityCache(rt.name); ok {
		if ent.err != "" {
			return venueEquitySnapshot{}, fmt.Errorf("%s", ent.err)
		}
		return ent.snapshot, nil
	}

	adp := s.venueAccountAdapter(rt)
	if adp == nil {
		err := fmt.Errorf("venue %q 账户适配器未就绪", rt.name)
		s.storeVenueEquityCache(rt.name, venueEquitySnapshot{}, err)
		return venueEquitySnapshot{}, err
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	positions, err := adp.FetchPositions(cctx)
	if err != nil {
		s.storeVenueEquityCache(rt.name, venueEquitySnapshot{}, err)
		return venueEquitySnapshot{}, err
	}
	upnl := unrealizedPnLUSDTFromPositions(positions)

	// 「全部资金(本金)」应覆盖该所**所有钱包**(交易/合约/现货 + 资金账户/资金钱包 + 全仓杠杆等),
	// 不只是「合约+现货」。FetchBalances 是交易口径(只含合约+现货,且被下单/调仓逻辑用于可用余额与
	// 现货可卖量判定),不能塞入不可直接交易的钱包,否则会污染下单 sizing。因此面板改用 FetchEquityUSDT
	// (适配器已覆盖全部钱包:binance 合约+现货+全仓+资金钱包;okx 交易账户+资金账户;bybit 统一+资金;
	// gate 合约+现货),再减去浮盈亏得到「钱包本金」口径,从而把此前漏算的资金账户/资金钱包计入展示。
	// 适配器若未实现 FetchEquityUSDT 或拉取失败,则回退到原 FetchBalances「合约+现货」口径(行为同改造前)。
	if er, ok := adp.(equityReporter); ok {
		if eq, eqErr := er.FetchEquityUSDT(cctx); eqErr == nil {
			snap := venueEquitySnapshot{WalletUSDT: eq - upnl, UnrealizedPnLUSDT: upnl}
			s.storeVenueEquityCache(rt.name, snap, nil)
			return snap, nil
		}
	}

	bal, err := adp.FetchBalances(cctx)
	if err != nil {
		s.storeVenueEquityCache(rt.name, venueEquitySnapshot{}, err)
		return venueEquitySnapshot{}, err
	}
	snap := venueEquitySnapshot{WalletUSDT: walletUSDTFromBalances(bal), UnrealizedPnLUSDT: upnl}
	s.storeVenueEquityCache(rt.name, snap, nil)
	return snap, nil
}
