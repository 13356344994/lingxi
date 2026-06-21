package venue

import (
	"context"
	"time"
)

// ExternalFundFlowProvider 是可选能力:拉取 [since, now] 窗口内外部充提净流入(USDT 口径,deposit−withdraw)。
// 不含所内钱包划转;未实现的适配器不应被 type assert 命中,或返回 ErrUnsupported。
type ExternalFundFlowProvider interface {
	FetchNetExternalFundFlowUSDT(ctx context.Context, since time.Time) (float64, error)
}
