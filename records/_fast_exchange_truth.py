#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""fast 实盘账本重建 + 赢单解剖(只读,绝不下单)。

背景:7-02 事故把本地 settlements 回滚,fast 实盘(6/30-7/01,~3855笔)结算行丢失。
但交易真实发生过——fast 单 clOrdId 含 fastmaker/fastclose 标记,从 OKX
fills-history API 拉回全部成交,按 symbol 配对重建每笔往返的真实 PnL(含手续费),
再 join fast_maker_attempts.jsonl 的入场特征(score/spread/imbalance/时段),
解剖:赢单集中在什么条件?有没有可过滤出的正期望子集?

用法: python3 _fast_exchange_truth.py [--cache-only]
输出: fast_exchange_fills_cache.jsonl (原始成交缓存,幂等增量)
      终端打印解剖报告
"""
import base64
import collections
import hashlib
import hmac
import json
import os
import sys
import time
import urllib.parse
import urllib.request
from datetime import datetime, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
CACHE = os.path.join(HERE, "fast_exchange_fills_cache.jsonl")
ATTEMPTS = os.path.join(HERE, "okx_directional", "fast_maker_attempts.jsonl")

# fast 实盘窗口(UTC ms):6-30 00:00 → 7-02 00:00,宽松覆盖
BEGIN_MS = int(datetime(2026, 6, 29, 0, 0, tzinfo=timezone.utc).timestamp() * 1000)
END_MS = int(datetime(2026, 7, 2, 12, 0, tzinfo=timezone.utc).timestamp() * 1000)


def load_okx_creds():
    api = secret = passphrase = ""
    in_okx = False
    for line in open(os.path.join(ROOT, "secret.yaml")):
        raw = line.rstrip("\n")
        if not raw.strip() or raw.strip().startswith("#"):
            continue
        if not raw.startswith(" "):
            in_okx = raw.strip().rstrip(":") == "okx"
            continue
        if in_okx:
            k, _, v = raw.strip().partition(":")
            v = v.strip().strip('"').strip("'")
            if k == "api":
                api = v
            elif k == "secret":
                secret = v
            elif k == "passphrase":
                passphrase = v
    if not (api and secret and passphrase):
        sys.exit("secret.yaml 缺 okx api/secret/passphrase")
    return api, secret, passphrase


def okx_get(path, params, creds):
    api, secret, passphrase = creds
    qs = urllib.parse.urlencode(params)
    request_path = path + ("?" + qs if qs else "")
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
    msg = ts + "GET" + request_path
    sign = base64.b64encode(hmac.new(secret.encode(), msg.encode(), hashlib.sha256).digest()).decode()
    req = urllib.request.Request(
        "https://www.okx.com" + request_path,
        headers={
            "OK-ACCESS-KEY": api,
            "OK-ACCESS-SIGN": sign,
            "OK-ACCESS-TIMESTAMP": ts,
            "OK-ACCESS-PASSPHRASE": passphrase,
            "Content-Type": "application/json",
            # python 默认 UA 会被 WAF 拦 403,伪装成常规客户端
            "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) lingxi-readonly-audit",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            out = json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")[:300]
        raise RuntimeError(f"okx http {e.code}: {body}")
    if out.get("code") != "0":
        raise RuntimeError(f"okx {path} code={out.get('code')} msg={out.get('msg')}")
    return out.get("data") or []


def fetch_fills(creds):
    """fills-history 分页拉取窗口内全部 SWAP 成交(billId 向前翻页),幂等缓存。"""
    seen = set()
    fills = []
    if os.path.exists(CACHE):
        for l in open(CACHE):
            try:
                d = json.loads(l)
                seen.add(d["billId"])
                fills.append(d)
            except Exception:
                pass
        print(f"[cache] 已有 {len(fills)} 条成交缓存")
        if fills:
            return fills  # 窗口固定且已成历史,缓存即全量;要强制重拉删缓存文件
    after = ""
    total_new = 0
    with open(CACHE, "a") as out:
        while True:
            params = {"instType": "SWAP", "begin": str(BEGIN_MS), "end": str(END_MS), "limit": "100"}
            if after:
                params["after"] = after  # 请求早于该 billId 的记录
            data = okx_get("/api/v5/trade/fills-history", params, creds)
            if not data:
                break
            for d in data:
                if d["billId"] in seen:
                    continue
                seen.add(d["billId"])
                fills.append(d)
                out.write(json.dumps(d, ensure_ascii=False) + "\n")
                total_new += 1
            after = data[-1]["billId"]
            if len(data) < 100:
                break
            time.sleep(0.25)  # 限频礼貌
    print(f"[api] 新拉取 {total_new} 条,总 {len(fills)} 条")
    return fills


def rebuild_roundtrips(fills):
    """按 instId 时间序配对 fast 开/平,重建每笔往返(fast 全部单向持仓,FIFO 即可)。"""
    fast_fills = [f for f in fills if "fast" in (f.get("clOrdId") or "").lower()]
    print(f"fast 成交腿: {len(fast_fills)} / 总 {len(fills)}")
    bysym = collections.defaultdict(list)
    for f in fast_fills:
        bysym[f["instId"]].append(f)
    trades = []
    for inst, legs in bysym.items():
        legs.sort(key=lambda x: int(x["ts"]))
        pos = None  # {"side","qty","cost","fee","open_ts","open_px"}
        for f in legs:
            clid = (f.get("clOrdId") or "").lower()
            px = float(f["fillPx"])
            sz = float(f["fillSz"])
            fee = float(f.get("fee") or 0)  # 负=支出
            ts = int(f["ts"])
            is_open = "fastmaker" in clid
            is_close = "fastclose" in clid
            if is_open:
                if pos is None:
                    pos = {"side": f["side"], "qty": sz, "px": px, "fee": fee, "ts": ts, "notional": px * sz}
                else:  # 同向加腿(部分成交多腿)
                    tot = pos["qty"] + sz
                    pos["px"] = (pos["px"] * pos["qty"] + px * sz) / tot
                    pos["qty"] = tot
                    pos["fee"] += fee
                    pos["notional"] += px * sz
            elif is_close and pos is not None:
                # 方向收益:open buy=做多
                sign = 1.0 if pos["side"] == "buy" else -1.0
                gross = sign * (px - pos["px"]) * min(sz, pos["qty"])
                # 张→币换算:fillSz 是张,OKX 线性合约张含 ctVal 币 —— gross 用价差×张×ctVal 才是 USDT。
                # 简化:用 fillPx*fillSz 做名义近似(ctVal 未知),改用收益率口径避免张值歧义。
                ret = sign * (px - pos["px"]) / pos["px"]
                fee_total = pos["fee"] + fee  # 双腿手续费(USDT,负值)
                trades.append({
                    "inst": inst,
                    "side": "long" if pos["side"] == "buy" else "short",
                    "open_px": pos["px"], "close_px": px,
                    "ret_bps": ret * 10000,
                    "fee_usdt": fee_total,
                    "open_ts": pos["ts"], "close_ts": ts,
                    "hold_s": (ts - pos["ts"]) / 1000.0,
                })
                pos["qty"] -= sz
                if pos["qty"] <= 1e-9:
                    pos = None
    return trades


def load_attempt_features():
    """入场特征索引:(symbol, ~时间) → score/spread/imbalance/reason。"""
    feats = []
    if not os.path.exists(ATTEMPTS):
        return feats
    for l in open(ATTEMPTS):
        try:
            d = json.loads(l)
        except Exception:
            continue
        if not d.get("maker_filled"):
            continue
        ts = d.get("recorded_at", "")
        try:
            t = datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp()
        except Exception:
            continue
        feats.append((d.get("symbol", "").replace("-", "").upper(), t, d))
    feats.sort(key=lambda x: x[1])
    return feats


def join_features(trades, feats):
    import bisect
    keys = [(s, t) for s, t, _ in feats]
    for tr in trades:
        sym = tr["inst"].replace("-SWAP", "").replace("-", "").upper()
        t0 = tr["open_ts"] / 1000.0
        best = None
        bestdt = 30.0  # 30s 内最近的成交入场尝试
        for s, t, d in feats:
            if s != sym:
                continue
            dt = abs(t - t0)
            if dt < bestdt:
                bestdt, best = dt, d
        tr["feat"] = best
    return trades


def pf(rows, key=lambda r: r["net_bps"]):
    gp = sum(key(r) for r in rows if key(r) > 0)
    gl = -sum(key(r) for r in rows if key(r) < 0)
    return gp / gl if gl > 0 else float("inf")


def report(trades):
    # 成本口径:fee_usdt 是真实值但名义未知 → 统一用 bps 口径,费率按腿估:maker 2bp(限价) taker 5bp(市价)。
    # 更诚实:直接用毛收益 ret_bps,单独报真实 fee 合计。
    fee_total = sum(t["fee_usdt"] for t in trades)
    for t in trades:
        t["net_bps"] = t["ret_bps"]  # 毛口径;费用单独看
    n = len(trades)
    if n == 0:
        print("无重建往返")
        return
    wins = sum(1 for t in trades if t["ret_bps"] > 0)
    print(f"\n===== 交易所口径 fast 实盘重建:{n} 笔往返 =====")
    print(f"毛胜率 {wins/n*100:.0f}%  毛PF {pf(trades, lambda r: r['ret_bps']):.2f}  "
          f"毛均值 {sum(t['ret_bps'] for t in trades)/n:+.2f}bps  真实手续费合计 {fee_total:+.2f}U")

    def bucket_report(name, keyfn, fmt=str):
        groups = collections.defaultdict(list)
        for t in trades:
            groups[keyfn(t)].append(t)
        print(f"\n--- 按 {name} ---")
        for k in sorted(groups, key=lambda k: -len(groups[k]))[:10]:
            g = groups[k]
            w = sum(1 for t in g if t["ret_bps"] > 0)
            m = sum(t["ret_bps"] for t in g) / len(g)
            print(f"  {fmt(k):24s} n={len(g):5d} 胜率{w/len(g)*100:3.0f}% 毛均{m:+6.2f}bps 毛PF{pf(g, lambda r: r['ret_bps']):5.2f}")

    bucket_report("持仓时长", lambda t: ("<15s" if t["hold_s"] < 15 else "<60s" if t["hold_s"] < 60 else "<180s" if t["hold_s"] < 180 else ">=180s"))
    bucket_report("方向", lambda t: t["side"])
    bucket_report("时段(UTC时)", lambda t: datetime.fromtimestamp(t["open_ts"]/1000, tz=timezone.utc).hour, fmt=lambda h: f"{h:02d}:00")
    bucket_report("symbol", lambda t: t["inst"])

    withf = [t for t in trades if t.get("feat")]
    print(f"\n(有入场特征 join 的: {len(withf)} 笔)")
    if withf:
        bucket_report("入场分数档", lambda t: round((t["feat"] or {}).get("score", 0) * 10) / 10 if t.get("feat") else None)
        bucket_report("score_reason", lambda t: (t["feat"] or {}).get("score_reason", "?") if t.get("feat") else "?")
        bucket_report("点差档", lambda t: ("<2bp" if (t["feat"] or {}).get("spread_bps", 99) < 2 else "<4bp" if (t["feat"] or {}).get("spread_bps", 99) < 4 else ">=4bp") if t.get("feat") else "?")


def main():
    creds = load_okx_creds()
    fills = fetch_fills(creds)
    trades = rebuild_roundtrips(fills)
    feats = load_attempt_features()
    trades = join_features(trades, feats)
    report(trades)


if __name__ == "__main__":
    main()
