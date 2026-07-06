#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""MEXC 换战场前置检验之二:磁带纯度采集器(2026-07-06,record-only,零成本无账户)。

同时订阅 MEXC(sub.deal)与 OKX(trades)同一批长尾暴动币的公共成交流,
逐笔落盘 records/crossvenue/mexc_tape_probe.jsonl。
分析(另跑 _mexc_tape_verdict.py):笔量分布/买卖交替签名/间隔分布/整数手占比——
刷量磁带 vs 真磁带的结构差。Python 3.6 + websockets 9.x。
"""
import asyncio
import json
import time
import os
import urllib.request
from datetime import datetime

import websockets

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "crossvenue", "mexc_tape_probe.jsonl")

# census 暴动榜 ∩ 双所上市(07-06 深度探针核对过的币,去掉 NES 异常)
SYMS = ["CAP", "HMSTR", "SLX", "LAB", "O", "RESOLV", "ARX", "UB", "H", "BEAT", "GIGGLE", "TRB"]


def get(url):
    req = urllib.request.Request(url, headers={"User-Agent": "lingxi-probe/1.0"})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())


def load_meta():
    mx = {}
    for c in get("https://contract.mexc.com/api/v1/contract/detail").get("data", []):
        if c.get("quoteCoin") == "USDT" and c.get("futureType") == 1:
            mx[c["symbol"]] = float(c.get("contractSize", 0))
    ok = {}
    for c in get("https://www.okx.com/api/v5/public/instruments?instType=SWAP").get("data", []):
        if c.get("settleCcy") == "USDT":
            ok[c["instId"]] = float(c.get("ctVal", 0))
    return mx, ok


MX_META, OK_META = load_meta()
_fh = open(OUT, "a", buffering=1)


def emit(venue, sym, px, notional, side, ts_ms):
    _fh.write(json.dumps({
        "recorded_at": datetime.utcnow().isoformat() + "Z",
        "venue": venue, "symbol": sym, "px": px,
        "notional": round(notional, 2), "side": side, "ts_ms": ts_ms,
    }) + "\n")


async def mexc_loop():
    while True:
        try:
            async with websockets.connect("wss://contract.mexc.com/edge", ping_interval=None) as ws:
                for s in SYMS:
                    await ws.send(json.dumps({"method": "sub.deal", "param": {"symbol": s + "_USDT"}}))
                    await asyncio.sleep(0.1)
                last_ping = time.time()
                while True:
                    if time.time() - last_ping > 15:
                        await ws.send(json.dumps({"method": "ping"}))
                        last_ping = time.time()
                    try:
                        msg = await asyncio.wait_for(ws.recv(), timeout=5)
                    except asyncio.TimeoutError:
                        continue
                    d = json.loads(msg)
                    if d.get("channel") != "push.deal":
                        continue
                    mxsym = d.get("symbol", "")
                    cs = MX_META.get(mxsym, 0)
                    sym = mxsym.replace("_USDT", "-USDT")
                    data = d.get("data")
                    rows = data if isinstance(data, list) else [data]
                    for r in rows:
                        if not isinstance(r, dict):
                            continue
                        px = float(r.get("p", 0))
                        v = float(r.get("v", 0))
                        side = "buy" if r.get("T") == 1 else "sell"
                        emit("mexc", sym, px, px * v * cs, side, int(r.get("t", 0)))
        except Exception as ex:
            print("[mexc] 断线重连: %s" % ex, flush=True)
            await asyncio.sleep(5)


async def okx_loop():
    while True:
        try:
            async with websockets.connect("wss://ws.okx.com:8443/ws/v5/public", ping_interval=None) as ws:
                args = [{"channel": "trades", "instId": s + "-USDT-SWAP"} for s in SYMS]
                await ws.send(json.dumps({"op": "subscribe", "args": args}))
                last_ping = time.time()
                while True:
                    if time.time() - last_ping > 20:
                        await ws.send("ping")
                        last_ping = time.time()
                    try:
                        msg = await asyncio.wait_for(ws.recv(), timeout=5)
                    except asyncio.TimeoutError:
                        continue
                    if msg == "pong":
                        continue
                    d = json.loads(msg)
                    if d.get("arg", {}).get("channel") != "trades" or "data" not in d:
                        continue
                    inst = d["arg"]["instId"]
                    ctval = OK_META.get(inst, 0)
                    sym = inst.replace("-SWAP", "")
                    for r in d["data"]:
                        px = float(r["px"])
                        sz = float(r["sz"])
                        emit("okx", sym, px, px * sz * ctval, r["side"], int(r["ts"]))
        except Exception as ex:
            print("[okx] 断线重连: %s" % ex, flush=True)
            await asyncio.sleep(5)


loop = asyncio.get_event_loop()
loop.create_task(mexc_loop())
loop.create_task(okx_loop())
print("磁带纯度采集器启动: %d 币 × 双所 → %s" % (len(SYMS), OUT), flush=True)
loop.run_forever()
