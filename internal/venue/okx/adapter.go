// Package okx 是 OKX(USDT 永续 + 现货)的 venue.Venue 适配器。
//
// 实现 v5 签名传输 + 公共行情 + 私有账户/交易 + 原生 dead-man(cancel-all-after)。
// 永续按「张」下单,PlaceOrder 内做币数量→张数换算;现货市价买单显式 tgtCcy=base_ccy 与中性 Qty 对齐。
package okx

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
)

// VenueName 是本适配器的注册名。
const VenueName = "okx"

// Adapter 实现 venue.Venue,内核是自包含的 OKX v5 REST transport。
type Adapter struct {
	t *transport

	// instruments 元数据进程级缓存(lookupInstrument):下单/盘口换算热路径免逐次拉公网。
	instMu    sync.RWMutex
	instCache map[string]instCacheEntry
}

// 编译期断言:Adapter 满足完整 venue.Venue 接口。
var _ venue.Venue = (*Adapter)(nil)

// New 用中性 venue.Config 构造 OKX 适配器(自建 HTTP transport,不复用其他所 client)。
func New(cfg venue.Config) (*Adapter, error) {
	base := ""
	if cfg.Extra != nil {
		base = cfg.Extra["base_url"] // 可选覆盖(测试/模拟盘端点)
	}
	return &Adapter{t: newTransport(base, cfg.APIKey, cfg.APISecret, cfg.Passphrase)}, nil
}

// Factory 是 venue.Registry 登记用的工厂函数(「加一家 = 注册一行」)。
func Factory(cfg venue.Config) (venue.Venue, error) {
	return New(cfg)
}

// Name 返回 "okx"。
func (a *Adapter) Name() string { return VenueName }

// SyncServerTime 同步 OKX 服务器时间偏移到签名时钟(本地时钟漂移自愈),返回「服务器 − 本地」偏移。
func (a *Adapter) SyncServerTime(ctx context.Context) (time.Duration, error) {
	return a.t.syncServerTime(ctx)
}

// Capabilities 声明 OKX 能力。引擎按此裁剪行为,而非写死币安假设。
func (a *Adapter) Capabilities() venue.Capabilities {
	return venue.Capabilities{
		SupportsNegativeCarry:    false,          // OKX 统一账户借币做空路径未接入,负 carry 暂不支持
		FundingIntervalsHours:    []int{8, 4, 1}, // OKX 永续常见 8h/4h/1h(逐币以 funding-rate 时间差推导)
		HasNativeDeadMan:         true,           // cancel-all-after 散户可用(与 Bybit set-dcp 不同)
		HasFeeDiscountToken:      false,          // OKB 抵扣未计入实拉费率画像
		MaxBatchOrders:           1,              // PlaceBatch 未实现,单笔 PlaceOrder 可用
		ContractBasedPerp:        true,           // USDT 永续 sz 单位为「张」,需 ctVal 换算
		SupportsCapitalRebalance: true,           // capital.go:funding↔trading 所内划转
		CapitalWallets:           []venue.Wallet{WalletFunding, WalletTrading},
	}
}

// Classify 把 OKX 错误粗分类(够 Safety 重试判定用)。
func (a *Adapter) Classify(err error) (category string, severity string) {
	if err == nil {
		return "", ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline") || strings.Contains(msg, "timeout"):
		return "request_timeout", "minor"
	case strings.Contains(msg, "code=501") || strings.Contains(msg, "API key"):
		return "api_key_error", "critical"
	case strings.Contains(msg, "code=50011") || strings.Contains(msg, "rate limit"):
		return "order_rate_limit", "major"
	case strings.Contains(msg, "code=51008"):
		return "platform_reject", "major" // 余额不足
	case strings.Contains(msg, "code=51121"):
		return "platform_reject", "minor" // 张数精度
	default:
		return "request_error", "minor"
	}
}

// ReserveOrderBudget:OKX 按 IP/UID 限频(非币安 used-weight 模型)。本地恒放行,
// 限频细节待接入 OKX 限频头后按实际规则实现。
func (a *Adapter) ReserveOrderBudget(units int) bool { return true }

// ReleaseOrderBudget:与 ReserveOrderBudget 配对,无账本,空实现。
func (a *Adapter) ReleaseOrderBudget(units int) {}

// ArmDeadMansSwitch 启用 OKX cancel-all-after:若在 timeOut 秒内未续期则自动撤销全部挂单。
// countdown 会被夹到 [10s,120s](OKX 允许范围);传 0 则取消倒计时。
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
		TimeOut string `json:"timeOut"`
	}{TimeOut: strconv.Itoa(sec)}
	return a.t.signedPost(ctx, pathCancelAllAfter, body, nil)
}
