#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""临时探测脚本:rubik 端点回溯边界(会被 _rubik_fetch.py 头注释吸收结论)。"""
import json
import time

import requests

H = {"User-Agent": "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0"}
BASE = "https://www.okx.com"
NOW = int(time.time() * 1000)
DAY = 86400 * 1000


def get(path, params):
    time.sleep(0.5)
    r = requests.get(BASE + path, params=params, headers=H, timeout=20)
    try:
        return r.json()
    except Exception:
        return {"code": "HTTP%d" % r.status_code}


def probe(path, base_params, label):
    print("== %s ==" % label)
    # 默认一页多少条
    j = get(path, base_params)
    rows = j.get("data") or []
    print("  default page: code=%s rows=%d newest=%s oldest=%s" %
          (j.get("code"), len(rows),
           rows[0][0] if rows else None, rows[-1][0] if rows else None))
    # 试不同深度的 end
    for days in (7, 15, 30, 60, 90, 180, 365, 730, 1095):
        end = NOW - days * DAY
        p = dict(base_params)
        p["end"] = str(end)
        j = get(path, p)
        rows = j.get("data") or []
        old = None
        if rows:
            old = time.strftime("%Y-%m-%d", time.gmtime(int(rows[-1][0]) / 1000))
        print("  end=now-%4dd: code=%s rows=%3d oldest_row=%s msg=%s" %
              (days, j.get("code"), len(rows), old, (j.get("msg") or "")[:60]))


for ccy in ("BTC", "TRIA"):
    probe("/api/v5/rubik/stat/taker-volume",
          {"ccy": ccy, "instType": "CONTRACTS", "period": "5m"},
          "taker-volume %s 5m" % ccy)
    probe("/api/v5/rubik/stat/taker-volume",
          {"ccy": ccy, "instType": "CONTRACTS", "period": "1H"},
          "taker-volume %s 1H" % ccy)
    probe("/api/v5/rubik/stat/contracts/long-short-account-ratio",
          {"ccy": ccy, "period": "5m"},
          "lsratio %s 5m" % ccy)
    probe("/api/v5/rubik/stat/contracts/long-short-account-ratio",
          {"ccy": ccy, "period": "1H"},
          "lsratio %s 1H" % ccy)
