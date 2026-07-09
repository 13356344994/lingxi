#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""一次性长历史行情采集脚本(给"天气多样性验证"买数据)。

采集内容:
  a) OKX 1H K线   全部 USDT 本位 SWAP,翻页到 2024-01-01(或 API 到底) -> records/history/okx_1h/{instId}.jsonl
  b) OKX 5m K线   24h 成交额前 150 合约,向前翻 60 天                  -> records/history/okx_5m/{instId}.jsonl
  c) Bybit 资费    全部 linear USDT 永续,翻一年                        -> records/history/bybit_funding/{symbol}.jsonl
  d) OKX 资费      全部 USDT SWAP,翻到底(约3个月)                     -> records/history/okx_funding/{instId}.jsonl

工程特性:
  - 全局限频 <=5 req/s;429/403 指数退避 1s->2s->4s->..->60s
  - 断点续传:每币完成 touch {file}.done;重启跳过;进行中的币从已写文件最老时间戳继续向前翻
  - 每 30s 打一行进度日志到 stdout
  - --trial 模式:只跑 3 个币、每任务最多 3 页,不写 .done(用于验证格式)

用法:
  python3 records/_history_fetch.py            # 全量
  python3 records/_history_fetch.py --trial    # 试跑验证
"""

import json
import os
import sys
import time

import requests

BASE_DIR = os.path.dirname(os.path.abspath(__file__))          # records/
HIST_DIR = os.path.join(BASE_DIR, "history")

OKX_BASE = "https://www.okx.com"
BYBIT_BASE = "https://api.bybit.com"

# OKX 不带 UA 会 403(已实测)
HEADERS = {
    "User-Agent": "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0",
    "Accept": "application/json",
}

RATE_INTERVAL = 0.2          # 全局 <=5 req/s
BACKOFF_MAX = 60.0
LOG_INTERVAL = 30.0

NOW_MS = int(time.time() * 1000)
CUTOFF_1H_MS = 1704067200000                       # 2024-01-01 00:00 UTC
CUTOFF_5M_MS = NOW_MS - 60 * 86400 * 1000          # 60 天
CUTOFF_BYBIT_FUND_MS = NOW_MS - 365 * 86400 * 1000 # 一年
# OKX 资费历史 API 本身只留 ~3 个月,直接翻到 API 返回空为止

TRIAL = "--trial" in sys.argv
TRIAL_INSTS = ["BTC-USDT-SWAP", "ETH-USDT-SWAP", "SOL-USDT-SWAP"]
TRIAL_BYBIT = ["BTCUSDT", "ETHUSDT", "SOLUSDT"]
TRIAL_MAX_PAGES = 3

SESSION = requests.Session()

STATE = {
    "done": 0,
    "total": 0,
    "current": "",
    "lines": 0,
    "requests": 0,
    "start": time.time(),
    "last_log": 0.0,
}
_last_req_ts = [0.0]


def log(msg):
    print("[%s] %s" % (time.strftime("%Y-%m-%d %H:%M:%S"), msg))
    sys.stdout.flush()


def progress_log(force=False):
    now = time.time()
    if not force and now - STATE["last_log"] < LOG_INTERVAL:
        return
    STATE["last_log"] = now
    elapsed = int(now - STATE["start"])
    log("progress: done %d/%d | current %s | lines %d | requests %d | elapsed %ds"
        % (STATE["done"], STATE["total"], STATE["current"],
           STATE["lines"], STATE["requests"], elapsed))


def http_get(url, params=None):
    """全局限频 GET,429/403/网络错误指数退避,永不硬扛。返回解析后的 JSON。"""
    backoff = 1.0
    while True:
        wait = _last_req_ts[0] + RATE_INTERVAL - time.time()
        if wait > 0:
            time.sleep(wait)
        _last_req_ts[0] = time.time()
        STATE["requests"] += 1
        try:
            r = SESSION.get(url, params=params, headers=HEADERS, timeout=20)
        except Exception as e:
            log("net error on %s: %s ; backoff %.0fs" % (url, e, backoff))
            time.sleep(backoff)
            backoff = min(backoff * 2, BACKOFF_MAX)
            continue
        if r.status_code in (429, 403):
            log("HTTP %d on %s ; backoff %.0fs" % (r.status_code, url, backoff))
            time.sleep(backoff)
            backoff = min(backoff * 2, BACKOFF_MAX)
            continue
        if r.status_code != 200:
            log("HTTP %d on %s ; backoff %.0fs" % (r.status_code, url, backoff))
            time.sleep(backoff)
            backoff = min(backoff * 2, BACKOFF_MAX)
            continue
        try:
            return r.json()
        except Exception as e:
            log("bad json on %s: %s ; backoff %.0fs" % (url, e, backoff))
            time.sleep(backoff)
            backoff = min(backoff * 2, BACKOFF_MAX)


def oldest_ts_in_file(path, extract_ts):
    """扫描已有 jsonl,返回最老时间戳(ms, int);文件不存在/空返回 None。"""
    if not os.path.exists(path):
        return None
    oldest = None
    try:
        with open(path, "r") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    ts = int(extract_ts(json.loads(line)))
                except Exception:
                    continue
                if oldest is None or ts < oldest:
                    oldest = ts
    except Exception as e:
        log("warn: scan %s failed: %s" % (path, e))
    return oldest


def mark_done(path):
    if TRIAL:
        return  # 试跑不写 .done,避免全量误跳过
    with open(path + ".done", "w") as f:
        f.write(str(int(time.time())))


def write_rows(fh, rows):
    fetched_at = int(time.time() * 1000)
    for row in rows:
        fh.write(json.dumps({"data": row, "fetched_at": fetched_at},
                            separators=(",", ":")) + "\n")
    fh.flush()
    STATE["lines"] += len(rows)


# ---------------------------------------------------------------- 列表获取

def get_okx_usdt_swaps():
    j = http_get(OKX_BASE + "/api/v5/public/instruments", {"instType": "SWAP"})
    insts = [i["instId"] for i in j.get("data", [])
             if i.get("instId", "").endswith("-USDT-SWAP")]
    insts.sort()
    return insts


def get_okx_top150_by_turnover():
    j = http_get(OKX_BASE + "/api/v5/market/tickers", {"instType": "SWAP"})
    rows = []
    for t in j.get("data", []):
        inst = t.get("instId", "")
        if not inst.endswith("-USDT-SWAP"):
            continue
        try:
            # volCcy24h 是币本位量,乘 last 得到 USDT 成交额再排序
            turnover = float(t.get("volCcy24h") or 0) * float(t.get("last") or 0)
        except ValueError:
            turnover = 0.0
        rows.append((turnover, inst))
    rows.sort(reverse=True)
    return [inst for _, inst in rows[:150]]


def get_bybit_linear_usdt_symbols():
    symbols = []
    cursor = ""
    while True:
        params = {"category": "linear", "limit": "1000"}
        if cursor:
            params["cursor"] = cursor
        j = http_get(BYBIT_BASE + "/v5/market/instruments-info", params)
        if j.get("retCode") != 0:
            log("bybit instruments retCode=%s msg=%s" % (j.get("retCode"), j.get("retMsg")))
            break
        result = j.get("result", {})
        for it in result.get("list", []):
            if it.get("quoteCoin") == "USDT" and it.get("contractType") == "LinearPerpetual":
                symbols.append(it["symbol"])
        cursor = result.get("nextPageCursor") or ""
        if not cursor:
            break
    symbols.sort()
    return symbols


# ---------------------------------------------------------------- 各任务抓取

def fetch_okx_candles(inst, bar, outdir, cutoff_ms):
    path = os.path.join(outdir, inst + ".jsonl")
    if os.path.exists(path + ".done"):
        STATE["done"] += 1
        return
    STATE["current"] = "%s:%s" % (os.path.basename(outdir), inst)
    after = oldest_ts_in_file(path, lambda rec: rec["data"][0])
    pages = 0
    with open(path, "a") as fh:
        while True:
            params = {"instId": inst, "bar": bar, "limit": "100"}
            if after is not None:
                params["after"] = str(after)
            j = http_get(OKX_BASE + "/api/v5/market/history-candles", params)
            if j.get("code") != "0":
                log("okx candles %s %s code=%s msg=%s -> stop" % (inst, bar, j.get("code"), j.get("msg")))
                break
            rows = j.get("data") or []
            if not rows:
                break
            write_rows(fh, rows)
            after = int(rows[-1][0])   # 返回按时间倒序,末条最老
            pages += 1
            progress_log()
            if after <= cutoff_ms:
                break
            if TRIAL and pages >= TRIAL_MAX_PAGES:
                break
    mark_done(path)
    STATE["done"] += 1


def fetch_okx_funding(inst, outdir):
    path = os.path.join(outdir, inst + ".jsonl")
    if os.path.exists(path + ".done"):
        STATE["done"] += 1
        return
    STATE["current"] = "okx_funding:%s" % inst
    after = oldest_ts_in_file(path, lambda rec: rec["data"]["fundingTime"])
    pages = 0
    with open(path, "a") as fh:
        while True:
            params = {"instId": inst, "limit": "100"}
            if after is not None:
                params["after"] = str(after)
            j = http_get(OKX_BASE + "/api/v5/public/funding-rate-history", params)
            if j.get("code") != "0":
                log("okx funding %s code=%s msg=%s -> stop" % (inst, j.get("code"), j.get("msg")))
                break
            rows = j.get("data") or []
            if not rows:
                break
            write_rows(fh, rows)
            after = min(int(r["fundingTime"]) for r in rows)
            pages += 1
            progress_log()
            if TRIAL and pages >= TRIAL_MAX_PAGES:
                break
    mark_done(path)
    STATE["done"] += 1


def fetch_bybit_funding(symbol, outdir, cutoff_ms):
    path = os.path.join(outdir, symbol + ".jsonl")
    if os.path.exists(path + ".done"):
        STATE["done"] += 1
        return
    STATE["current"] = "bybit_funding:%s" % symbol
    oldest = oldest_ts_in_file(path, lambda rec: rec["data"]["fundingRateTimestamp"])
    end_time = (oldest - 1) if oldest else None
    pages = 0
    with open(path, "a") as fh:
        while True:
            params = {"category": "linear", "symbol": symbol, "limit": "200"}
            if end_time is not None:
                params["endTime"] = str(end_time)
            j = http_get(BYBIT_BASE + "/v5/market/funding/history", params)
            if j.get("retCode") != 0:
                log("bybit funding %s retCode=%s msg=%s -> stop" % (symbol, j.get("retCode"), j.get("retMsg")))
                break
            rows = j.get("result", {}).get("list") or []
            if not rows:
                break
            write_rows(fh, rows)
            oldest = min(int(r["fundingRateTimestamp"]) for r in rows)
            pages += 1
            progress_log()
            if oldest <= cutoff_ms:
                break
            end_time = oldest - 1
            if TRIAL and pages >= TRIAL_MAX_PAGES:
                break
    mark_done(path)
    STATE["done"] += 1


# ---------------------------------------------------------------- 主流程

def main():
    for sub in ("okx_1h", "okx_5m", "bybit_funding", "okx_funding"):
        d = os.path.join(HIST_DIR, sub)
        if not os.path.isdir(d):
            os.makedirs(d)

    log("mode: %s" % ("TRIAL(3币,每任务<=3页,不写.done)" if TRIAL else "FULL"))
    log("collecting instrument lists ...")

    okx_swaps = get_okx_usdt_swaps()
    top150 = get_okx_top150_by_turnover()
    bybit_syms = get_bybit_linear_usdt_symbols()

    if TRIAL:
        okx_swaps = [i for i in TRIAL_INSTS if i in okx_swaps] or TRIAL_INSTS
        top150 = list(okx_swaps)
        bybit_syms = [s for s in TRIAL_BYBIT if s in bybit_syms] or TRIAL_BYBIT

    n_1h, n_5m, n_bf, n_of = len(okx_swaps), len(top150), len(bybit_syms), len(okx_swaps)
    STATE["total"] = n_1h + n_5m + n_bf + n_of

    # ---- 磁盘/耗时估算先行 ----
    hours_since_2024 = (NOW_MS - CUTOFF_1H_MS) / 3600000.0
    est_1h_lines = int(n_1h * hours_since_2024 * 0.7)          # 平均上市时长打 7 折
    est_5m_lines = n_5m * 60 * 288
    est_bf_lines = n_bf * 365 * 3
    est_of_lines = n_of * 90 * 3
    est_lines = est_1h_lines + est_5m_lines + est_bf_lines + est_of_lines
    est_gb = est_lines * 170 / 1e9                             # ~170B/行
    est_reqs = (est_1h_lines + est_5m_lines + est_of_lines) / 100.0 + est_bf_lines / 200.0
    est_hours = est_reqs / 5.0 / 3600.0
    st = os.statvfs(BASE_DIR)
    free_gb = st.f_bavail * st.f_frsize / 1e9

    log("estimate: okx_1h %d 币 * ~%.0f 根 = ~%.1fM 行; okx_5m %d 币 * 17280 根 = ~%.1fM 行"
        % (n_1h, hours_since_2024, est_1h_lines / 1e6, n_5m, est_5m_lines / 1e6))
    log("estimate: bybit_funding %d 币 ~%.2fM 行; okx_funding %d 币 ~%.2fM 行"
        % (n_bf, est_bf_lines / 1e6, n_of, est_of_lines / 1e6))
    log("estimate: total ~%.1fM 行, ~%.1f GB JSONL ; 磁盘可用 %.1f GB -> %s"
        % (est_lines / 1e6, est_gb, free_gb, "OK" if free_gb > est_gb * 3 else "偏紧,注意"))
    log("estimate: ~%d 请求 @5req/s -> 约 %.1f 小时跑完" % (int(est_reqs), est_hours))

    # ---- 按 a/b/c/d 顺序执行 ----
    log("task a: okx_1h, %d instruments" % n_1h)
    for inst in okx_swaps:
        fetch_okx_candles(inst, "1H", os.path.join(HIST_DIR, "okx_1h"), CUTOFF_1H_MS)
    progress_log(force=True)

    log("task b: okx_5m top150, %d instruments" % n_5m)
    for inst in top150:
        fetch_okx_candles(inst, "5m", os.path.join(HIST_DIR, "okx_5m"), CUTOFF_5M_MS)
    progress_log(force=True)

    log("task c: bybit_funding, %d symbols" % n_bf)
    for sym in bybit_syms:
        fetch_bybit_funding(sym, os.path.join(HIST_DIR, "bybit_funding"), CUTOFF_BYBIT_FUND_MS)
    progress_log(force=True)

    log("task d: okx_funding, %d instruments" % n_of)
    for inst in okx_swaps:
        fetch_okx_funding(inst, os.path.join(HIST_DIR, "okx_funding"))
    progress_log(force=True)

    log("ALL DONE. done %d/%d, lines %d, requests %d, elapsed %.1f h"
        % (STATE["done"], STATE["total"], STATE["lines"], STATE["requests"],
           (time.time() - STATE["start"]) / 3600.0))


if __name__ == "__main__":
    main()
