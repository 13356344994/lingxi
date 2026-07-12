#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""OKX rubik 主动买卖量 / 多空人数比 历史采集(给"方向钥匙"研究)。

===== 端点探测结论(2026-07-09 实测,records/_rubik_probe.py)=====
1) /api/v5/rubik/stat/taker-volume?ccy=X&instType=CONTRACTS&period=...
   返回格式: data = [[ts_ms, sellVol, buyVol], ...] 时间倒序。
   period=1H: 默认一页 720 行 = 正好 30 天,即保留窗口 ~30 天;
              end 早于窗口起点 -> code=50030 "Illegal time range"。
   period=5m: 默认一页 576 行 = 正好 48 小时,保留窗口只有 ~2 天;
              end=now-7d 即报 50030。
2) /api/v5/rubik/stat/contracts/long-short-account-ratio?ccy=X&period=...
   返回格式: data = [[ts_ms, longShortRatio], ...] 时间倒序。
   保留窗口与 taker-volume 完全一致:1H ~30 天,5m ~2 天。
3) ccy 支持面:BTC / ETH / TRIA(长尾)均正常返回,长尾币支持良好。
   不支持的币返回非 0 code,按"该币无数据"跳过并记日志。
4) 边界行为:end 早于保留窗口起点返回 code=50030(不是空数组),
   翻页时把 50030 视为"已到底"正常收尾。
5) 所有请求必须带 User-Agent,否则 403(与 _history_fetch.py 一致)。

结论:所谓"长历史"实际只有 30 天(1H)/ 2 天(5m),每币每任务 1-2 页
即可到底;全量 top200 约 1200-1800 个请求,几分钟跑完。脚本仍按
断点续传 + 限频 + 退避的工程标准写,便于日后定期重跑积累历史。

输出(每行 = {"data": 原始数组, "fetched_at": ms}):
  records/history/rubik_taker_1h/{ccy}.jsonl
  records/history/rubik_taker_5m/{ccy}.jsonl
  records/history/rubik_lsratio_1h/{ccy}.jsonl

工程特性:
  - 全局限频 <=2 req/s(预算 3 req/s 内留余量;rubik 单端点限频约
    5 次/2s,试跑时 2.5/s 与 _history_fetch.py 并跑仍偶发 429)。
    与 _history_fetch.py(<=5 req/s)合计 <=7 req/s < 8 req/s。
  - 429/403/网络错误指数退避 1s->2s->...->60s
  - 断点续传:每币每任务完成 touch {file}.done;重启跳过;
    未完成的从已写文件最老 ts 继续向前翻
  - --trial:只跑 3 个币,不写 .done

用法:
  python3 records/_rubik_fetch.py            # 全量 top200
  python3 records/_rubik_fetch.py --trial
