#!/usr/bin/env python3
# fast 动态采集监控(只读)。每10min:健康(active/panic-429/WS写入)+ aligned 样本数。
# aligned>=100 -> ALIGN_READY(该出动态特征报告);异常 -> ALIGN_ANOMALY;否则心跳 ALIGN_HB。
import json, os, time, bisect, subprocess
from datetime import datetime, timezone

VEN = ["okx", "bybit", "binance", "gate"]
POS = {"fast_trail_tp", "fast_tp"}
NEG = {"fast_stop_reversal", "fast_no_feedback"}
DYN = "okx_directional/fast_dynamic_feature_events.jsonl"
LOG = "lingxi_server.log"
TOL = 2.5


def ts(s):
    s = (s or "").rstrip("Z")
    try:
        if "." in s:
            h, f = s.split(".", 1)
            return datetime.strptime(h + "." + "".join(c for c in f if c.isdigit())[:6], "%Y-%m-%dT%H:%M:%S.%f").replace(tzinfo=timezone.utc)
        return datetime.strptime(s, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def sh(cmd):
    try:
        p = subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=20)
        return p.stdout.decode().strip()
    except Exception:
        return ""


def aligned_count():
    dyn = {}
    if os.path.exists(DYN):
        for l in open(DYN):
            l = l.strip()
            if not l:
                continue
            try:
                d = json.loads(l)
            except json.JSONDecodeError:
                continue
            t = ts(d.get("recorded_at"))
            if t:
                dyn.setdefault(d.get("symbol"), []).append(t.timestamp())
    for s in dyn:
        dyn[s].sort()
    pos = neg = 0
    for v in VEN:
        f = f"{v}_directional/settlements.jsonl"
        if not os.path.exists(f):
            continue
        for l in open(f):
            if '"signal_layer":"fast_v1"' not in l:
                continue
            try:
                d = json.loads(l)
            except json.JSONDecodeError:
                continue
            if d.get("reason") not in (POS | NEG):
                continue
            t = ts(d.get("source_event_time") or d.get("time"))
            arr = dyn.get(d.get("symbol"))
            if not t or not arr:
                continue
            i = bisect.bisect_left(arr, t.timestamp())
            hit = any(0 <= j < len(arr) and abs(arr[j] - t.timestamp()) <= TOL for j in (i - 1, i))
            if hit:
                if d.get("reason") in POS:
                    pos += 1
                else:
                    neg += 1
    return pos, neg


def panic_count():
    try:
        return int(sh(f"grep -acE 'panic|Too Many Requests|HTTP 429' {LOG}") or 0)
    except Exception:
        return 0


def main():
    base_panic = panic_count()
    t0 = time.time()
    print(f"FASTDYN_START base_panic={base_panic}", flush=True)
    ready = False
    while True:
        active = sh("systemctl is-active lingxi")
        panic = panic_count()
        dyn_fresh = (time.time() - os.path.getmtime(DYN)) if os.path.exists(DYN) else 9e9
        pos, neg = aligned_count()
        al = pos + neg
        ts_now = datetime.now(timezone.utc).strftime("%H:%M:%SZ")
        flags = []
        if active != "active":
            flags.append("service非active")
        if panic > base_panic:
            flags.append(f"panic/429新增({panic-base_panic})")
        if dyn_fresh > 300:
            flags.append(f"WS文件停写{int(dyn_fresh)}s")
        if flags:
            print(f"ALIGN_ANOMALY {ts_now} {';'.join(flags)} | aligned={al}", flush=True)
        elif al >= 100 and not ready:
            ready = True
            print(f"ALIGN_READY {ts_now} aligned={al}(pos={pos}/neg={neg}) → 出动态特征报告", flush=True)
        else:
            print(f"ALIGN_HB {ts_now} aligned={al}(pos={pos}/neg={neg}) active={active} panic+={panic-base_panic} WS写入{int(dyn_fresh)}s前 elapsed={int((time.time()-t0)//60)}min", flush=True)
        time.sleep(600)


if __name__ == "__main__":
    main()
