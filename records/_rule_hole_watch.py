#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""规则洞窗口监视器(2026-07-10;RULE_HOLES #3/#5 的哨兵;nohup 常驻,只读+Telegram)。

每 5min:
  ① funding-rate?instId=ANY 扫 |f|≥0.9×cap 的钉死币 → 新段开启即推送(#3 微实验窗口);
  ② 下架公告页扫新的 "delist perpetual" → 推送(#5 微实验窗口,含 7 天倒计时);
状态持久化 _rule_hole_watch_state.json 防重复推送。只观察不下单。
"""
import json, time, urllib.request, os
from datetime import datetime, timezone

STATE = "/root/lingxi/records/_rule_hole_watch_state.json"
SECRET = "/root/lingxi/secret.yaml"
UA = {"User-Agent": "Mozilla/5.0"}

def get(url):
    req = urllib.request.Request(url, headers=UA)
    with urllib.request.urlopen(req, timeout=15) as r:
        return json.loads(r.read())

def tg(msg):
    tok = cid = None
    try:
        for line in open(SECRET):
            if line.startswith("Telegram_Token:"):
                tok = line.split(":", 1)[1].strip()
            if line.startswith("Telegram_Chat_ID:"):
                cid = line.split(":", 1)[1].strip()
    except Exception:
        pass
    if not tok or not cid:
        return
    try:
        data = json.dumps({"chat_id": cid, "text": msg}).encode()
        req = urllib.request.Request(f"https://api.telegram.org/bot{tok}/sendMessage",
                                     data=data, headers={"Content-Type": "application/json"})
        urllib.request.urlopen(req, timeout=10)
    except Exception as e:
        print("tg fail", e, flush=True)

def load_state():
    try:
        return json.load(open(STATE))
    except Exception:
        return {"pinned": {}, "seen_ann": []}

def save_state(st):
    json.dump(st, open(STATE, "w"))

st = load_state()
print(f"[{datetime.now(timezone.utc):%H:%M}] 规则洞监视器上岗(#3 钉死段 / #5 下架公告)", flush=True)
while True:
    now = datetime.now(timezone.utc)
    try:
        d = get("https://www.okx.com/api/v5/public/funding-rate?instId=ANY")["data"]
        cur = {}
        for r in d:
            fr = float(r.get("fundingRate") or 0)
            cap = float(r.get("maxFundingRate") or 0.01)
            if cap > 0 and abs(fr) >= 0.9 * cap:
                cur[r["instId"]] = fr
        for inst, fr in cur.items():
            if inst not in st["pinned"]:
                msg = (f"🕳 #3 钉死段开启:{inst} f={fr*100:+.2f}%(≥90%cap)\n"
                       f"微实验窗口:OKX 收租腿 5U + Bybit 对冲 5U,持 2-4 期;"
                       f"历史:连≥4期段中位+300~+2226bps")
                print(f"[{now:%m-%d %H:%M}] {msg}", flush=True)
                tg(msg)
        # 结束通知
        for inst in list(st["pinned"]):
            if inst not in cur:
                print(f"[{now:%m-%d %H:%M}] 钉死段结束:{inst}", flush=True)
        st["pinned"] = {k: v for k, v in cur.items()}
    except Exception as e:
        print("funding scan fail", e, flush=True)
    try:
        d = get("https://www.okx.com/api/v5/support/announcements?annType=announcements-delistings")
        for det in (d.get("data") or [{}])[0].get("details", []):
            key = det.get("url") or det.get("title")
            if key in st["seen_ann"]:
                continue
            st["seen_ann"] = (st["seen_ann"] + [key])[-200:]
            title = det.get("title", "")
            if "perpetual" in title.lower() and "delist" in title.lower():
                t = datetime.fromtimestamp(int(det["pTime"]) / 1000, tz=timezone.utc)
                msg = f"🕳 #5 下架公告:{title}({t:%m-%d %H:%M}Z)\n微实验窗口:末2h盯perp vs指数基差,|基差|>1%反向5U持有至TWAP结算(免费+资费清零)"
                print(f"[{now:%m-%d %H:%M}] {msg}", flush=True)
                tg(msg)
    except Exception as e:
        print("ann scan fail", e, flush=True)
    save_state(st)
    time.sleep(300)
