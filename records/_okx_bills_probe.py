#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""OKX 账单取证(只读;2026-07-07 20:26 BJ 权益骤降 42U 事件)。
查:①交易账户流水 /account/bills(type=1划转) ②资金账户流水 /asset/bills
   ③提现记录 /asset/withdrawal-history ④当前 totalEq。
绝不打印密钥;只打印与事件相关的行。"""
import base64
import hashlib
import hmac
import json
import urllib.request
from datetime import datetime, timezone

import yaml

sec = yaml.safe_load(open("/root/lingxi/secret.yaml"))
ok = sec.get("okx") or {}
KEY = ok.get("api") or ""
SECRET = ok.get("secret") or ""
PASS = str(ok.get("passphrase") or "")
BASE = "https://www.okx.com"


def get(path):
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
    sig = base64.b64encode(hmac.new(SECRET.encode(), (ts + "GET" + path).encode(), hashlib.sha256).digest()).decode()
    req = urllib.request.Request(BASE + path, headers={
        "OK-ACCESS-KEY": KEY, "OK-ACCESS-SIGN": sig, "OK-ACCESS-TIMESTAMP": ts,
        "OK-ACCESS-PASSPHRASE": PASS, "Content-Type": "application/json",
        "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) lingxi-diag/1.0"})
    return json.load(urllib.request.urlopen(req, timeout=15))


def ms2t(ms):
    try:
        return datetime.utcfromtimestamp(int(ms) / 1000).strftime("%m-%d %H:%M:%S")
    except (ValueError, TypeError):
        return str(ms)


print("== ① 当前交易账户 totalEq ==")
r = get("/api/v5/account/balance?ccy=USDT")
for d in r.get("data", []):
    print("  totalEq=%s USDT明细=%s" % (d.get("totalEq"), [
        {k: x.get(k) for k in ("ccy", "eq", "availBal", "frozenBal")} for x in d.get("details", [])]))

print("== ② 交易账户流水(最近50,重点 type=1 划转) ==")
r = get("/api/v5/account/bills?limit=50")
for d in r.get("data", []):
    if d.get("type") in ("1", "2") or abs(float(d.get("balChg") or 0)) >= 5:
        print("  %s type=%s subType=%s ccy=%s balChg=%s bal=%s" % (
            ms2t(d.get("ts")), d.get("type"), d.get("subType"), d.get("ccy"), d.get("balChg"), d.get("bal")))

print("== ③ 资金账户流水(最近20) ==")
r = get("/api/v5/asset/bills?limit=20")
for d in r.get("data", []):
    print("  %s type=%s ccy=%s balChg=%s bal=%s" % (
        ms2t(d.get("ts")), d.get("type"), d.get("ccy"), d.get("balChg"), d.get("bal")))

print("== ④ 提现记录(最近10) ==")
r = get("/api/v5/asset/withdrawal-history?limit=10")
for d in r.get("data", []):
    print("  %s ccy=%s amt=%s state=%s to=%s" % (
        ms2t(d.get("ts")), d.get("ccy"), d.get("amt"), d.get("state"), (d.get("to") or "")[:12] + "..."))
if not r.get("data"):
    print("  (近期无提现记录)")
