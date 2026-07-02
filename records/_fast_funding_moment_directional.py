#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""资费结算瞬间方向性研究(read-only,不下单)。

假说:资费结算瞬间(00/08/16 UTC)存在短暂的方向性压力释放——付费侧在结算后
平仓/减仓,价格倾向朝「资费的反方向」走(fade the funding)。

两种运行模式:
  默认      : 只读累计事件文件 records/funding_moment_events.jsonl,输出方向性统计。
  --live    : 经 OKX 公共 REST(免鉴权)取最近一次结算的资费率 + 结算前后 1s K线,
              计算 entry_mid 与 fwd 30/60/120/300s 的带符号漂移(bps),按 (symbol,settle_ts)
              去重后追加进累计文件,再输出统计。由 systemd 定时器每日 4 班自动积累样本
              (deploy/lingxi-funding-study.{service,timer},结算后 70min 跑,1s K线查历史)。

事件行 schema(与既有数据兼容):
  {symbol, settle_ts, rate, entry_mid, recorded_at, fwd_30s_bps, fwd_60s_bps, fwd_120s_bps, fwd_300s_bps}

统计口径:方向 = -sign(rate)(付费侧出清方向);aligned_bps = fwd_bps × direction。
aligned>0 表示「顺 fade 方向」赚。样本按 |rate| 分桶看单调性(桶间不单调=信号可疑)。
"""
import json
import math
import os
import sys
import time
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
EVENTS_PATH = os.path.join(HERE, "funding_moment_events.jsonl")
OKX = "https://www.okx.com"
TOP_N = 80          # 每班覆盖的成交额头部 SWAP 数
RATE_MIN = 0.00005  # |rate|<0.005% 的样本方向性太弱,不采
WINDOWS = [30, 60, 120, 300]


def http_json(path, retries=3):
    url = OKX + path
    for i in range(retries):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "lingxi-study/1.0"})
            with urllib.request.urlopen(req, timeout=15) as r:
                d = json.loads(r.read().decode("utf-8"))
            if d.get("code") not in ("0", 0):
                raise RuntimeError("okx code=%s msg=%s" % (d.get("code"), d.get("msg")))
            return d.get("data") or []
        except Exception as e:
            if i == retries - 1:
                raise
            time.sleep(1.0 + i)
    return []


def last_settle_ts(now=None):
    now = now or time.time()
    day = int(now // 86400) * 86400
    for h in (16, 8, 0):
        ts = day + h * 3600
        if ts <= now - 360:  # 结算后至少 6min 才有完整 300s 前向窗
            return ts
    return day - 86400 + 16 * 3600


def load_existing_keys():
    keys = set()
    if not os.path.exists(EVENTS_PATH):
        return keys
    with open(EVENTS_PATH) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                d = json.loads(line)
                keys.add((d["symbol"], int(d["settle_ts"])))
            except Exception:
                continue
    return keys


def top_swap_instruments():
    data = http_json("/api/v5/market/tickers?instType=SWAP")
    rows = []
    for t in data:
        inst = t.get("instId", "")
        if not inst.endswith("-USDT-SWAP"):
            continue
        try:
            vol = float(t.get("volCcy24h") or 0) * float(t.get("last") or 0)
        except ValueError:
            continue
        rows.append((vol, inst))
    rows.sort(reverse=True)
    return [inst for _, inst in rows[:TOP_N]]


def funding_rate_at(inst, settle_ts):
    ms = settle_ts * 1000
    data = http_json("/api/v5/public/funding-rate-history?instId=%s&after=%d&limit=3" % (inst, ms + 1))
    for d in data:
        if int(d.get("fundingTime", 0)) == ms:
            try:
                return float(d.get("realizedRate") or d.get("fundingRate"))
            except (TypeError, ValueError):
                return None
    return None


def candles_1s(inst, settle_ts):
    """取 [settle-2s, settle+305s] 的 1s K线,返回 {ts_sec: close}。history-candles 每页 100 根,倒序分页。"""
    closes = {}
    after = (settle_ts + 306) * 1000
    lo = (settle_ts - 2) * 1000
    for _ in range(5):
        data = http_json("/api/v5/market/history-candles?instId=%s&bar=1s&after=%d&limit=100" % (inst, after))
        if not data:
            break
        for row in data:
            ts = int(row[0])
            closes[ts // 1000] = float(row[4])
        oldest = int(data[-1][0])
        if oldest <= lo:
            break
        after = oldest
        time.sleep(0.12)
    return closes


def nearest_close(closes, ts, tol=3):
    for d in range(tol + 1):
        for cand in (ts + d, ts - d):
            if cand in closes:
                return closes[cand]
    return None


def collect_live():
    settle = last_settle_ts()
    existing = load_existing_keys()
    insts = top_swap_instruments()
    print("[live] settle_ts=%d (%s UTC) 候选=%d 已有样本键=%d" % (
        settle, time.strftime("%F %T", time.gmtime(settle)), len(insts), len(existing)))
    added = 0
    with open(EVENTS_PATH, "a") as out:
        for inst in insts:
            sym = inst.replace("-SWAP", "")
            if (sym, settle) in existing:
                continue
            try:
                rate = funding_rate_at(inst, settle)
                if rate is None or abs(rate) < RATE_MIN:
                    continue
                closes = candles_1s(inst, settle)
                entry = nearest_close(closes, settle)
                if not entry:
                    continue
                ev = {"symbol": sym, "settle_ts": settle, "rate": rate, "entry_mid": entry,
                      "recorded_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
                ok = True
                for w in WINDOWS:
                    px = nearest_close(closes, settle + w)
                    if px is None:
                        ok = False
                        break
                    ev["fwd_%ds_bps" % w] = (px / entry - 1.0) * 1e4
                if not ok:
                    continue
                out.write(json.dumps(ev) + "\n")
                out.flush()
                added += 1
            except Exception as e:
                print("[live] %s 跳过: %s" % (inst, e))
            time.sleep(0.15)
    print("[live] 本班新增样本 %d 条" % added)


def pstats(vals):
    n = len(vals)
    if n == 0:
        return "n=0"
    mean = sum(vals) / n
    med = sorted(vals)[n // 2]
    win = sum(1 for v in vals if v > 0) / n
    sd = math.sqrt(sum((v - mean) ** 2 for v in vals) / n) if n > 1 else 0.0
    t = mean / (sd / math.sqrt(n)) if sd > 0 else 0.0
    return "n=%-4d mean=%+7.2fbp med=%+7.2fbp win=%4.1f%% t=%+.2f" % (n, mean, med, win * 100, t)


def report():
    rows = []
    if os.path.exists(EVENTS_PATH):
        with open(EVENTS_PATH) as f:
            for line in f:
                line = line.strip()
                if line:
                    try:
                        rows.append(json.loads(line))
                    except Exception:
                        pass
    print("\n===== 资费结算瞬间·方向性研究(累计 %d 样本;方向= -sign(rate) 即 fade)=====" % len(rows))
    if not rows:
        return
    settles = sorted(set(r["settle_ts"] for r in rows))
    print("覆盖结算班次 %d 个: %s ~ %s UTC" % (
        len(settles), time.strftime("%F %H:%M", time.gmtime(settles[0])),
        time.strftime("%F %H:%M", time.gmtime(settles[-1]))))
    for w in WINDOWS:
        key = "fwd_%ds_bps" % w
        aligned = [-math.copysign(1, r["rate"]) * r[key] for r in rows if key in r]
        print("fwd %3ds aligned: %s" % (w, pstats(aligned)))
    print("-- 按 |rate| 分桶(fwd_60s aligned;桶间应单调增,否则信号可疑)--")
    buckets = [(0.00005, 0.0002), (0.0002, 0.0005), (0.0005, 0.001), (0.001, 99)]
    for lo, hi in buckets:
        sel = [r for r in rows if lo <= abs(r["rate"]) < hi and "fwd_60s_bps" in r]
        aligned = [-math.copysign(1, r["rate"]) * r["fwd_60s_bps"] for r in sel]
        print("|rate|∈[%.3f%%,%.3f%%): %s" % (lo * 100, hi * 100, pstats(aligned)))
    print("判读:mean>0 且 t>2 且桶间单调 → 有戏,进影子模拟;否则继续积累或放弃。")
    print("成本对照:OKX 现费率 taker 单边≈5bp、往返≈10bp;maker/返佣后见 fee_rebate.json。")


if __name__ == "__main__":
    if "--live" in sys.argv:
        collect_live()
    report()
