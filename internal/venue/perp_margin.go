package venue

import "context"

// PerpMarginProvider 是跨所对冲可选能力:读取永续/合约钱包可用保证金,并从闲置钱包补足差额。
// 未实现的适配器在闸门处保守拒绝(不当作 0 或无穷大)。
type PerpMarginProvider interface {
	FetchPerpAvailableUSDT(ctx context.Context) (float64, error)
	TopUpPerpMarginUSDT(ctx context.Context, requiredUSDT float64) (transferred float64, err error)
}
