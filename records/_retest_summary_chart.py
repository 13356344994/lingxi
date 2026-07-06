#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""复测总图(2026-07-06):最新讨论落实后,用存量数据(642确认枪/442可对照)复测,一张图交付。
数字来源:_tape_prefire_study.py / _latency_anatomy.py / _bybit_confirm_study.py / _secondgen_watch.log 当轮。
"""
import os
from datetime import datetime

import matplotlib
matplotlib.use("Agg")
from matplotlib import font_manager
import matplotlib.pyplot as plt

FP = "/usr/share/fonts/google-droid/DroidSansFallback.ttf"
font_manager.fontManager.addfont(FP)
plt.rcParams["font.family"] = font_manager.FontProperties(fname=FP).get_name()
plt.rcParams["axes.unicode_minus"] = False

fig, axes = plt.subplots(2, 2, figsize=(15, 10))
fig.suptitle("LINGXI 复测总图 · 最新讨论落实后(存量 642 确认枪)· 2026-07-06 18:50 BJ",
             fontsize=15, fontweight="bold")

# ① 耳朵价值复测:抢跑 vs 现枪(毛中位 bps)
ax = axes[0][0]
cats = ["全体\nn=442", "接力枪\nn=121", "首枪\nn=321"]
a_vals = [-3.8, -7.4, -2.3]
b_vals = [-6.9, -10.4, -4.9]
x = range(3)
w = 0.36
ax.bar([i - w / 2 for i in x], a_vals, w, color="#3a7bd5", label="A 抢跑(探测价 t0 进,理论上界)")
ax.bar([i + w / 2 for i in x], b_vals, w, color="#c0504d", label="B 现枪(等确认 ~t0+1.4s 进,现状)")
for i in x:
    ax.text(i - w / 2, a_vals[i] - 0.5, "%+.1f" % a_vals[i], ha="center", va="top", fontsize=10, color="#3a7bd5")
    ax.text(i + w / 2, b_vals[i] - 0.5, "%+.1f" % b_vals[i], ha="center", va="top", fontsize=10, color="#c0504d")
ax.set_xticks(list(x))
ax.set_xticklabels(cats)
ax.axhline(0, color="#333", lw=0.8)
ax.set_ylabel("trail 毛中位 (bps,无滑点=上界)")
ax.set_ylim(-13, 2)
ax.set_title("① 耳朵价值复测:1.4s 等待每枪付 ~3bps 中位\n(接力枪抢跑假说证伪:接力桶两边都更差,瀑布越晚越馊)")
ax.legend(fontsize=9, loc="lower left")

# ② 延迟解剖:1.4s 里谁的肉
ax = axes[0][1]
labels = ["现状\n(复测 n=642)", "v6.4 改造后\n(已部署 18:06)"]
conf = [1232, 1232]
fat = [74, 5]
rest = [195, 100]
ax.bar(labels, conf, color="#8899aa", label="官方确认到达 1232ms(纯度成本,不可减)")
ax.bar(labels, fat, bottom=conf, color="#e0503c", label="排队脂肪(刀①:推送落地即评估,74→~5)")
ax.bar(labels, rest, bottom=[conf[i] + fat[i] for i in range(2)], color="#f2a63b",
       label="REST盘口组装(刀②:串行改并发,195→~100)")
for i in range(2):
    tot = conf[i] + fat[i] + rest[i]
    ax.text(i, tot + 20, "%d ms" % tot, ha="center", fontweight="bold", fontsize=12)
    ax.text(i, conf[i] / 2, "%d" % conf[i], ha="center", color="white", fontsize=10)
ax.set_ylim(0, 1780)
ax.annotate("省 ~164ms ≈ 0.3-0.5bps/枪(零纯度代价)", xy=(1, 1360), xytext=(0.08, 1650),
            fontsize=10, color="#a03020", arrowprops=dict(arrowstyle="->", color="#a03020"))
ax.text(0.5, 400, "诚实修正:昨说\"0.9s是我们的脂肪\"——高估。\n实测自身仅 74+195ms,其余全是官方推送延迟。",
        ha="center", fontsize=9.5, color="#333",
        bbox=dict(boxstyle="round,pad=0.4", fc="#fff8e0", ec="#ccc"))
ax.set_ylabel("burst → 可下单 (ms,中位)")
ax.set_title("② 延迟解剖:1.4s 里 82% 是官方推送延迟\n可回收的只有自己的 ~270ms → v6.4 两刀已部署")
ax.legend(fontsize=8.5, loc="center right")

# ③ 分拣器判决集漏斗(第二代,since 07-05 18:00Z)
ax = axes[1][0]
stages = ["判决窗确认事件", "海况刹车(不玩)", "无通道/混沌(不玩)", "开枪"]
vals = [429, 344, 11, 4]
colors = ["#3a7bd5", "#8899aa", "#b0b8c0", "#e0503c"]
ypos = range(len(stages))[::-1]
ax.barh(list(ypos), vals, color=colors)
for y, v in zip(ypos, vals):
    ax.text(v + 6, y, "%d" % v, va="center", fontsize=11, fontweight="bold")
ax.set_yticks(list(ypos))
ax.set_yticklabels(stages)
ax.set_xlim(0, 500)
ax.set_title("③ 第二代分拣器判决集(修门+lag海况后复测)\n开枪4 净-22.4;同窗第一代冠军 -20.9/笔 = 不进场价值的反证")
ax.text(240, 0.85, "海况刹车 80%:整窗海况差,\nfail-closed 按设计工作(零亏)。\n判决需海况转好或 07-08 死线处置。",
        fontsize=9.5, bbox=dict(boxstyle="round,pad=0.4", fc="#f0f4f8", ec="#ccc"))

# ④ 三项裁决文字板
ax = axes[1][1]
ax.axis("off")
txt = (
    "本轮复测裁决(全部存量数据钉死)\n"
    "══════════════════════════════\n"
    "㊀ 抢跑假说:证伪,维持等确认。\n"
    "    纯抢跑=赌确认(确认率仅5-10%);接力枪桶更差\n"
    "    (-7.4 vs 首枪-2.3),\"官方已背书再抢\"不成立。\n\n"
    "㊁ Bybit 确认源:数据处决,不接。\n"
    "    642枪中 Bybit 窗内可确认仅 21%、新增猎物 0 枪、\n"
    "    双所同到时仅快 12ms 中位 → 不值一行代码。\n"
    "    (猎场窄真因=探测器只挂 OKX 热币 WS,另案)\n\n"
    "㊂ v6.4 两刀已部署(18:06,build/test/push 全绿):\n"
    "    刀①事件驱动唤醒:确认落地即评估(-74ms)\n"
    "    刀②REST 并发化:盘口+深度同时拉(-~100ms)\n"
    "    部署后首两枪实测 eval_rest=164/68ms,通路已生效;\n"
    "    新字段 eval_rest_ms 落 shadow,后续自动记账。\n\n"
    "结论:耳朵的极致=确认后自己的每一毫秒榨干,\n"
    "剩余 1232ms 是纯度的价格——不付它就要去赌 5% 确认率。"
)
ax.text(0.0, 1.0, txt, va="top", ha="left", fontsize=10.5,
        bbox=dict(boxstyle="round,pad=0.6", fc="#f7f5ef", ec="#ccc"))

plt.tight_layout(rect=[0, 0, 1, 0.94])
out = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                   "retest_summary_%s.png" % datetime.utcnow().strftime("%m%d_%H%M"))
plt.savefig(out, dpi=130)
print("saved:", out)
