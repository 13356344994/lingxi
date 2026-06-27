package app

import (
	"context"

	"lingxi/internal/venue"
)

// DirectionalVenue 是方向量化引擎对单个交易所的依赖面:通用 venue.Venue 能力 + 统一 K 线拉取。
// okx/binance/bybit/gate 的 *Adapter 均满足(它们都实现了 venue.Venue 且各有 FetchCandles([]venue.Candle))。
type DirectionalVenue interface {
	venue.Venue
	FetchCandles(ctx context.Context, m venue.Market, s venue.Symbol, bar string, limit int) ([]venue.Candle, error)
}