"""

import json
import os
import sys
import time

import requests

BASE_DIR = os.path.dirname(os.path.abspath(__file__))          # records/
HIST_DIR = os.path.join(BASE_DIR, "history")

OKX_BASE = "https://www.okx.com"

HEADERS = {
    "User-Agent": "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0",
    "Accept": "application/json",
}

RATE_INTERVAL = 0.5          # 全局 <=2 req/s(实测 2.5/s 与 _history_fetch 并跑仍偶发 429)
BACKOFF_MAX = 60.0
LOG_INTERVAL = 30.0
TOP_N = 200

# end 早于保留窗口起点时 OKX 返回的错误码,视为"翻到底"
CODE_ILLEGAL_RANGE = "50030"

TRIAL = "--trial" in sys.argv
TRIAL_CCYS = ["BTC", "ETH", "TRIA"]

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
    """全局限频 GET;429/403/网络错误指数退避;返回解析后的 JSON。"""
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


def oldest_ts_in_file(path):
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
                    ts = int(json.loads(line)["data"][0])
                except Exception:
                    continue
                if oldest is None or ts < oldest:
                    oldest = ts
    except Exception as e:
        log("warn: scan %s failed: %s" % (path, e))
    return oldest


def mark_done(path):
    if TRIAL:
        return
    with open(path + ".done", "w") as f:
        f.write(str(int(time.time())))


def write_rows(fh, rows):
    fetched_at = int(time.time() * 1000)
    for row in rows:
        fh.write(json.dumps({"data": row, "fetched_at": fetched_at},
                            separators=(",", ":")) + "\n")
    fh.flush()
    STATE["lines"] += len(rows)


def get_top_ccys(n):
    """24h 成交额(volCcy24h*last, USDT 计)前 n 的 USDT 永续,去后缀得 ccy。"""
    j = http_get(OKX_BASE + "/api/v5/market/tickers", {"instType": "SWAP"})
    rows = []
    for t in j.get("data", []):
        inst = t.get("instId", "")
        if not inst.endswith("-USDT-SWAP"):
            continue
        try:
            turnover = float(t.get("volCcy24h") or 0) * float(t.get("last") or 0)
        except ValueError:
            turnover = 0.0
        rows.append((turnover, inst[:-len("-USDT-SWAP")]))
    rows.sort(reverse=True)
    return [ccy for _, ccy in rows[:n]]


def fetch_rubik(ccy, url, extra_params, outdir):
    """向前翻页到底:end=最老ts-1,直到空数组或 50030。"""
    path = os.path.join(outdir, ccy + ".jsonl")
    if os.path.exists(path + ".done"):
        STATE["done"] += 1
        return
    STATE["current"] = "%s:%s" % (os.path.basename(outdir), ccy)
    oldest = oldest_ts_in_file(path)
    with open(path, "a") as fh:
        while True:
            params = dict(extra_params)
            params["ccy"] = ccy
            if oldest is not None:
                params["end"] = str(oldest - 1)
            j = http_get(OKX_BASE + url, params)
            code = j.get("code")
            if code == CODE_ILLEGAL_RANGE:
                break                        # 已翻出保留窗口 -> 到底
            if code != "0":
                log("%s %s code=%s msg=%s -> stop" %
                    (os.path.basename(outdir), ccy, code, j.get("msg")))
                break
            rows = j.get("data") or []
            if not rows:
                break
            write_rows(fh, rows)
            new_oldest = min(int(r[0]) for r in rows)
            if oldest is not None and new_oldest >= oldest:
                break                        # 防御:没有前进就停
            oldest = new_oldest
            progress_log()
    mark_done(path)
    STATE["done"] += 1


TASKS = [
    ("rubik_taker_1h", "/api/v5/rubik/stat/taker-volume",
     {"instType": "CONTRACTS", "period": "1H"}),
    ("rubik_taker_5m", "/api/v5/rubik/stat/taker-volume",
     {"instType": "CONTRACTS", "period": "5m"}),
    ("rubik_lsratio_1h", "/api/v5/rubik/stat/contracts/long-short-account-ratio",
     {"period": "1H"}),
]


def main():
    for sub, _, _ in TASKS:
        d = os.path.join(HIST_DIR, sub)
        if not os.path.isdir(d):
            os.makedirs(d)

    log("mode: %s" % ("TRIAL(3币,不写.done)" if TRIAL else "FULL top%d" % TOP_N))
    ccys = TRIAL_CCYS if TRIAL else get_top_ccys(TOP_N)
    STATE["total"] = len(ccys) * len(TASKS)
    log("ccys: %d 个, 任务 %d 个 (head: %s)" %
        (len(ccys), STATE["total"], ", ".join(ccys[:10])))
    log("estimate: 每任务 1-2 页(1H 窗口仅 30 天 / 5m 仅 2 天), "
        "~%d 请求 @2.5req/s ≈ %.0f 分钟" %
        (STATE["total"] * 2, STATE["total"] * 2 / 2.5 / 60))

    # 5m 窗口只有 2 天且持续滚动丢失,优先抓 5m
    for sub, url, extra in sorted(TASKS, key=lambda t: 0 if "5m" in t[0] else 1):
        for ccy in ccys:
            fetch_rubik(ccy, url, extra, os.path.join(HIST_DIR, sub))
    progress_log(force=True)
    log("ALL DONE. done %d/%d, lines %d, requests %d, elapsed %.1f min"
        % (STATE["done"], STATE["total"], STATE["lines"], STATE["requests"],
           (time.time() - STATE["start"]) / 60.0))


if __name__ == "__main__":
    main()
