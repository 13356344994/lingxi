# 数据使用与服务器降载（2026-07-10）

## 直接进入当前真钱方向决策

- OKX 实时盘口/深度：入场可执行性、点差、数量；
- OKX 爆仓流：key 方向触发；
- 24h 暴动计数：惯犯池；
- 全市场 mid 内存环：冲量、天气 riot/breadth；
- fast 持仓、结算、probe state：退出、预算、缴械；
- 账户权益、交易所实仓、资金流：账户风控。

## 进入司天监/分拣器，但当前只打标签

- 1h `riot × breadth × volx` 天气格；
- 5min 燃料、潮汐和 30min 广度；
- particle ledger 与 auto_miner。

`fast_key_grid_gate_enabled=false`，所以天气映射目前没有改变真钱开仓，只提供格内/格外证据。

## 已采集但当前没有真钱消费者

- `fast_hot_micro`：热币秒级盘口/成交/OI；
- `fast_full_burst_probe`：全市场 tape burst；
- `fast_xmid`：Bybit 跨所锚（旧 E/xgap）；
- `fast_dynamic_feature_events` 与 scorer/alt/liq_follow shadow；
- `fast_hot_mids`：与全市场 mid 大量重复的热币路径；
- funding-study、rule-hole、band-pin、旧 universe/secondgen Python 监视器。

这些数据不是全都没价值，但“可能以后有用”不能无限占用生产服务器。已有历史保留一份；无当前消费者的实时采集停止。

## 本次降载

- 停止重复/过期后台进程：2× rule-hole watcher、band-pin collector、secondgen watch、旧 fast-universe monitor；
- 停止并禁用 `lingxi-funding-study.timer`；
- 关闭 `fast_hot_ws`、`fast_full_tape`、`fast_xmid`、alt candidate 和 liq-follow shadow；
- scorer 与 alt 都关闭时跳过 shortlist 的每4秒 REST 盘口/深度扫描；
- 全市场 mid 仍每3秒进入内存供方向决策，但磁盘落盘降为每15秒一次；
- hot WS 关闭时同时停止重复的 hot-mids 路径/锚落盘；
- 保留方向执行、司天监天气、粒子账本、OKX 爆仓、跨所爆仓轻量流、账户风控和真钱账本。

## 数据删除边界

真金结算、持仓、权益、审计、probe state、粒子账本和不可再生交易所事件不删除。
仅删除已有当前副本的旧轮换大文件；删除前确认主文件仍存在。

## 部署后结果

- 删除 `fast_hot_micro.jsonl.1` 与 `fast_dynamic_feature_events.jsonl.1`，释放约 **4.9GB**；
- records 总量约 **15GB → 11GB**，根分区可用空间约 **43GB → 48GB**；
- Lingxi 进程 CPU 从维护前约 **4.4%** 降至部署后约 **1.7%**；
- 新日志不再出现 hot-ws、full-tape、xmid 和 scorer/alt 扫描；
- 司天监从 1135 行全市场 mid 尾部恢复约 45.9 万点，重启后天气立即可用；
- 四所实仓复核为零，账户风控可信、无硬停。
