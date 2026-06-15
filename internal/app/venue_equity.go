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

type venueEquityCacheEntry struct {
	equityUSDT float64
	fetchedAt  time.Time
	err        string
}

// equityUSDTFromBalancesAndPositions 按面板口径汇总账户总权益:
// USDT 资产 = Free+Locked(含占用保证金);非 USDT = WalletUSD(已折美元);再加持仓浮盈亏 Σ UnrealizedPnL。
func equityUSDTFromBalancesAndPositions(bal venue.Balances, positions []venue.Position) float64 {
	var total float64
	for asset, b := range bal.ByAsset {
		if strings.EqualFold(asset, "USDT") {
			total = money.SumUSDT(total, b.Free+b.Locked)
		} else {
			total = money.SumUSDT(total, b.WalletUSD)
		}
	}
	for _, p := range positions {
		total = money.SumUSDT(total, p.UnrealizedPnL)
	}
	return total
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

func (s *DashboardServer) storeVenueEquityCache(name string, equity float64, err error) {
	if s == nil || name == "" {
		return
	}
	ent := venueEquityCacheEntry{equityUSDT: equity, fetchedAt: time.Now()}
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

func (s *DashboardServer) fetchVenueEquityUSDT(ctx context.Context, rt *venueRuntime) (float64, error) {
	if rt == nil || rt.name == "" {
		return 0, fmt.Errorf("venue 未指定")
	}
	if ent, ok := s.loadVenueEquityCache(rt.name); ok {
		if ent.err != "" {
			return 0, fmt.Errorf("%s", ent.err)
		}
		return ent.equityUSDT, nil
	}

	adp := s.venueAccountAdapter(rt)
	if adp == nil {
		err := fmt.Errorf("venue %q 账户适配器未就绪", rt.name)
		s.storeVenueEquityCache(rt.name, 0, err)
		return 0, err
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	bal, err := adp.FetchBalances(cctx)
	if err != nil {
		s.storeVenueEquityCache(rt.name, 0, err)
		return 0, err
	}
	positions, err := adp.FetchPositions(cctx)
	if err != nil {
		s.storeVenueEquityCache(rt.name, 0, err)
		return 0, err
	}
	equity := equityUSDTFromBalancesAndPositions(bal, positions)
	s.storeVenueEquityCache(rt.name, equity, nil)
	return equity, nil
}
