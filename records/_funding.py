import json, collections

rows = []
for v in ['okx', 'bybit', 'binance']:
    try:
        for l in open(f"{v}_directional/settlements.jsonl"):
            l = l.strip()
            if not l:
                continue
            r = json.loads(l)
            rows.append(r)
    except Exception:
        pass

def agg(rs):
    n = len(rs)
    if n == 0:
        return "0笔"
    net = sum(r.get('realized_usdt', 0) for r in rs)
    w = sum(1 for r in rs if r.get('realized_usdt', 0) > 0.02)
    return f"{n}笔 净{net:+.2f}U 胜{100*w//n}%"

print("=== 资费符号 × 方向 (funding_at_open) ===")
for fs, fname in [(lambda f: f < -0.0002, "负资费(<-0.02%)"), (lambda f: abs(f) <= 0.0002, "近零资费"), (lambda f: f > 0.0002, "正资费(>0.02%)")]:
    for sd in ['long', 'short']:
        rs = [r for r in rows if fs(r.get('funding_at_open', 0)) and r.get('side') == sd]
        print(f"  {fname:<16} {sd:<5} {agg(rs)}")

print("\n=== 「资费对齐方向」vs「逆资费方向」 ===")
# 对齐 = (负资费&多) or (正资费&空);逆 = 反之
aligned = [r for r in rows if (r.get('funding_at_open', 0) < -0.0002 and r.get('side') == 'long') or (r.get('funding_at_open', 0) > 0.0002 and r.get('side') == 'short')]
against = [r for r in rows if (r.get('funding_at_open', 0) < -0.0002 and r.get('side') == 'short') or (r.get('funding_at_open', 0) > 0.0002 and r.get('side') == 'long')]
print(f"  资费对齐(随资费做): {agg(aligned)}")
print(f"  资费逆向(逆资费做): {agg(against)}")

print("\n=== 按币种聚合(净亏 top) ===")
bysym = collections.defaultdict(lambda: [0, 0.0, 0])
for r in rows:
    s = bysym[r.get('symbol', '?')]
    s[0] += 1; s[1] += r.get('realized_usdt', 0); s[2] += (1 if r.get('realized_usdt', 0) > 0.02 else 0)
worst = sorted(bysym.items(), key=lambda x: x[1][1])[:12]
for sym, (n, net, w) in worst:
    print(f"  {sym:<13} {n}笔 净{net:+.2f}U 胜{100*w//n if n else 0}%")
