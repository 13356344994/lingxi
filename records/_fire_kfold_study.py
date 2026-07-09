# -*- coding: utf-8 -*-
"""
火刀策略 K 折研究:哪些卖方 burst 有"二波延续"?
口径纪律:
  - 事件 = side=sell burst 且 3s 内本币有 pos_side=long 官方爆仓确认;同币 120s 去重取首个。
  - 入场价 = burst 后首个 mid 快照(<=10s,否则丢弃);做空口径收益路径 120s。
  - trail:激活 +10bps,峰值回吐 20bps 出场,-40bps 灾难线,120s 强制出;净 = 毛 - 12bps。
  - 特征全部入场时刻可知;K 折 = 按事件时间切 8 等长片,7 片定方向+阈值,留出片验证;
    过检 = >=6/8 片留出集"好-坏"净中位差与训练方向同号。
Python 3.6 兼容(无 fromisoformat,无 f-string '=')。
"""
import json
import time
import calendar
import bisect
import statistics
import sys
from collections import defaultdict, deque

DIR = "/root/lingxi/records/okx_directional"
BURST_F = DIR + "/fast_burst_detect.jsonl"
LIQ_F = DIR + "/fast_liquidation_events.jsonl"
MIDS_F = DIR + "/fast_market_mids.jsonl"

COST_BPS = 12.0
TRAIL_ACT = 10.0
TRAIL_GIVEBACK = 20.0
DISASTER = -40.0
HORIZON = 120.0
DEDUP_S = 120.0
CONFIRM_S = 3.0
ENTRY_MAX_LAG = 10.0
PRE_WIN = 70.0    # 回放窗口前沿(动量需要 t-60)
POST_WIN = 130.0
N_FOLDS = 8
PASS_NEED = 6

_date_cache = {}


def parse_iso(s):
    # "2026-07-05T02:15:28.729143164Z"
    base = s[:19]
    day = base[:10]
    if day in _date_cache:
        day_epoch = _date_cache[day]
    else:
        tt = time.strptime(day, "%Y-%m-%d")
        day_epoch = calendar.timegm(tt)
        _date_cache[day] = day_epoch
    h = int(base[11:13]); m = int(base[14:16]); sec = int(base[17:19])
    frac = 0.0
    if len(s) > 19 and s[19] == '.':
        fs = s[20:].rstrip('Z')
        if fs:
            frac = float('0.' + fs)
    return day_epoch + h * 3600 + m * 60 + sec + frac


def fmt_t(t):
    return time.strftime("%m-%d %H:%M", time.gmtime(t))


def pf(nets):
    up = sum(x for x in nets if x > 0)
    dn = -sum(x for x in nets if x < 0)
    if dn <= 0:
        return float('inf') if up > 0 else 0.0
    return up / dn


def grp_stat(evts):
    nets = [e['net'] for e in evts]
    if not nets:
        return "n=0"
    med = statistics.median(nets)
    p = pf(nets)
    wr = sum(1 for x in nets if x > 0) / float(len(nets))
    s = "n=%d 净中位=%+.1fbps PF=%.2f 胜率=%.0f%%" % (len(nets), med, p, wr * 100)
    # 单币集中度警告
    cnt = defaultdict(int)
    for e in evts:
        cnt[e['symbol']] += 1
    top_sym, top_n = max(cnt.items(), key=lambda kv: kv[1])
    if top_n / float(len(evts)) > 0.30:
        s += "  [警告:%s 占 %.0f%%]" % (top_sym, 100.0 * top_n / len(evts))
    return s


