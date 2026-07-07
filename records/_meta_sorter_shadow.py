#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""高维分拣器·影子(章程㉕ v0,2026-07-07 12:30 BJ;record-only,零真钱)。

每次调用记一行"当刻慢状态 → 值班策略族"到 records/meta_sorter_states.jsonl。
状态轴(全部慢变量,允许滞后——分拣器分拣不扣扳机):
  fuel_5m    全市场官方爆仓笔数/5min(死水<20/中间20-40/活跃>=40,占空比实测 55/30/15)
  tide_30m   30min 爆仓净方向(long爆>=2×short爆=跌潮 ebb;反之涨潮 flood;否则 mixed)
  breadth30  全市场 mid 30min 收益中位(bps)——E 族毒州探测器(广跌=单腿多头接崩盘)
值班表 v0(冻结;表是数据不是代码,每行带证据;当前无族持证,duty 仅供反事实记账):
  fuel>=40 & tide=ebb            → fire_cascade  (证据:40h风暴窗全肉;⑭剧本①;未持证)
  fuel>=40 & tide!=ebb           → none          (轧空包③影子0触发未持证)
  fuel<40  & breadth30>-15       → xgap_rev      (证据:07-06 n=627 +9.7/PF1.83;E-v2在考)
  fuel<40  & breadth30<=-15      → none          (E毒州:07-07 UTC02 PF0.31)
  数据不足                        → none          (fail-closed)
格子填充=离线 join:本文件的状态行 × 各族影子成绩(E=_xgap_v2_verdict 口径 trades,
tape=fast_executable_shadow_events),每格 n>=30 双正才谈持证。
"""
import json
import os
import statistics
import time
import calendar
from datetime import datetime

REC = "/root/lingxi/records/okx_directional"
OUT = "/root/lingxi/records/meta_sorter_states.jsonl"


def ep(s):
    base = calendar.timegm(datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").timetuple())
    try:
        return base + float("0." + s[20:].rstrip("Z")) if len(s) > 20 and s[19] == "." else base
    except ValueError:
        return base


def tail_lines(path, nbytes):
    try:
        size = os.path.getsize(path)
        with open(path, "rb") as f:
            if size > nbytes:
                f.seek(size - nbytes)
                f.readline()  # 掐半行
            return f.read().decode("utf-8", "replace").splitlines()
    except OSError:
        return []


now = time.time()

# ---- 燃料 + 潮汐(官方爆仓流,文件小全量 tail 即可)----
fuel_5m, ebb_n, flood_n = 0, 0, 0
for line in tail_lines(REC + "/fast_liquidation_events.jsonl", 2 << 20):
    try:
        d = json.loads(line)
        t = ep(d["recorded_at"])
    except (ValueError, KeyError):
        continue
    age = now - t
    if age <= 300:
        fuel_5m += 1
    if age <= 1800:
        if d.get("pos_side") == "long":
            ebb_n += 1     # 多头爆=被迫卖=跌潮成分
        elif d.get("pos_side") == "short":
            flood_n += 1   # 空头爆=被迫买=涨潮成分
tide = "mixed"
if ebb_n >= 2 * max(flood_n, 1) and ebb_n >= 5:
    tide = "ebb"
elif flood_n >= 2 * max(ebb_n, 1) and flood_n >= 5:
    tide = "flood"

# ---- 市场广度(全市场 mid 标尺,只 tail ~12MB≈35min)----
breadth = None
rows = []
for line in tail_lines(REC + "/fast_market_mids.jsonl", 12 << 20):
    p = line.find('"recorded_at":"')
    if p < 0 or '"backlog' in line:
        continue
    try:
        t = ep(line[p + 15:p + 35].rstrip('"'))
    except ValueError:
        continue
    rows.append((t, line))
if rows:
    t_new = rows[-1][0]
    old = None
    for t, line in rows:
        if t_new - t <= 1860:
            old = (t, line)
            break
    if old and t_new - old[0] >= 1500:
        try:
            m_old = json.loads(old[1]).get("mids", {})
            m_new = json.loads(rows[-1][1]).get("mids", {})
            rets = [(m_new[s] - m_old[s]) / m_old[s] * 1e4
                    for s in m_new if s in m_old and m_old[s] > 0]
            if len(rets) >= 100:
                breadth = statistics.median(rets)
        except ValueError:
            pass

# ---- 值班表 v0(冻结)----
if fuel_5m >= 40:
    duty = "fire_cascade" if tide == "ebb" else "none"
elif breadth is None:
    duty = "none"  # fail-closed:广度不可测不派班
else:
    duty = "xgap_rev" if breadth > -15 else "none"

row = {
    "recorded_at": datetime.utcfromtimestamp(now).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "fuel_5m": fuel_5m,
    "tide_30m": tide, "ebb_n": ebb_n, "flood_n": flood_n,
    "breadth_med_ret30m_bps": round(breadth, 2) if breadth is not None else None,
    "duty": duty,
    "certified": False,  # 当前无族持证;本行仅供反事实记账
}
with open(OUT, "a") as f:
    f.write(json.dumps(row, ensure_ascii=False) + "\n")
print("meta_sorter: fuel=%d tide=%s(ebb%d/flood%d) breadth30=%s duty=%s" % (
    fuel_5m, tide, ebb_n, flood_n,
    "%.1f" % breadth if breadth is not None else "NA", duty))
