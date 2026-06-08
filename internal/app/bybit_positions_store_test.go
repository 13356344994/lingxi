package app

import (
	"testing"
	"time"
)

func TestBybitPositionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := newBybitPositionStore(dir)
	if err != nil {
		t.Fatalf("建仓位台账失败: %v", err)
	}
	if ps, err := store.load(); err != nil || len(ps) != 0 {
		t.Fatalf("空台账应返回空, got %v err=%v", ps, err)
	}

	p1 := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, NextFundingTime: time.Now().UTC(), IdemPrefix: "lx-1"}
	p2 := BybitLivePosition{Symbol: "EDEN-USDT", BaseQty: 5, NextFundingTime: time.Now().UTC(), IdemPrefix: "lx-2"}
	if err := store.upsert(p1); err != nil {
		t.Fatalf("upsert p1: %v", err)
	}
	if err := store.upsert(p2); err != nil {
		t.Fatalf("upsert p2: %v", err)
	}
	// 重复 upsert 同 symbol 应覆盖而非新增。
	p1.BaseQty = 20
	if err := store.upsert(p1); err != nil {
		t.Fatalf("upsert p1 again: %v", err)
	}
	ps, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(ps) != 2 {
		t.Fatalf("应有 2 笔持仓, got %d", len(ps))
	}
	var homeQty float64
	for _, p := range ps {
		if p.Symbol == "HOME-USDT" {
			homeQty = p.BaseQty
		}
	}
	if homeQty != 20 {
		t.Fatalf("HOME 数量应被覆盖为 20, got %v", homeQty)
	}

	if err := store.remove("HOME-USDT"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	ps, _ = store.load()
	if len(ps) != 1 || ps[0].Symbol != "EDEN-USDT" {
		t.Fatalf("移除 HOME 后应只剩 EDEN, got %+v", ps)
	}

	// 持久化:新实例从同目录加载应见到 EDEN。
	store2, err := newBybitPositionStore(dir)
	if err != nil {
		t.Fatalf("重开台账: %v", err)
	}
	ps, _ = store2.load()
	if len(ps) != 1 || ps[0].Symbol != "EDEN-USDT" {
		t.Fatalf("跨实例加载应保留 EDEN, got %+v", ps)
	}
}

func TestBybitIdemPrefixStableAcrossCalls(t *testing.T) {
	ft := time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC)
	a := bybitIdemPrefix("HOME-USDT", ft)
	b := bybitIdemPrefix("HOME-USDT", ft)
	if a != b {
		t.Fatalf("同 symbol+结算时刻幂等前缀应稳定: %s vs %s", a, b)
	}
	if c := bybitIdemPrefix("HOME-USDT", ft.Add(time.Hour)); c == a {
		t.Fatalf("不同结算时刻前缀应不同")
	}
}
