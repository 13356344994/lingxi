import json, datetime

cut = datetime.datetime(2026, 6, 28, 4, 0, 0)  # UTC = 北京12:00 紧急吃单部署
rows = []
for v in ['okx', 'bybit', 'binance']:
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
                r['_v'] = v
                rows.append(r)
    except Exception:
        pass

def ap(r):
    n = r.get('notional', 0) or 0
    return r.get('realized_usdt', 0) / n * 5 * 100 if n > 0 else 0

print(f"=== 12:00后(紧急吃单部署后) {len(rows)}笔 ===")
if rows:
    net = sum(r.get('realized_usdt', 0) for r in rows)
    w = sum(1 for r in rows if r.get('realized_usdt', 0) > 0.02)
    wins = [r['realized_usdt'] for r in rows if r.get('realized_usdt', 0) > 0.02]
    losses = [r['realized_usdt'] for r in rows if r.get('realized_usdt', 0) < -0.02]
    print(f"净{net:+.2f}U 胜{w}/{len(rows)}={100*w//len(rows)}%")
    if wins: print(f"平均赢{sum(wins)/len(wins):+.3f}U")
    if losses: print(f"平均亏{sum(losses)/len(losses):+.3f}U")
    hs = [r for r in rows if '硬止损' in r.get('reason', '')]
    print(f"硬止损{len(hs)}笔:")
    for r in hs:
        print(f"  {r['_v']} {r.get('symbol')} 账户{ap(r):+.0f}% 实现{r.get('realized_usdt',0):+.2f}U")
else:
    print("(暂无平仓)")
