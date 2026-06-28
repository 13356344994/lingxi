import json, collections, datetime, sys

cut = datetime.datetime(2026, 6, 27, 22, 51, 0)  # UTC = 北京06:51 反转持有部署
tot = 0; totn = 0; hs_n = 0; hs_pnl = 0.0
for v in ['okx', 'bybit', 'gate', 'binance']:
    rows = []
    try:
        for l in open(f"{v}_directional/settlements.jsonl"):
            l = l.strip()
            if not l:
                continue
            r = json.loads(l)
            try:
                t = datetime.datetime.strptime(r.get('time', '')[:19], '%Y-%m-%dT%H:%M:%S')
            except Exception:
                continue
            if t >= cut:
                rows.append(r)
    except Exception:
        pass
    if not rows:
        print(f"{v:<8} 无平仓")
        continue
    net = sum(r.get('realized_usdt', 0) for r in rows)
    w = sum(1 for r in rows if r.get('realized_usdt', 0) > 0.02)
    hs = [r for r in rows if '硬止损' in r.get('reason', '')]
    hsp = sum(r.get('realized_usdt', 0) for r in hs)
    tot += net; totn += len(rows); hs_n += len(hs); hs_pnl += hsp
    print(f"{v:<8} {len(rows)}笔 净{net:+.2f}U 胜{w}/{len(rows)}={100*w//len(rows)}% 硬止损{len(hs)}({hsp:+.1f}U)")
print(f"合计 {totn}笔 净{tot:+.2f}U | 硬止损共{hs_n}笔 {hs_pnl:+.1f}U")
