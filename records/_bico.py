import json, datetime

def bj(t):
    return t + datetime.timedelta(hours=8)

print("=== BICO-USDT 全量成交(四所) ===")
allrows = []
for v in ['okx', 'bybit', 'gate', 'binance']:
    try:
        for l in open(f"{v}_directional/settlements.jsonl"):
            l = l.strip()
            if not l:
                continue
            r = json.loads(l)
            if r.get('symbol') != 'BICO-USDT':
                continue
            try:
                r['_t'] = datetime.datetime.strptime(r.get('time', '')[:19], '%Y-%m-%dT%H:%M:%S')
            except Exception:
                continue
            r['_v'] = v
            allrows.append(r)
    except Exception:
        pass
allrows.sort(key=lambda r: r['_t'])
net = 0.0
for r in allrows:
    net += r.get('realized_usdt', 0)
    print(f"{bj(r['_t']):%m-%d %H:%M} {r['_v']:<7} {r.get('side',''):<5} {r.get('regime',''):<5} {r.get('entry',0):.5f}->{r.get('exit',0):.5f} 实现{r.get('realized_usdt',0):+.2f} | {r.get('reason','')[:22]}")
w = sum(1 for r in allrows if r.get('realized_usdt', 0) > 0.02)
print(f"\nBICO 合计 {len(allrows)}笔 净{net:+.2f}U 胜{w}/{len(allrows)}  方向: long={sum(1 for r in allrows if r.get('side')=='long')} short={sum(1 for r in allrows if r.get('side')=='short')}")
