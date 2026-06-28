import json, datetime, collections

# 回放验证:对每所按时间顺序走 settlements,模拟「某币连亏 N 次后封禁 H 小时」,
# 统计被封禁期间跳过的那些交易的真实盈亏(即该规则能避免/错过多少)。
def load(v):
    rows = []
    try:
        for l in open(f"{v}_directional/settlements.jsonl"):
            l = l.strip()
            if not l: continue
            r = json.loads(l)
            try: r['_t'] = datetime.datetime.strptime(r.get('time','')[:19], '%Y-%m-%dT%H:%M:%S')
            except: continue
            rows.append(r)
    except: pass
    rows.sort(key=lambda r: r['_t'])
    return rows

for N, H in [(2,6),(2,12),(3,6),(3,12)]:
    avoided_net=0.0; avoided_n=0; kept_net=0.0; kept_n=0
    for v in ['okx','bybit','binance']:
        rows=load(v)
        ban_until={}   # sym -> datetime
        streak=collections.defaultdict(int)
        for r in rows:
            s=r.get('symbol'); t=r['_t']; pnl=r.get('realized_usdt',0)
            if s in ban_until and t < ban_until[s]:
                avoided_net+=pnl; avoided_n+=1   # 本会被跳过
            else:
                kept_net+=pnl; kept_n+=1
            # 更新连亏与封禁(基于真实结果)
            if pnl < -0.02:
                streak[s]+=1
                if streak[s]>=N:
                    ban_until[s]=t+datetime.timedelta(hours=H)
            elif pnl > 0.02:
                streak[s]=0; ban_until.pop(s,None)
    print(f"连亏{N}封{H}h: 跳过{avoided_n}笔(净{avoided_net:+.2f}U) | 保留{kept_n}笔(净{kept_net:+.2f}U)  → 规则后净≈{kept_net:+.2f}U")
