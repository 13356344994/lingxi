#!/usr/bin/env python3
# 主线1 低效率 universe 采样监控(只读):每 INTERVAL 跑一次 _fast_universe_leadfollow.py,
# 输出心跳 + 达标候选。发现 ★ 真候选 → 打 UNIV_CANDIDATE 提醒。
import subprocess, time, sys, os

HERE = os.path.dirname(os.path.abspath(__file__))
INTERVAL = 1800  # 30min


def run_once():
    p = subprocess.Popen([sys.executable, "_fast_universe_leadfollow.py"], cwd=HERE,
                         stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    out, _ = p.communicate()
    return out.decode("utf-8", "replace")


def main():
    print("UNIV_MON_START", time.strftime("%H:%M:%S"), flush=True)
    while True:
        try:
            txt = run_once()
            hit = [l for l in txt.splitlines() if l.strip().startswith("★")]
            hdr = next((l for l in txt.splitlines() if "peer lag-follow 命中" in l), "")
            if hit:
                print("UNIV_CANDIDATE", time.strftime("%H:%M:%S"), "|", " ; ".join(hit), flush=True)
            else:
                print("UNIV_HB", time.strftime("%H:%M:%S"), "|", hdr.strip(), "| 无达标候选", flush=True)
        except Exception as e:
            print("UNIV_ERR", time.strftime("%H:%M:%S"), repr(e), flush=True)
        time.sleep(INTERVAL)


if __name__ == "__main__":
    main()
