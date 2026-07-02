package app

// 本文件收纳原先散落在 carry/轮动文件中、但被方向引擎与应用装配长期依赖的通用助手。
// carry/五策略轮动代码删除后,这些符号在此单独存活。

import (
	"fmt"
	"log"
	"strings"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// allTradingVenueNames 全部支持的交易所(账户级服务按此集合覆盖,不做策略性剔除)。
var allTradingVenueNames = []string{binance.VenueName, okx.VenueName, gate.VenueName, bybit.VenueName}

// beijingCST 固定 UTC+8,不依赖服务器本地时区(生产机为 UTC)。
var beijingCST = time.FixedZone("CST", 8*3600)

// beijingCSTDayStart 返回 now 所在北京时间自然日的 00:00(带 +08:00 偏移,可转 UTC 比较)。
func beijingCSTDayStart(now time.Time) time.Time {
	cst := now.In(beijingCST)
	y, m, d := cst.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, beijingCST)
}

// 方向引擎 maker/taker 腿确认轮询节奏(原 cross_venue_open.go)。
const crossVenueConfirmTimeout = 8 * time.Second
const crossVenueConfirmPoll = 400 * time.Millisecond

// crossVenueLegClientID 生成腿级幂等单号(截尾保证 ≤36 字符,兼容各所 clientOrderID 长度限制)。
func crossVenueLegClientID(prefix, leg string) string {
	id := prefix + "-" + leg
	if len(id) > 36 {
		id = id[len(id)-36:]
	}
	return id
}

// crossVenueAdapterFromOrchestrator 从编排器取某所的 venue 适配器(未注册返回 nil)。
func crossVenueAdapterFromOrchestrator(orch *Orchestrator, venueName string) venue.Venue {
	if orch == nil {
		return nil
	}
	rt, ok := orch.Get(venueName)
	if !ok || rt == nil || rt.venueAdapter == nil {
		return nil
	}
	return rt.venueAdapter
}

// buildCrossVenueReadOnlyAdapter 按 venue 名加载凭证并构造适配器;凭证缺失则返回 error(调用方降级跳过)。
func buildCrossVenueReadOnlyAdapter(venueName string, cfg EngineConfig, secretPath string) (venue.Venue, error) {
	venueName = strings.ToLower(strings.TrimSpace(venueName))
	switch venueName {
	case binance.VenueName:
		client, err := NewBinanceClientWithNetworkConfig(cfg.NetworkConfig)
		if err != nil {
			return nil, err
		}
		if sec, secErr := LoadVenueSecretConfig(secretPath, binance.VenueName); secErr == nil {
			if authClient, authErr := NewAuthenticatedBinanceClient(sec, cfg.NetworkConfig); authErr == nil {
				client = authClient
			}
		}
		return binance.New(client), nil
	case bybit.VenueName:
		sec, err := LoadVenueSecretConfig(secretPath, bybit.VenueName)
		if err != nil {
			return nil, err
		}
		return bybit.New(venue.Config{
			Name:      bybit.VenueName,
			APIKey:    sec.APIKey,
			APISecret: sec.Secret,
			ProxyURL:  sec.ProxyURL,
		})
	case okx.VenueName, gate.VenueName:
		return buildRegisteredVenueAdapter(venueName, secretPath)
	default:
		return nil, fmt.Errorf("venue %q 未登记适配器", venueName)
	}
}

// buildAccountVenueAdapters 构造账户级服务(权益采样/充提核算/账户风控)用的全所适配器集合。
// 与旧 carry 版不同:**不剔除方向交易专用所**——账户级权益必须覆盖全部四所,
// 否则主战场(如 OKX)的资金变化不进权益曲线,账户风控形同虚设。
func buildAccountVenueAdapters(orch *Orchestrator, cfg EngineConfig, secretPath string, logger *log.Logger) map[string]venue.Venue {
	out := make(map[string]venue.Venue, len(allTradingVenueNames))
	for _, name := range allTradingVenueNames {
		if adp := crossVenueAdapterFromOrchestrator(orch, name); adp != nil {
			out[name] = adp
			continue
		}
		adp, err := buildCrossVenueReadOnlyAdapter(name, cfg, secretPath)
		if err != nil {
			if logger != nil {
				logger.Printf("[账户服务] venue=%s 适配器构造失败,跳过: %v", name, err)
			}
			continue
		}
		out[name] = adp
	}
	return out
}
