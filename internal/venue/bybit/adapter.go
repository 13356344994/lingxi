// Package bybit 是 Bybit(USDT 永续)的 venue.Venue 适配器。
//
// Stage 1(只读地基):实现 v5 签名传输 + 公共行情(instruments/funding/bookTicker/depth/OI/serverTime)
// + 私有只读账户(balances/positions/feeRate/leverage/key 自检),可对真实 Bybit API 活体验证,零资金风险。
// 下单/撤单/WS 流/调仓等写动作 Stage 1 一律返回 venue.ErrUnsupported,待 Stage 3 引擎接口化后再实现。
package bybit

import (
	"context"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// VenueName 是本适配器的注册名。
const VenueName = "bybit"

// Adapter 实现 venue.Venue,内核是自包含的 Bybit v5 REST transport。
type Adapter struct {
	t *transport
}

// 编译期断言:Adapter 满足完整 venue.Venue 接口。
var _ venue.Venue = (*Adapter)(nil)

// New 用中性 venue.Config 构造 Bybit 适配器(自建 HTTP transport,不复用币安 client)。
func New(cfg venue.Config) (*Adapter, error) {
	base := ""
	if cfg.Extra != nil {
		base = cfg.Extra["base_url"] // 可选覆盖(测试/区域端点)
	}
	return &Adapter{t: newTransport(base, cfg.APIKey, cfg.APISecret)}, nil
}

// Factory 是 venue.Registry 登记用的工厂函数(「加一家 = 注册一行」)。
func Factory(cfg venue.Config) (venue.Venue, error) {
	return New(cfg)
}

// Name 返回 "bybit"。
func (a *Adapter) Name() string { return VenueName }

// SyncServerTime 同步 Bybit 服务器时间偏移到签名时钟(本地时钟漂移自愈),返回「服务器 − 本地」偏移。
func (a *Adapter) SyncServerTime(ctx context.Context) (time.Duration, error) {
	return a.t.syncServerTime(ctx)
}

// Capabilities 声明 Bybit 能力。Stage 1 保守:负 carry / 调仓 / 原生 Kill-Switch 暂未接,置 false,
// 引擎据此裁剪(负 carry=false → 该所走 50/50 正 carry,§P4)。后续阶段落地后再逐项打开。
func (a *Adapter) Capabilities() venue.Capabilities {
	return venue.Capabilities{
		SupportsNegativeCarry:    false,          // TODO(Stage3+): 统一账户借币做空后置 true
		FundingIntervalsHours:    []int{8, 4, 1}, // Bybit linear 常见 8h,部分 4h/1h(逐币以 instruments fundingInterval 为准)
		HasNativeDeadMan:         false,          // set-dcp 仅机构客户可用(官方文档),散户走启动清扫替代(CancelAllOrders)
		HasFeeDiscountToken:      false,
		MaxBatchOrders:           10,    // v5 linear 批量上限
		ContractBasedPerp:        false, // USDT 永续按币数量下单
		SupportsCapitalRebalance: true,  // capital.go:资金↔统一账户划转
		SupportsBorrowQuote:      true,  // collateral-info + spot-borrow-check 只读探测
		CapitalWallets:           []venue.Wallet{WalletUnified, WalletFunding},
	}
}

// Classify 把 Bybit 错误粗分类(Stage 1 占位:够 Safety 重试判定用,细码表后续补)。
func (a *Adapter) Classify(err error) (category string, severity string) {
	if err == nil {
		return "", ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline") || strings.Contains(msg, "timeout"):
		return "request_timeout", "minor"
	case strings.Contains(msg, "retCode=10003") || strings.Contains(msg, "retCode=10004") || strings.Contains(msg, "API key"):
		return "api_key_error", "critical"
	case strings.Contains(msg, "retCode=10006") || strings.Contains(msg, "rate limit"):
		return "order_rate_limit", "major"
	default:
		return "request_error", "minor"
	}
}

// ReserveOrderBudget:Bybit 按 IP/UID 限频(非币安 used-weight 模型)。Stage 1 只读,本地恒放行,
// 限频细节待 Stage 3 下单路径接入时按 v5 限频头实现。
func (a *Adapter) ReserveOrderBudget(units int) bool { return true }

// ReleaseOrderBudget:与 ReserveOrderBudget 配对,Stage 1 无账本,空实现。
func (a *Adapter) ReleaseOrderBudget(units int) {}

// ArmDeadMansSwitch:Bybit 的 set-dcp(disconnect-cancel-all)官方文档明确**仅机构客户(Ins clients)可用**,
// 散户账户无法启用 → 永久返回 ErrUnsupported(HasNativeDeadMan=false)。
// 散户替代路径:崩溃重启后由 bybit_runtime 启动清扫(CancelAllOrders)撤残留挂单,窗口 = 重启延迟。
func (a *Adapter) ArmDeadMansSwitch(ctx context.Context, countdown time.Duration) error {
	return venue.ErrUnsupported
}
