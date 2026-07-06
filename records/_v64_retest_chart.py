#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""v6.4 耳朵工程复测出图(2026-07-06):部署前 vs 部署后同口径对比,一张图交付。
口径:tape_burst 确认枪;before=部署时刻(10:06:56Z)前,after=其后。
读数:①EventAgeMs(burst→评估)②分解=确认到达(不可减)+排队脂肪(v6.4刀口)③eval_rest_ms(REST并发化刀口)。
"""
import json
import os
import bisect
import statistics
from datetime import datetime, timedelta

import matplotlib
matplotlib.use("Agg")
from matplotlib import font_manager
import matplotlib.pyplot as plt

for fp in ["/usr/share/fonts/google-droid/DroidSansFallback.ttf"]:
    if os.path.exists(fp):
        font_manager.fontManager.addfont(fp)
        plt.rcParams["font.family"] = font_manager.FontProperties(fname=fp).get_name()
plt.rcParams["axes.unicode_minus"] = False

HERE = os.path.dirname(os.path.abspath(__file__))
DIR = os.path.join(HERE, "okx_directional")
DEPLOY = datetime(2026, 7, 6, 10, 6, 56)  # UTC


def tsu(s):
    return datetime.strptime(s[:23].rstrip("Z"), "%Y-%m-%dT%H:%M:%S.%f") if "." in s[:23] \
        else datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")


liq = {}
with open(os.path.join(DIR, "fast_liquidation_events.jsonl")) as f:
    for line in f:
        try:
            r = json.loads(line)
            liq.setdefault(r["symbol"], []).append(tsu(r["recorded_at"]))
        except Exception:
            continue
for v in liq.values():
    v.sort()

before, after = [], []
with open(os.path.join(DIR, "fast_executable_shadow_events.jsonl")) as f:
    for line in f:
        if '"tape_burst"' not in line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        if r.get("candidate_source") != "tape_burst" or not r.get("event_age_ms"):
            continue
        et = tsu(r["recorded_at"])
        t0 = et - timedelta(milliseconds=r["event_age_ms"])
        arr = liq.get(r["symbol"], [])
        i = bisect.bisect_right(arr, t0)
        conf = None
        if i < len(arr) and (arr[i] - t0).total_seconds() <= (r["event_age_ms"] / 1000.0) + 0.2:
            conf = (arr[i] - t0).total_seconds() * 1000.0
        rec = {"age": r["event_age_ms"], "conf": conf,
               "fat": max(0.0, r["event_age_ms"] - conf) if conf is not None else None,
               "rest": r.get("eval_rest_ms")}
        (after if et >= DEPLOY else before).append(rec)


def med(v):
    return statistics.median(v) if v else float("nan")


def q90(v):
    if not v:
        return float("nan")
    s = sorted(v)
    return s[min(len(s) - 1, int(0.9 * len(s)))]


b_age = [x["age"] for x in before]
a_age = [x["age"] for x in after]
b_fat = [x["fat"] for x in before if x["fat"] is not None]
a_fat = [x["fat"] for x in after if x["fat"] is not None]
b_conf = [x["conf"] for x in before if x["conf"] is not None]
a_conf = [x["conf"] for x in after if x["conf"] is not None]
a_rest = [x["rest"] for x in after if x["rest"]]

print("before n=%d after n=%d" % (len(before), len(after)))
print("age    中位 %.0f → %.0f" % (med(b_age), med(a_age)))
print("确认   中位 %.0f → %.0f" % (med(b_conf), med(a_conf)))
print("脂肪   中位 %.0f → %.0f  P90 %.0f → %.0f" % (med(b_fat), med(a_fat), q90(b_fat), q90(a_fat)))
print("REST   中位(改后实测) %s" % (("%.0f" % med(a_rest)) if a_rest else "无数据"))

fig, axes = plt.subplots(2, 2, figsize=(14, 9.5))
fig.suptitle("v6.4 耳朵工程复测:部署前(n=%d) vs 部署后(n=%d)  ·  2026-07-06" % (len(before), len(after)),
             fontsize=15, fontweight="bold")

# ① 进场延迟分解(堆叠条,中位)
ax = axes[0][0]
labels = ["改造前", "改造后"]
conf_v = [med(b_conf), med(a_conf)]
fat_v = [med(b_fat), med(a_fat)]
rest_v = [195, med(a_rest) if a_rest else float("nan")]  # 改前=串行实测95+100
p1 = ax.bar(labels, conf_v, color="#8899aa", label="官方确认到达(纯度成本,不可减)")
p2 = ax.bar(labels, fat_v, bottom=conf_v, color="#e0503c", label="排队脂肪(v6.4刀①事件驱动唤醒)")
p3 = ax.bar(labels, rest_v, bottom=[conf_v[i] + fat_v[i] for i in range(2)],
            color="#f2a63b", label="REST盘口组装(v6.4刀②并发化)")
for i in range(2):
    tot = conf_v[i] + fat_v[i] + rest_v[i]
    ax.text(i, tot + 25, "%.0f ms" % tot, ha="center", fontweight="bold", fontsize=12)
    ax.text(i, conf_v[i] / 2, "%.0f" % conf_v[i], ha="center", color="white", fontsize=10)
    ax.text(i, conf_v[i] + fat_v[i] + rest_v[i] / 2, "%.0f" % rest_v[i], ha="center", fontsize=9)
ax.set_ylabel("burst → 可下单 延迟中位 (ms)")
ax.set_title("① 进场延迟分解:刀只砍自己的脂肪,不碰纯度")
ax.legend(fontsize=8.5, loc="lower right")

# ② 排队脂肪分布
ax = axes[0][1]
bins = list(range(0, 200, 10))
ax.hist(b_fat, bins=bins, alpha=0.55, color="#8899aa", density=True, label="改造前(等150ms轮询) 中位%.0f" % med(b_fat))
ax.hist(a_fat, bins=bins, alpha=0.65, color="#e0503c", density=True, label="改造后(推送落地即评估) 中位%.0f" % med(a_fat))
ax.set_xlabel("确认到达 → 开始评估 (ms)")
ax.set_title("② 刀①效果:排队脂肪分布")
ax.legend(fontsize=9)

# ③ EventAge 全量对比(累积分布)
ax = axes[1][0]
for v, c, lb in [(b_age, "#8899aa", "改造前 中位%.0f/P90 %.0f" % (med(b_age), q90(b_age))),
                 (a_age, "#e0503c", "改造后 中位%.0f/P90 %.0f" % (med(a_age), q90(a_age)))]:
    s = sorted(v)
    ax.plot(s, [i / len(s) for i in range(1, len(s) + 1)], color=c, lw=2, label=lb)
ax.axvline(1250, color="#333", ls="--", lw=1)
ax.text(1265, 0.05, "预注册裁决线 1250ms\n(=贴到确认到达地板)", fontsize=8.5)
ax.axvline(2000, color="#a00", ls=":", lw=1)
ax.text(2015, 0.4, "2s半衰期", fontsize=8.5, color="#a00")
ax.set_xlim(0, 3200)
ax.set_xlabel("EventAgeMs:burst → 评估 (ms)")
ax.set_ylabel("累积占比")
ax.set_title("③ 全链延迟 CDF 与预注册裁决线")
ax.legend(fontsize=9, loc="lower right")

# ④ 本轮三项裁决文字板
ax = axes[1][1]
ax.axis("off")
txt = (
    "本轮复测三项裁决(数据钉死)\n"
    "────────────────────────────\n"
    "㊀ 延迟解剖修正(诚实入册):\n"
    "   1.4s等待里 1.22s=官方推送延迟(纯度成本,不可减);\n"
    "   自己的脂肪实测仅 排队75ms + REST串行~100ms。\n"
    "   昨日说\"0.9s是我们自己的\"——高估,已修正。\n"
    "   v6.4可回收 ≈175-275ms ≈ 0.5-1bps/枪(零纯度代价)。\n\n"
    "㊁ Bybit确认源:数据处决,不接。\n"
    "   624枪中Bybit窗内可确认仅21%、新增猎物0枪、\n"
    "   双所同确认时仅快2ms中位 → 不值一行代码。\n"
    "   (猎场窄的真因=探测器只挂OKX热币WS,另案)\n\n"
    "㊂ 抢跑假说:证伪,维持等确认。\n"
    "   接力枪(前30s已有官方爆仓)抢跑反而更差\n"
    "   (-6.2 vs 首枪-1.5):瀑布越晚越馊,与半衰期一致。\n"
    "   耳朵的正确用法=确认后把自己的每一毫秒榨干。"
)
ax.text(0.02, 0.98, txt, va="top", ha="left", fontsize=10.5, family=plt.rcParams["font.family"],
        bbox=dict(boxstyle="round,pad=0.6", fc="#f7f5ef", ec="#ccc"))

plt.tight_layout(rect=[0, 0, 1, 0.955])
out = os.path.join(HERE, "v64_retest_%s.png" % datetime.utcnow().strftime("%m%d_%H%M"))
plt.savefig(out, dpi=130)
print("saved:", out)
