import json

rows = []
for v in ['okx', 'bybit', 'binance']:
    try:
        for l in open(f"{v}_directional/settlements.jsonl"):
            l = l.strip()
            if not l:
                continue
            r = json.loads(l)
            r['_v'] = v
            rows.append(r)
    except Exception:
        pass

def acctpct(r):
    n = r.get('notional', 0) or 0
    if n <= 0:
        return 0.0
    return r.get('realized_usdt', 0) / n * 5 * 100  # 账户%(5x)

wins = [r for r in rows if r.get('realized_usdt', 0) > 0.02]
losses = [r for r in rows if r.get('realized_usdt', 0) < -0.02]
aw = sum(r['realized_usdt'] for r in wins) / len(wins) if wins else 0
al = sum(r['realized_usdt'] for r in losses) / len(losses) if losses else 0
print(f"总{len(rows)}笔 胜{len(wins)} 亏{len(losses)} 胜率{100*len(wins)//len(rows)}%")
print(f"平均每笔赢 {aw:+.3f}U | 平均每笔亏 {al:+.3f}U | 赢亏比(R:R)={abs(aw/al):.2f}" if al else "")
print(f"账户%口径: 平均赢{sum(acctpct(r) for r in wins)/len(wins):+.1f}% 平均亏{sum(acctpct(r) for r in losses)/len(losses):+.1f}%")

# 硬止损滑穿统计
hs = [r for r in rows if '硬止损' in r.get('reason', '')]
print(f"\n硬止损 {len(hs)}笔:")
slipped = 0
for r in sorted(hs, key=lambda x: acctpct(x)):
    ap = acctpct(r)
    mark = " ←滑穿(应≤-20%)" if ap < -22 else ""
    if ap < -22:
        slipped += 1
    print(f"  {r['_v']:<7} {r.get('symbol',''):<11} {r.get('side',''):<5} 账户{ap:+.0f}% 实现{r.get('realized_usdt',0):+.2f}U 持{r.get('hold_minutes',0)}m{mark}")
print(f"滑穿-20%止损的: {slipped}/{len(hs)}")

# 最大单笔亏 vs 最大单笔赢
if rows:
    mxw = max(rows, key=lambda r: r.get('realized_usdt', 0))
    mxl = min(rows, key=lambda r: r.get('realized_usdt', 0))
    print(f"\n最大赢 {mxw.get('symbol')} {mxw.get('realized_usdt'):+.2f}U | 最大亏 {mxl.get('symbol')} {mxl.get('realized_usdt'):+.2f}U")
