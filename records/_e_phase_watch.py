#!/usr/bin/env python3
# E 阶段触发盯盘(只读)。循环检查:
#   - E 阶段真实成交仓 >=30 且盈亏样本都有 -> EWATCH_FULL(出完整报告)
#   - 5 类异常任一 -> EWATCH_ANOMALY(提前报告,不等30)
#   - 运行>=40min 仍未触发 -> EWATCH_LIGHT(一次轻量简报)
#   - 否则 -> EWATCH_HB 心跳
# 纯只读:只读 settlements / 日志 / ps / systemctl;绝不改任何文件或交易。
import json, os, subprocess, time, glob
from datetime import datetime, timezone

E_START = datetime(2026, 6, 29, 15, 29, 31, tzinfo=timezone.utc)  # E阶段起点(taker_fallback_venues=binance 部署)
VEN = ["okx", "bybit", "binance"]
LOG = "records/lingxi_server.log"
INTERVAL = 300          # 5min 一查
LIGHT_AT = 40 * 60      # 40min 仍未触发出轻量简报


def ob(r):
    pid = r.get("position_id", "")
    if pid and "-" in pid:
        t = pid.rsplit("-", 1)[-1]
        if t.isdigit() and len(t) >= 18:
            try:
                return datetime.fromtimestamp(int(t) / 1e9, tz=timezone.utc)
            except (ValueError, OSError):
                pass
    return None


def e_positions():
    agg = {}
    for v in VEN:
        f = f"records/{v}_directional/settlements.jsonl"
        if not os.path.exists(f):
            f = f"{v}_directional/settlements.jsonl"
        if not os.path.exists(f):
            continue
        for line in open(f):
            line = line.strip()
            if not line:
                continue
            try:
                r = json.loads(line)
            except json.JSONDecodeError:
                continue
            pid = r.get("position_id") or "x"
            p = agg.get(pid)
            if p is None:
                p = {"v": v, "net": 0.0, "ob": ob(r), "hard": False, "last_reason": ""}
                agg[pid] = p
            p["net"] += r.get("realized_usdt", 0.0)
            if "硬止损" in r.get("reason", ""):
                p["hard"] = True
            if r.get("reason"):
                p["last_reason"] = r.get("reason")
    return [p for p in agg.values() if p["ob"] and p["ob"] >= E_START]


def logcount(*pats):
    if not os.path.exists(LOG):
        return 0
    try:
        out = subprocess.run(["grep", "-acE", "|".join(pats), LOG],
                             capture_output=True, text=True, timeout=20)
        return int(out.stdout.strip() or 0)
    except Exception:
        return 0


def lingxiopt_alive():
    try:
        out = subprocess.run(["bash", "-c", "ps -eo args 2>/dev/null | grep AGENT_LOOP_TICK_lingxiopt | grep -v grep"],
                             capture_output=True, text=True, timeout=15)
        return bool(out.stdout.strip())
    except Exception:
        return False


def svc_active():
    try:
        out = subprocess.run(["systemctl", "is-active", "lingxi"], capture_output=True, text=True, timeout=15)
        return out.stdout.strip() == "active"
    except Exception:
        return True  # 取不到不误报


def anomalies(base):
    flags = []
    # 1. panic / 429 / depth/BTC 错误(相对基线增量)
    if logcount("panic") > base["panic"]:
        flags.append("panic新增")
    if logcount("429", "Too Many Requests") > base["http429"]:
        flags.append("429新增")
    if logcount("depth.*err", "深度.*失败", "btc.*err", "BTC.*失败", "拉K线失败") > base["fetcherr"]:
        flags.append("depth/BTC错误新增")
    # 2. lingxiopt 重生
    if lingxiopt_alive():
        flags.append("lingxiopt重生")
    # 4. account risk 异常升级(.halt-live 出现 / 人工急停)
    if glob.glob("**/.halt-live", recursive=True) or glob.glob(".halt-live"):
        flags.append(".halt-live急停出现")
    # 5. 交易服务不 active
    if not svc_active():
        flags.append("lingxi服务非active")
    return flags


def hardstop_streak(E):
    # 3. E 硬止损连续集中:按开仓时间排序,末尾连续硬止损数 >=5
    s = sorted(E, key=lambda p: p["ob"])
    cnt = 0
    for p in reversed(s):
        if p["hard"]:
            cnt += 1
        else:
            break
    return cnt


def main():
    t0 = time.time()
    base = {"panic": logcount("panic"), "http429": logcount("429", "Too Many Requests"),
            "fetcherr": logcount("depth.*err", "深度.*失败", "btc.*err", "BTC.*失败", "拉K线失败")}
    print(f"EWATCH_START base={base} E_start={E_START.isoformat()}", flush=True)
    light_done = False
    while True:
        E = e_positions()
        n = len(E)
        win = sum(1 for p in E if p["net"] > 0)
        loss = sum(1 for p in E if p["net"] < 0)
        hard = sum(1 for p in E if p["hard"])
        streak = hardstop_streak(E)
        elapsed = int(time.time() - t0)
        flags = anomalies(base)
        if streak >= 5:
            flags.append(f"E硬止损连续{streak}")
        ts = datetime.now(timezone.utc).strftime("%H:%M:%SZ")
        if flags:
            print(f"EWATCH_ANOMALY {ts} flags={';'.join(flags)} | E n={n} hard={hard} win={win} loss={loss}", flush=True)
        elif n >= 30 and win >= 1 and loss >= 1:
            print(f"EWATCH_FULL {ts} E n={n} win={win} loss={loss} hard={hard} -> 出完整报告", flush=True)
            return
        elif elapsed >= LIGHT_AT and not light_done:
            light_done = True
            print(f"EWATCH_LIGHT {ts} E n={n} 距30还差{max(0,30-n)} win={win} loss={loss} elapsed={elapsed//60}min", flush=True)
        else:
            print(f"EWATCH_HB {ts} E n={n}/30 win={win} loss={loss} hard={hard} elapsed={elapsed//60}min", flush=True)
        time.sleep(INTERVAL)


if __name__ == "__main__":
    main()
