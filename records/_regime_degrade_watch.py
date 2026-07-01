#!/usr/bin/env python3
# regime_degrade 上线盯盘(只读;纯 Python 文件增量读,兼容 Python 3.6,不用 subprocess)。
#   - 「已开」仓 -> RDWATCH_OPEN(venue/symbol/名义;5.00=命中降档, 7.50=正常)
#   - 「[regime_degrade] 命中坏环境…降档」 -> RDWATCH_HIT
#   - panic/429/BTC/depth/盘口 错误 -> RDWATCH_ANOMALY
#   - 否则 RDWATCH_HB 心跳
# 用字节偏移只读新追加内容(抗日志增长/轮转),绝不漏。纯只读;不下单、不改任何文件。
import re, time, os

LOG = "records/lingxi_server.log"
INTERVAL = 120

re_open = re.compile(r"已开 (\S+) (long|short) .*名义=([0-9.]+)")
re_venue = re.compile(r"\[(\w+)\]\[OKX方向\] 已开")
re_hit = re.compile(r"\[regime_degrade\] (\S+) 命中坏环境\((\w+)\).*?([0-9.]+)→([0-9.]+)U")
# 异常:只匹配真错误,避免价格/数字里的 "429" 误报(如 price=0.05429)。
re_err = re.compile(r"panic:|panic \(|Too Many Requests|HTTP 429|code=-?429|拉K线失败|深度.*失败|BTC.*失败")
ALERT_REASONS = {"extreme_trend", "broad_up"}  # 只有这些 reason 才告警;transition_band 静默累计


def fsize(p):
    try:
        return os.path.getsize(p)
    except OSError:
        return 0


def read_new(p, offset):
    """从 offset 读到文件末尾,返回 (新行列表, 新offset)。truncate/轮转时从 0 读。"""
    sz = fsize(p)
    if sz < offset:
        offset = 0
    try:
        with open(p, "r", errors="ignore") as f:
            f.seek(offset)
            data = f.read()
            return data.splitlines(), f.tell()
    except OSError:
        return [], offset


def main():
    offset = fsize(LOG)  # 从当前末尾开始,只报启动后的新事件
    opens = hits = 0
    t0 = time.time()
    print("RDWATCH_START offset=%d" % offset, flush=True)
    while True:
        ts = time.strftime("%H:%M:%S")
        lines, offset = read_new(LOG, offset)
        fresh = 0
        for ln in lines:
            mh = re_hit.search(ln)
            mo = re_open.search(ln)
            me = re_err.search(ln)
            if not (mh or mo or me):
                continue
            fresh += 1
            if mh:
                hits += 1
                sym, reason = mh.group(1), mh.group(2)
                # extreme_trend / broad_up -> ALERT(通知);transition_band -> 静默累计(OKHIT,不通知)。
                if reason in ALERT_REASONS:
                    print("RDWATCH_ALERT %s %s 命中(%s)降档 %s→%sU (累计命中%d)" % (ts, sym, reason, mh.group(3), mh.group(4), hits), flush=True)
                else:
                    print("RDWATCH_OKHIT %s %s 命中(%s)降档 5U (累计命中%d)" % (ts, sym, reason, hits), flush=True)
            if mo:
                opens += 1
                mv = re_venue.search(ln)
                ven = mv.group(1) if mv else "okx"
                notional = mo.group(3)
                if ven == "binance":  # Binance 本应不新开 -> 告警
                    print("RDWATCH_ALERT %s venue=binance %s %s 名义=%sU ⚠Binance本应不新开!" % (ts, mo.group(1), mo.group(2), notional), flush=True)
                else:  # 常规开仓(含 5U,降档已由 regime_degrade 行处理)只记录,不通知
                    print("RDWATCH_OK %s venue=%s %s %s 名义=%sU" % (ts, ven, mo.group(1), mo.group(2), notional), flush=True)
            if me:
                print("RDWATCH_ANOMALY %s %s" % (ts, ln[:150]), flush=True)
        if fresh == 0:
            print("RDWATCH_HB %s 累计开仓=%d 命中=%d elapsed=%dmin" % (ts, opens, hits, int((time.time() - t0) // 60)), flush=True)
        time.sleep(INTERVAL)


if __name__ == "__main__":
    main()
