#!/usr/bin/env python3
"""阶段-1 数据抓取（父代理用，需网络）。
抓币安主流币历史资金费率(含markPrice)与现货8h收盘价，缓存到 data/ 供离线分析。
只读公开接口，无鉴权，不下单。
"""
import json, os, time, urllib.request, urllib.parse

HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.path.join(HERE, "data")
os.makedirs(DATA, exist_ok=True)

SYMBOLS = ["BTCUSDT","ETHUSDT","SOLUSDT","BNBUSDT","XRPUSDT",
           "DOGEUSDT","ADAUSDT","AVAXUSDT","LINKUSDT","SUIUSDT"]

DAYS = 90
NOW_MS = int(time.time()*1000)
START_MS = NOW_MS - DAYS*24*3600*1000

FAPI = "https://fapi.binance.com"
SAPI = "https://api.binance.com"

def get(url):
    for attempt in range(5):
        try:
            req = urllib.request.Request(url, headers={"User-Agent":"go-no-go/0.1"})
            with urllib.request.urlopen(req, timeout=20) as r:
                return json.loads(r.read().decode())
        except Exception as e:
            wait = 2*(attempt+1)
            print(f"  retry {attempt+1} after {wait}s: {e}")
            time.sleep(wait)
    raise RuntimeError(f"failed: {url}")

def fetch_funding(sym):
    out = []
    start = START_MS
    while True:
        q = urllib.parse.urlencode({"symbol":sym,"startTime":start,"endTime":NOW_MS,"limit":1000})
        chunk = get(f"{FAPI}/fapi/v1/fundingRate?{q}")
        if not chunk:
            break
        out.extend(chunk)
        if len(chunk) < 1000:
            break
        start = chunk[-1]["fundingTime"] + 1
        time.sleep(0.25)
    return out

def fetch_klines(base, path, sym):
    out = []
    start = START_MS
    while True:
        q = urllib.parse.urlencode({"symbol":sym,"interval":"8h","startTime":start,"endTime":NOW_MS,"limit":1000})
        chunk = get(f"{base}{path}?{q}")
        if not chunk:
            break
        out.extend(chunk)
        if len(chunk) < 1000:
            break
        start = chunk[-1][0] + 1
        time.sleep(0.25)
    # openTime -> close
    return {int(k[0]): float(k[4]) for k in out}

def main():
    summary = {}
    for sym in SYMBOLS:
        print(f"[{sym}] funding...")
        funding = fetch_funding(sym)
        print(f"[{sym}] spot klines...")
        spot = fetch_klines(SAPI, "/api/v3/klines", sym)
        rec = {
            "symbol": sym,
            "days": DAYS,
            "funding": funding,         # [{fundingTime, fundingRate, markPrice}]
            "spot_8h_close": spot,      # {openTimeMs: close}
        }
        with open(os.path.join(DATA, f"{sym}.json"), "w") as f:
            json.dump(rec, f)
        n = len(funding)
        pos = sum(1 for x in funding if float(x["fundingRate"])>0)
        avg = sum(float(x["fundingRate"]) for x in funding)/n if n else 0
        summary[sym] = {"funding_points":n, "positive_pct":round(pos/n*100,1) if n else 0,
                        "avg_rate_per_8h":avg, "avg_apr_pct":round(avg*3*365*100,2),
                        "spot_points":len(spot)}
        print(f"  -> {n} funding pts, {summary[sym]['positive_pct']}% positive, avg APR {summary[sym]['avg_apr_pct']}%")
    with open(os.path.join(DATA, "_summary.json"),"w") as f:
        json.dump(summary, f, indent=2)
    print("\n=== SUMMARY (raw funding, no cost) ===")
    for s,v in summary.items():
        print(f"{s:10} pts={v['funding_points']:4} pos%={v['positive_pct']:5} avgAPR={v['avg_apr_pct']:7}% spot={v['spot_points']}")

if __name__ == "__main__":
    main()
