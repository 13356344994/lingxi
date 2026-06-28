import json, collections, datetime

def load(v):
    rows = []
    try:
        for l in open(f"{v}_directional/settlements.jsonl"):
            l = l.strip()
            if not l:
                continue
            r = json.loads(l)
            try:
                r['_t'] = datetime.datetime.strptime(r.get('time', '')[:19], '%Y-%m-%dT%H:%M:%S')
            except Exception:
                continue
            rows.append(r)
    except Exception:
        pass
    return rows

def bj(t):  # UTC -> 北京
    return t + datetime.timedelta(hours=8)

for v in ['okx', 'bybit', 'binance']:
    rows = load(v)
    if not rows:
        print(f"\n##### {v.upper()}: 无数据"); continue
    rows.sort(key=lambda r: r['_t'])
    net = sum(r.get('realized_usdt', 0) for r in rows)
    w = sum(1 for r in rows if r.get('realized_usdt', 0) > 0.02)
    print(f"\n##### {v.upper()} 全量 {len(rows)}笔 净{net:+.2f}U 胜{w}/{len(rows)}={100*w//len(rows)}%  [{bj(rows[0]['_t']):%m-%d %H:%M}~{bj(rows[-1]['_t']):%m-%d %H:%M} 北京]")
    # 按北京小时段聚合
    seg = collections.OrderedDict()
    for r in rows:
        k = bj(r['_t']).strftime('%m-%d %Hh')
        s = seg.setdefault(k, [0, 0.0, 0])  # n, net, wins
        s[0] += 1; s[1] += r.get('realized_usdt', 0)
        if r.get('realized_usdt', 0) > 0.02: s[2] += 1
    print("  时段(北京) | 笔 净U 胜率")
    for k, (n, nt, wn) in seg.items():
        bar = '+' if nt >= 0 else '-'
        print(f"   {k} | {n:>2} {nt:+6.2f} {100*wn//n if n else 0:>3}% {bar*min(int(abs(nt)),20)}")
    # 方向 long/short
    for sd in ['long', 'short']:
        sr = [r for r in rows if r.get('side') == sd]
        if sr:
            n2 = sum(r.get('realized_usdt', 0) for r in sr); w2 = sum(1 for r in sr if r.get('realized_usdt', 0) > 0.02)
            print(f"  方向{sd:<5} {len(sr)}笔 净{n2:+.2f}U 胜{100*w2//len(sr)}%")
