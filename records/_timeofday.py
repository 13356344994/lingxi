import json, datetime, collections

def load(v):
    out=[]
    try:
        for l in open(f"{v}_directional/settlements.jsonl"):
            l=l.strip()
            if not l: continue
            r=json.loads(l)
            try: t=datetime.datetime.strptime(r.get('time','')[:19],'%Y-%m-%dT%H:%M:%S')
            except: continue
            hold=int(r.get('hold_minutes',0) or 0)
            opent=t-datetime.timedelta(minutes=hold)          # UTC 开仓时刻
            r['_open_bj']=opent+datetime.timedelta(hours=8)    # 北京开仓时刻
            out.append(r)
    except: pass
    return out

rows=[]
for v in ['okx','bybit','binance']:
    rows+=load(v)

# 按北京日内小时聚合(开仓时刻)
byh=collections.defaultdict(lambda:[0,0.0,0])
for r in rows:
    h=r['_open_bj'].hour
    s=byh[h]; s[0]+=1; s[1]+=r.get('realized_usdt',0); s[2]+=(1 if r.get('realized_usdt',0)>0.02 else 0)
print("=== 按开仓「北京日内小时」聚合(两天合并,三所) ===")
print("时 | 笔  净U    胜率  条形")
cum=0.0
for h in range(24):
    n,net,w=byh[h]
    if n==0: continue
    cum+=net
    bar=('+'*min(int(net),15)) if net>=0 else ('-'*min(int(-net),15))
    print(f"{h:>2} | {n:>2} {net:+6.2f} {100*w//n if n else 0:>3}%  {bar}")
print(f"合计净 {cum:+.2f}U")

# 粗分时段
seg={'夜(0-7)':range(0,8),'上午(8-12)':range(8,13),'下午(13-18)':range(13,19),'晚(19-23)':range(19,24)}
print("\n=== 时段聚合 ===")
for name,hrs in seg.items():
    rs=[r for r in rows if r['_open_bj'].hour in hrs]
    if not rs: continue
    net=sum(r.get('realized_usdt',0) for r in rs); w=sum(1 for r in rs if r.get('realized_usdt',0)>0.02)
    print(f"  {name:<10} {len(rs)}笔 净{net:+.2f}U 胜{100*w//len(rs)}%")
