package main

// 离线 fixture:按 OKX v5 文档字段手工构造,非真实账户数据,不触网。
const ordersPushFixture = `{
  "arg": {"channel": "orders", "instType": "SWAP"},
  "data": [{
    "instType": "SWAP",
    "instId": "BTC-USDT-SWAP",
    "ordId": "680800019903107072",
    "clOrdId": "abc123",
    "ordType": "limit",
    "side": "buy",
    "state": "partially_filled",
    "px": "50000",
    "sz": "10",
    "avgPx": "50000",
    "accFillSz": "4",
    "fillSz": "4",
    "fillPx": "50000",
    "fillPnl": "0",
    "tradeId": "trade-001",
    "feeCcy": "USDT",
    "fee": "-0.02",
    "reduceOnly": "false",
    "category": "normal",
    "code": "0",
    "msg": "",
    "uTime": "1597026383085",
    "cTime": "1597026383085"
  }]
}`

const positionsPushFixture = `{
  "arg": {"channel": "positions", "instType": "SWAP"},
  "data": [{
    "instType": "SWAP",
    "instId": "BTC-USDT-SWAP",
    "posSide": "short",
    "pos": "3",
    "avgPx": "100",
    "markPx": "99",
    "uTime": "1597026383085"
  }]
}`