def main():
    t0_wall = time.time()
    # 稳健性开关:--exclude SYM1,SYM2 剔除指定币后复跑
    exclude = set()
    if '--exclude' in sys.argv:
        i = sys.argv.index('--exclude')
        if i + 1 < len(sys.argv):
            exclude = set(sys.argv[i + 1].split(','))
            print("[稳健性] 剔除币: %s" % ','.join(sorted(exclude)))

    # ---------- 1. 官方爆仓(多头爆) ----------
    liq_long = defaultdict(list)
    with open(LIQ_F) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue
            if d.get('pos_side') != 'long':
                continue
            liq_long[d['symbol']].append(parse_iso(d['recorded_at']))
    for k in liq_long:
        liq_long[k].sort()
    print("[载入] 多头爆仓推送 %d 币 %d 条 (%.0fs)" % (
        len(liq_long), sum(len(v) for v in liq_long.values()), time.time() - t0_wall))

    # ---------- 2. burst 流:构造事件(走前计数 24h burst 次数) ----------
    events = []
    last_kept = {}
    hist24 = defaultdict(deque)   # symbol -> 24h 内所有 burst 时间(买卖都算)
    n_sell = 0
    n_confirm = 0
    with open(BURST_F) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue
            sym = d['symbol']
            if sym in exclude:
                continue
            t = parse_iso(d['recorded_at'])
            dq = hist24[sym]
            while dq and dq[0] < t - 86400:
                dq.popleft()
            prior24 = len(dq)      # 不含当前这笔,先算再入队
            dq.append(t)
            if d.get('side') != 'sell':
                continue
            n_sell += 1
            # 3s 内确认
            times = liq_long.get(sym)
            if not times:
                continue
            i = bisect.bisect_left(times, t)
            if i >= len(times) or times[i] > t + CONFIRM_S:
                continue
            n_confirm += 1
            # 120s 去重取首个
            if sym in last_kept and t - last_kept[sym] <= DEDUP_S:
                continue
            last_kept[sym] = t
            mult = d.get('multiple') or 0.0
            noti = d.get('notional_1s') or 0.0
            events.append({
                'symbol': sym, 't': t,
                'multiple': mult,
                'notional_1s': noti,
                'baseline': (noti / mult) if mult > 0 else None,
                # 跳零序列化:缺字段=0(已验证全文件无 ":0" 写法)
                'market_liq_5m': d.get('market_liq_5m', 0),
                'sym_liq_n_30s': d.get('sym_liq_n_30s', 0),
                'xliq_n_30s': d.get('xliq_n_30s', 0),
                'prior24': prior24,
                'path': [],
            })
    events.sort(key=lambda e: e['t'])
    print("[事件] 卖方burst=%d 确认=%d 去重后事件=%d (%.0fs)" % (
        n_sell, n_confirm, len(events), time.time() - t0_wall))

    # ---------- 3. 流式回放 mids ----------
    ei = 0
    active = []
    n_lines = 0
    with open(MIDS_F) as f:
        for line in f:
            n_lines += 1
            # 快速取时间戳:{"recorded_at":"....."
            q1 = line.find('"', 16)
            if not line.startswith('{"recorded_at":"') or q1 < 0:
                try:
                    d = json.loads(line)
                    ts = d['recorded_at']
                except Exception:
                    continue
                T = parse_iso(ts)
                mids_d = d.get('mids', {})
                while ei < len(events) and events[ei]['t'] - PRE_WIN <= T:
                    active.append(events[ei]); ei += 1
                active = [e for e in active if T <= e['t'] + POST_WIN]
                for e in active:
                    px = mids_d.get(e['symbol'])
                    if px:
                        e['path'].append((T, px))
                continue
            T = parse_iso(line[16:q1])
            while ei < len(events) and events[ei]['t'] - PRE_WIN <= T:
                active.append(events[ei]); ei += 1
            if active:
                active = [e for e in active if T <= e['t'] + POST_WIN]
                for e in active:
                    key = '"' + e['symbol'] + '":'
                    j = line.find(key)
                    if j < 0:
                        continue
                    j += len(key)
                    k = j
                    L = len(line)
                    while k < L and line[k] not in ',}':
                        k += 1
                    try:
                        px = float(line[j:k])
                    except ValueError:
                        continue
                    e['path'].append((T, px))
            if n_lines % 30000 == 0:
                sys.stdout.write("  ...mids %d 行 %s (%.0fs)\n" % (n_lines, fmt_t(T), time.time() - t0_wall))
                sys.stdout.flush()
    print("[回放] mids %d 行扫描完成 (%.0fs)" % (n_lines, time.time() - t0_wall))

    # ---------- 4. 逐事件计算指标 ----------
    kept = []
    n_drop_entry = 0
    n_drop_cover = 0
    for e in events:
        t = e['t']
        path = e['path']
        # 入场:burst 后首个快照
        entry = None
        for (T, px) in path:
            if T > t:
                entry = (T, px)
                break
        if entry is None or entry[0] - t > ENTRY_MAX_LAG:
            n_drop_entry += 1
            continue
        post = [(T, px) for (T, px) in path if T >= entry[0] and T <= t + HORIZON]
        if len(post) < 2 or post[-1][0] < t + HORIZON - 15.0:
            n_drop_cover += 1
            continue
        e_px = entry[1]
        rets = [(T, (e_px - px) / e_px * 1e4) for (T, px) in post]
        peak = max(r for (_, r) in rets)
        # 定点收益
        r60 = None
        for (T, r) in rets:
            if T >= t + 60.0:
                r60 = r
                break
        r120 = rets[-1][1]
        # trail 模拟
        act = False
        hi = -1e9
        exit_ret = rets[-1][1]
        for (T, r) in rets:
            if r > hi:
                hi = r
            if not act and r >= TRAIL_ACT:
                act = True
            if r <= DISASTER:
                exit_ret = r
                break
            if act and hi - r >= TRAIL_GIVEBACK:
                exit_ret = r
                break
        e['peak'] = peak
        e['r60'] = r60 if r60 is not None else r120
        e['r120'] = r120
        e['gross'] = exit_ret
        e['net'] = exit_ret - COST_BPS
        # burst 前 60s 动量(入场时刻可知:只用 <=t 的快照)
        pre_now = None
        pre_60 = None
        for (T, px) in path:
            if T <= t:
                pre_now = px
            if T <= t - 60.0:
                pre_60 = px
        if pre_now and pre_60:
            e['mom60'] = (pre_now - pre_60) / pre_60 * 1e4
        else:
            e['mom60'] = None
        kept.append(e)

    print("[清洗] 丢弃(无入场快照)=%d 丢弃(120s覆盖不足)=%d 有效事件=%d" % (
        n_drop_entry, n_drop_cover, len(kept)))
    if len(kept) < 100:
        print("!! 有效事件过少,后续统计不可靠")
    if not kept:
        return

    tmin = kept[0]['t']; tmax = kept[-1]['t']
    print("")
    print("=" * 78)
    print("① 事件总数与时间跨度")
    print("   有效事件 n=%d  跨度 %s ~ %s UTC (%.1f 天)  涉及币数=%d" % (
        len(kept), fmt_t(tmin), fmt_t(tmax), (tmax - tmin) / 86400.0,
        len(set(e['symbol'] for e in kept))))

    print("")
    print("② 基线(全体事件,trail 口径,净=毛-12bps)")
    print("   " + grp_stat(kept))
    nets_all = [e['net'] for e in kept]
    peaks = [e['peak'] for e in kept]
    print("   峰值<20bps 占比=%.0f%%  峰值中位=%.1fbps  定点60s净中位=%+.1f  定点120s净中位=%+.1f" % (
        100.0 * sum(1 for p in peaks if p < 20) / len(peaks),
        statistics.median(peaks),
        statistics.median([e['r60'] - COST_BPS for e in kept]),
        statistics.median([e['r120'] - COST_BPS for e in kept])))
    print("   净和=%+.0fbps  灾难单(净<=-40)=%d" % (
        sum(nets_all), sum(1 for x in nets_all if x <= -40)))

    # ---------- 5. K 折框架 ----------
    span = (tmax - tmin) / N_FOLDS if tmax > tmin else 1.0
    for e in kept:
        e['fold'] = min(N_FOLDS - 1, int((e['t'] - tmin) / span))
    fold_n = [sum(1 for e in kept if e['fold'] == k) for k in range(N_FOLDS)]
    print("   8 等长时间片事件数: %s" % fold_n)

    FEATURES = [
        ('multiple', 'tertile',
         '故事:倍数越高=抛压相对基线越极端,越可能击穿止损/爆仓价位带引发二波'),
        ('notional_1s', 'tertile',
         '故事:绝对名义大才有足够体量把价格推过下一层强平触发线'),
        ('baseline', 'tertile',
         '故事:基线小的币薄,同样燃料更容易被打穿;基线大的币能吸收第一波'),
        ('market_liq_5m', 'tertile',
         '故事:全市场5min爆仓多=系统性去杠杆进行中,燃料充足,单币瀑布易共振'),
        ('sym_liq_n_30s', 'binary0',
         '故事:本币30s内已有官方爆仓=强平引擎已点火,burst是链条中段而非孤立噪声'),
        ('xliq_n_30s', 'binary0',
         '故事:跨所同时在爆=全市场同向持仓被清算,非本所单一大户砸盘'),
        ('prior24', 'tertile',
         '故事:24h内burst频繁的币是常态噪声源(做市/刷量),首爆的币信息含量更高'),
        ('mom60', 'tertile',
         '故事:burst前60s已在下跌=多头保证金已被压薄,burst是压垮下一层的稻草'),
    ]

    def split_groups(evs, feat, mode, thr):
        """按训练阈值 thr 把 evs 分为 (lo组, hi组);tertile 时中间丢弃。"""
        if mode == 'binary0':
            lo = [e for e in evs if e[feat] == 0]
            hi = [e for e in evs if e[feat] > 0]
            return lo, hi
        q33, q66 = thr
        if q33 == q66:
            lo = [e for e in evs if e[feat] <= q33]
            hi = [e for e in evs if e[feat] > q33]
            return lo, hi
        lo = [e for e in evs if e[feat] <= q33]
        hi = [e for e in evs if e[feat] >= q66]
        return lo, hi

    def calc_thr(vals, mode):
        if mode == 'binary0':
            return None
        vs = sorted(vals)
        n = len(vs)
        q33 = vs[max(0, n // 3 - 1)]
        q66 = vs[min(n - 1, 2 * n // 3)]
        if q33 == q66:
            q33 = q66 = statistics.median(vs)
        return (q33, q66)

    def kfold_eval(feat, mode, cond2=None):
        """cond2: (feat2, mode2) 组合时的第二特征;组合口径=两特征好组交集 vs 其余。
        返回 (n_same, results, pooled_good, pooled_bad, dir_desc)"""
        pool = [e for e in kept if e[feat] is not None]
        if cond2:
            pool = [e for e in pool if e[cond2[0]] is not None]
        n_same = 0
        results = []
        pooled_good = []
        pooled_bad = []
        dirs = []
        for k in range(N_FOLDS):
            tr = [e for e in pool if e['fold'] != k]
            te = [e for e in pool if e['fold'] == k]
            if len(tr) < 30 or len(te) < 4:
                results.append('片%d:样本不足' % k)
                continue
            thr = calc_thr([e[feat] for e in tr], mode)
            tr_lo, tr_hi = split_groups(tr, feat, mode, thr)
            if cond2:
                f2, m2, gdir2 = cond2
                thr2 = calc_thr([e[f2] for e in tr], m2)
                # 第二特征好组方向在训练集上现算
                lo2, hi2 = split_groups(tr, f2, m2, thr2)
                if len(lo2) < 3 or len(hi2) < 3:
                    results.append('片%d:组合训练样本不足' % k)
                    continue
                good2_is_hi = statistics.median([e['net'] for e in hi2]) > statistics.median([e['net'] for e in lo2])

                def in_good2(e):
                    if m2 == 'binary0':
                        v_hi = e[f2] > 0
                    else:
                        v_hi = e[f2] >= thr2[1] if thr2[0] != thr2[1] else e[f2] > thr2[0]
                        v_lo = e[f2] <= thr2[0]
                        if not v_hi and not v_lo:
                            return None  # 中段丢弃
                    if m2 == 'binary0':
                        return v_hi if good2_is_hi else (not v_hi)
                    return v_hi if good2_is_hi else v_lo
            if len(tr_lo) < 3 or len(tr_hi) < 3:
                results.append('片%d:训练组太小' % k)
                continue
            good_is_hi = statistics.median([e['net'] for e in tr_hi]) > statistics.median([e['net'] for e in tr_lo])
            dirs.append('hi' if good_is_hi else 'lo')
            te_lo, te_hi = split_groups(te, feat, mode, thr)
            g, b = (te_hi, te_lo) if good_is_hi else (te_lo, te_hi)
            if cond2:
                g2 = []
                b2 = list(b)
                for e in g:
                    r = in_good2(e)
                    if r is True:
                        g2.append(e)
                    elif r is False:
                        b2.append(e)
                g, b = g2, b2
            if len(g) < 2 or len(b) < 2:
                results.append('片%d:留出组太小(g=%d,b=%d)' % (k, len(g), len(b)))
                continue
            diff = statistics.median([e['net'] for e in g]) - statistics.median([e['net'] for e in b])
            same = diff > 0
            if same:
                n_same += 1
            results.append('片%d:%s diff=%+.1f (g=%d,b=%d)' % (k, '同' if same else '反', diff, len(g), len(b)))
            pooled_good.extend(g)
            pooled_bad.extend(b)
        dir_desc = '/'.join(sorted(set(dirs))) if dirs else '?'
        return n_same, results, pooled_good, pooled_bad, dir_desc

    print("")
    print("=" * 78)
    print("③ 单特征 K 折过检结果(过检=8片中>=%d片留出集好-坏净中位差同号)" % PASS_NEED)
    passed = []
    feat_summary = {}
    for feat, mode, story in FEATURES:
        n_same, results, pg, pb, dir_desc = kfold_eval(feat, mode)
        ok = n_same >= PASS_NEED
        feat_summary[feat] = (n_same, pg, pb, dir_desc)
        if ok:
            passed.append((feat, mode, story))
        print("")
        print("-- %s [%s]  %s" % (feat, mode, story))
        print("   训练方向(好组)=%s  同号片数=%d/8  -> %s" % (dir_desc, n_same, '过检 ✔' if ok else '不过 ✘'))
        for r in results:
            print("     " + r)
        print("   汇总留出集 好组: " + grp_stat(pg))
        print("   汇总留出集 坏组: " + grp_stat(pb))

    print("")
    print("=" * 78)
    print("④ 过检特征的两两组合(好组=两特征好桶交集,坏组=其余)")
    combo_pass = []
    if len(passed) < 2:
        print("   过检特征不足 2 个,无组合可测。")
    else:
        for i in range(len(passed)):
            for j in range(i + 1, len(passed)):
                f1, m1, _ = passed[i]
                f2, m2, _ = passed[j]
                n_same, results, pg, pb, dir_desc = kfold_eval(f1, m1, cond2=(f2, m2, None))
                ok = n_same >= PASS_NEED
                if ok:
                    combo_pass.append((f1, f2, n_same, pg, pb))
                print("")
                print("-- 组合 %s + %s  同号片数=%d/8 -> %s" % (f1, f2, n_same, '过检 ✔' if ok else '不过 ✘'))
                for r in results:
                    print("     " + r)
                print("   汇总留出集 好组: " + grp_stat(pg))
                print("   汇总留出集 坏组: " + grp_stat(pb))

    print("")
    print("=" * 78)
    print("⑤ 最终结论")
    base_med = statistics.median(nets_all)
    base_pf = pf(nets_all)
    print("   基线:n=%d 净中位=%+.1fbps PF=%.2f" % (len(kept), base_med, base_pf))
    if not passed:
        print("   >>> 负结果:8 个入场时刻可知特征无一通过 6/8 片 K 折同号检验。")
        print("   >>> 在此数据窗口内没有找到站得住的'二波分辨器'。")
    else:
        print("   过检单特征:")
        for feat, mode, story in passed:
            n_same, pg, pb, dir_desc = feat_summary[feat]
            g_med = statistics.median([e['net'] for e in pg]) if pg else float('nan')
            g_pf = pf([e['net'] for e in pg]) if pg else 0.0
            print("     - %s (方向=%s, %d/8): 好组 n=%d 净中位=%+.1f PF=%.2f | vs 基线净中位 %+.1f" % (
                feat, dir_desc, n_same, len(pg), g_med, g_pf, base_med))
        if combo_pass:
            print("   过检组合:")
            for f1, f2, ns, pg, pb in combo_pass:
                g_med = statistics.median([e['net'] for e in pg]) if pg else float('nan')
                g_pf = pf([e['net'] for e in pg]) if pg else 0.0
                print("     - %s+%s (%d/8): 好组 n=%d 净中位=%+.1f PF=%.2f" % (
                    f1, f2, ns, len(pg), g_med, g_pf))
        else:
            print("   组合均未过检。")
        # 站得住的标准:好组净中位 > 0 且 PF > 1
        strong = []
        for feat, mode, story in passed:
            n_same, pg, pb, dir_desc = feat_summary[feat]
            if pg and statistics.median([e['net'] for e in pg]) > 0 and pf([e['net'] for e in pg]) > 1.0:
                strong.append(feat)
        if strong:
            print("   >>> 站得住(好组净中位>0 且 PF>1)的分辨器:%s" % ', '.join(strong))
        else:
            print("   >>> 有特征过同号检验但好组本身仍不赚钱(净中位<=0 或 PF<=1):")
            print("       只能'少亏',不能把火刀变成正期望——本质仍是负结果。")
    print("")
    print("[完成] 总耗时 %.0fs" % (time.time() - t0_wall))


if __name__ == '__main__':
    main()
