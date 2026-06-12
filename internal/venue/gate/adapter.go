// Package gate 是 Gate.io(USDT 永续 + 现货)的 venue.Venue 适配器。
//
// 实现 v4 签名传输 + 公共行情 + 私有账户/交易 + 原生 dead-man(countdown_cancel_all)。
// 永续按「张」下单(size 带符号整数),PlaceOrder 内做币数量→张数换算;现货市价买单用 IOC 限价规避 Gate 市价买按 USDT 计量的坑。
package gate

import (
	"context"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
)

// VenueName 是本适配器的注册名。
const VenueName = "gate"

// Adapter 实现 venue.Venue,内核是自包含的 Gate v4 REST transport。
type Adapter struct {
	t *transport

	// instruments 元数据进程级缓存(lookupInstrument):下单/盘口换算热路径免逐次拉公网。
	instMu    sync.RWMutex
	instCache map[string]instCacheEntry
}

// 编译期断言:Adapter 满足完整 venue.Venue 接口。
var _ venue.Venue = (*Adapter)(nil)

// New 用中性 venue.Config 构造 Gate 适配器(只需 api/secret,无 passphrase)。
func New(cfg venue.Config) (*Adapter, error) {
	base := ""
	if cfg.Extra != nil {
		base = cfg.Extra["base_url"]
	}
	return &Adapter{t: newTransport(base, cfg.APIKey, cfg.APISecret)}, nil
}

// Factory 是 venue.Registry 登记用的工厂函数(「加一家 = 注册一行」)。
func Factory(cfg venue.Config) (venue.Venue, error) {
	return New(cfg)
}

// Name 返回 "gate"。
func (a *Adapter) Name() string { return VenueName }

// SyncServerTime 同步 Gate 服务器时间偏移到签名时钟,返回「服务器 − 本地」偏移。
func (a *Adapter) SyncServerTime(ctx context.Context) (time.Duration, error) {
	return a.t.syncServerTime(ctx)
}

// Capabilities 声明 Gate 能力;引擎按此裁剪行为。
func (a *Adapter) Capabilities() venue.Capabilities {
	return venue.Capabilities{
		SupportsNegativeCarry:    false,          // Gate 借币做空路径未接入,负 carry 暂不支持
		FundingIntervalsHours:    []int{8, 4, 1}, // contracts 端点 funding_interval 常见 28800/14400/3600 秒
		HasNativeDeadMan:         true,           // countdown_cancel_all 散户可用(与 Bybit set-dcp 不同)
		HasFeeDiscountToken:      false,          // GT 抵扣未计入实拉费率画像
		MaxBatchOrders:           1,              // PlaceBatch 未实现,单笔 PlaceOrder 可用
		ContractBasedPerp:        true,           // USDT 永续 size 为带符号整数张数,需 quanto_multiplier 换算
		SupportsCapitalRebalance: true,           // capital.go:spot↔futures 所内划转
		CapitalWallets:           []venue.Wallet{WalletSpot, WalletFutures},
	}
}

// Classify 把 Gate label 粗分类(够 Safety 重试判定用)。
func (a *Adapter) Classify(err error) (category string, severity string) {
	if err == nil {
		return "", ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline") || strings.Contains(msg, "timeout"):
		return "request_timeout", "minor"
	case strings.Contains(msg, "label=INVALID_SIGNATURE") || strings.Contains(msg, "label=INVALID_KEY"):
		return "api_key_error", "critical"
	case strings.Contains(msg, "label=TOO_MANY_REQUESTS"):
		return "order_rate_limit", "major"
	case strings.Contains(msg, "label=BALANCE_NOT_ENOUGH") || strings.Contains(msg, "label=INSUFFICIENT_AVAILABLE"):
		return "platform_reject", "major"
	default:
		return "request_error", "minor"
	}
}

// ReserveOrderBudget:Gate 按 IP/UID 限频,本地恒放行(与 okx 同口径)。
func (a *Adapter) ReserveOrderBudget(units int) bool { return true }

// ReleaseOrderBudget:与 ReserveOrderBudget 配对,无账本,空实现。
func (a *Adapter) ReleaseOrderBudget(units int) {}

// ArmDeadMansSwitch 启用 Gate countdown_cancel_all:若在 timeout 秒内未续期则自动撤销全部合约挂单。
// countdown 会被夹到 [10s,120s];传 0 则取消倒计时。
func (a *Adapter) ArmDeadMansSwitch(ctx context.Context, countdown time.Duration) error {
	sec := 0
	if countdown > 0 {
		sec = int(countdown.Seconds())
		if sec < 10 {
			sec = 10
		}
		if sec > 120 {
			sec = 120
		}
	}
	body := struct {
		Timeout int `json:"timeout"`
	}{Timeout: sec}
	return a.t.signedPost(ctx, pathFuturesCountdownCancelAll, nil, body, nil)
}
