#!/usr/bin/env python3
"""阶段-1 复核：更大样本抓取（父代理用，需网络）。
- 全币安永续 ∩ 现货双挂
- 纳入费率更肥的中小市值币(按 |lastFundingRate| 排) + 高成交额币
- 180 天历史资金费率(含markPrice) + 现货8h收盘
缓存到 data_broad/ 供离线分析。只读公开接口，无鉴权，不下单。
"""
import json, os, time, urllib.request, urllib.parse

HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.path.join(HERE, "data_broad")
os.makedirs(DATA, exist_ok=True)

FAPI = "https://fapi.binance.com"
SAPI = "https://api.binance.com"
DAYS = 180
NOW_MS = int(time.time()*1000)
START_MS = NOW_MS - DAYS*24*3600*1000

def get(url):
    for attempt in range(5):
        try:
            req = urllib.request.Request(url, headers={"User-Agent":"go-no-go/0.2"})
            with urllib.request.urlopen(req, timeout=25) as r:
                return json.loads(r.read().decode())
        except Exception as e:
            wait = 2*(attempt+1); print(f"  retry {attempt+1} {wait}s: {e}"); time.sleep(wait)
    raise RuntimeError(f"failed {url}")

def build_universe():
    print("perp exchangeInfo..."); perp = get(f"{FAPI}/fapi/v1/exchangeInfo")
    perps = {s["symbol"] for s in perp["symbols"]
             if s.get("contractType")=="PERPETUAL" and s.get("status")=="TRADING"
             and s.get("quoteAsset")=="USDT"}
    print("spot exchangeInfo..."); spot = get(f"{SAPI}/api/v3/exchangeInfo")
    spots = {s["symbol"] for s in spot["symbols"]
             if s.get("status")=="TRADING" and s.get("quoteAsset")=="USDT"
             and s.get("isSpotTradingAllowed")}
    dual = sorted(perps & spots)
    print(f"perps={len(perps)} spots={len(spots)} dual-listed={len(dual)}")
    print("premiumIndex (current funding)..."); pi = {x["symbol"]: float(x.get("lastFundingRate") or 0) for x in get(f"{FAPI}/fapi/v1/premiumIndex")}
    print("24h ticker (volume)..."); vol = {x["symbol"]: float(x.get("quoteVolume") or 0) for x in get(f"{FAPI}/fapi/v1/ticker/24hr")}
    rows = [{"symbol":s,"fr":pi.get(s,0),"vol":vol.get(s,0)} for s in dual]
    # 高费率(中小市值常在此) + 高成交额 两组取并集
    by_fr  = sorted(rows, key=lambda r: abs(r["fr"]), reverse=True)[:35]
    by_vol = sorted(rows, key=lambda r: r["vol"], reverse=True)[:35]
    sel, seen = [], set()
    for r in by_fr+by_vol:
        if r["symbol"] not in seen:
            seen.add(r["symbol"]); sel.append(r)
    print(f"selected={len(sel)} symbols")
    return sel

def fetch_funding(sym):
    q = urllib.parse.urlencode({"symbol":sym,"startTime":START_MS,"endTime":NOW_MS,"limit":1000})
    return get(f"{FAPI}/fapi/v1/fundingRate?{q}")

def fetch_spot(sym):
    q = urllib.parse.urlencode({"symbol":sym,"interval":"8h","startTime":START_MS,"endTime":NOW_MS,"limit":1000})
    try:
        kl = get(f"{SAPI}/api/v3/klines?{q}")
    except Exception:
        return {}
    return {int(k[0]): float(k[4]) for k in kl}

def main():
    sel = build_universe()
    summary = {}
    for i,r in enumerate(sel):
        sym = r["symbol"]
        print(f"[{i+1}/{len(sel)}] {sym} fr={r['fr']:.5f} vol={r['vol']/1e6:.1f}M")
        try:
            funding = fetch_funding(sym); time.sleep(0.2)
            spot = fetch_spot(sym); time.sleep(0.2)
        except Exception as e:
            print(f"  skip {sym}: {e}"); continue
        json.dump({"symbol":sym,"days":DAYS,"quote_volume_24h":r["vol"],
                   "funding":funding,"spot_8h_close":spot},
                  open(os.path.join(DATA,f"{sym}.json"),"w"))
        n=len(funding); avg=sum(float(x["fundingRate"]) for x in funding)/n if n else 0
        pos=sum(1 for x in funding if float(x["fundingRate"])>0)
        summary[sym]={"pts":n,"pos_pct":round(pos/n*100,1) if n else 0,
                      "avg_apr_pct":round(avg*3*365*100,2),"vol_m":round(r["vol"]/1e6,1),
                      "spot_pts":len(spot)}
    json.dump(summary, open(os.path.join(DATA,"_summary.json"),"w"), indent=2)
    print("\n=== TOP by gross funding APR (raw, no cost) ===")
    for s,v in sorted(summary.items(), key=lambda kv: kv[1]["avg_apr_pct"], reverse=True)[:25]:
        print(f"{s:14} avgAPR={v['avg_apr_pct']:8}% pos%={v['pos_pct']:5} vol={v['vol_m']:8}M spot={v['spot_pts']}")

if __name__=="__main__":
    main()
