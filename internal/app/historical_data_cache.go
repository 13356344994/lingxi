package app

import "lingxi/internal/exchange"

// 历史负载缓存已迁移到 internal/exchange（唯一摸网络的包）。
// 这里保留同名别名与构造包装，让既有引用无需改动即可继续编译。
type historicalPayloadCache = exchange.HistoricalPayloadCache

func newHistoricalPayloadCache(dir string) (*historicalPayloadCache, error) {
	return exchange.NewHistoricalPayloadCache(dir)
}
