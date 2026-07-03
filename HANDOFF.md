# LINGXI 方向量化 — 交接文档(2026-06-28;07-02 晚追加 §-1 事故与重建)

## -2. 2026-07-03 因子哲学修正 + 动机层启动(最新,先读)

**用户三层纠偏(务必内化,统计口径由此改变):**
1. **时段不是变量,是索引**:v1 账本 24 个 UTC 时段仅 08 时一个小时勉强毛正(+0.69bps),不存在"好时段"只有"亏得慢的时段";弱时段效应大半是点差/流动性的影子(点差≥2bp 桶内 weak/norm 无差)。**任何按钟点分桶下的结论都要先问"它代理的物理状态是什么"。**
2. **机械因子(点差/失衡/冲量)只描述行为不解释动机,同维度相关、天花板锁死**。可预测性来自"不得不"(强平/资费结算的被迫流)与"忍不住"(FOMO/整数锚/恐慌)。**91k shadow 大样本已验:score≥0.90 内,贴整数锚(<3bp)跟随率 57.0% vs 远锚 65.5%(8.5pp 正交分层);薄盘<5k 跟随率仅 50.3%;`impulse_continuation` 类足迹系统性最差(我们自己就是 FOMO 方)。** 脚本:`records/_fast_motive_factors.py`(只读)。
3. **资金量约束选战场**:几千 U 玩机构的游戏(主流币微结构/爆仓狙击)=送人头;唯一结构性优势是体型——机构进不去的长尾/股票 perp 池塘(v1 唯一 PF>1 的 symbol = SKHYNIX 股票 perp;lag-follow 位移最大全是长尾)。**因子研究与未来 universe 优先聚焦该子池;"刷 VIP"划掉,返佣保留。**

**最大机制发现(比因子重要)**:信号时刻跟随率 60%(shadow mid 口径)→ 成交后只剩 ~27%(v2 结算 post-fill 口径),**maker 逆选择吃掉一半以上的看对率**——挂单恰在对手穿越时成交。fast 转正的下一个结构性假设应优先攻这里(入场执行方式),其次才是 scorer 校准(大样本下辨别力仅 58.9%→67.5%,平庸)。

**已上线(2026-07-03 12:12 BJ 重启部署,record-only 零交易影响)**:全市场 SWAP 爆仓单采集 `fast_liquidation_events.jsonl`(挂 fast_ws 连接,`fast_liq_collector_enabled=true`;单测 `fast_liq_collector_test.go`)。数据已流入(含长尾币)。**用途:攒"强平密度/方向"动机因子,与 shadow/结算对齐做 P(跟随|强平流) 校准;不足样本前不得上任何实盘规则。**

**🚀 v3 蓝图三层已落地(2026-07-03 14:36 BJ 重启部署,用户批准"可以";单测 `fast_v3_pond_test.go`,部署后已核对数据流入)**:
- **① 池塘质量(战场层)**:每个 shadow 事件带 `pond_energy_med_bps/pond_depth_med_usdt/pond_samples/pond_ok`(滚动 2h 逐扫描中位数,动态判定,禁止写死名单;热身期<150样本 fail-open)。依据 119k 事件实证:能量(30s位移中位)是主轴,好池全部 ≥9.6bps,死水镜面 SNDK/MRVL 深度17万但能量<6→跟随率38-56%,CELO 能量3.2 全场最差。**当前仅打标(`fast_pond_filter_enabled=false`);打标 1 天与离线排名核对命中集一致后才开过滤(开后 fastLiveOpen 拒 `pond_reject`)。**
- **② 接型候选生成(入场层,record-only)**:`fast_alt_candidates_enabled=true`,每轮扫描从同一盘口快照派生两类"接"型候选写入 shadow(`candidate_source` 标记):`liq_catch`=强平后 60s 接被迫者(强平内存缓冲由 fast_ws 爆仓推送实时喂,P2 首验 82.0% 全场最高)、`fade_exhaustion`=30s 冲量≥8bps 且 15s 已减速<40% → 逆向接力竭者。依据:91k 实证同冲量档"接"比"追"高 8-15pp,而旧生成器 98% 产追单。**`fastLiveOpen` 对 `candidate_source` 非空硬闸拒绝(`record_only_source`),绝不实盘;攒 1-2 天前向成绩(离线按 candidate_source 分桶)达标才谈灰度。**部署 2 分钟即有 fade 42 + liq_catch 12 条流入。
- **📌 首日前向裁决(2026-07-03 16:00,部署后1.5h,base shadow 做前向标尺)**:
  - **fade_exhaustion 活着**:n≈3500,+60s 跟随 52.2% 均值 +1.73bps,且随窗口放大(15s+0.31→60s+1.73→180s 池塘OK子集 **+4.29**);偏态收益形状(错的多但对的走得远)正是"接"该有的样子。继续攒样本。
  - **liq_catch 证伪,且解剖后连"力竭确认"也救不了**:整体 60s 均值 -18.6bps;按 15s 冲量分桶:已翻向(假力竭/死猫跳)**更差** -22.9bps,仍在砸 -17.6bps,横住 -9.8bps——全负。**假的部分预示的真东西:被迫流方向在这些池塘持续 1-3 分钟(180s 衰减到 -3.5),60s 内顺瀑布方向 = +17~23bps 的镜像信号(liq_follow)**。P2 之前的 82% 不矛盾:那批是 scorer 候选叠强平(=强平仍在砸但价格已在涨的"吸收"证据),条件完全不同。→ 接被迫者的合格前提是**吸收证据**(强平未停但价格顶住),不是时间窗也不是 15s 翻向;镜像 liq_follow 与吸收条件均可从现有 shadow 字段离线推导,不必改生成器,灰度设计时再定。
  - 池塘打标与离线 119k 排名吻合(OK: LAB/ZKP/KORU/SLX/SOXL;REJ: CELO/SNDK/MRVL/SKHYNIX 等 19 个);反事实:1.5h 实盘 28 笔中 5 笔开在 REJ 池共亏 -0.037U。**打标核对通过,过滤闸具备开启条件。**
- **③ 物种化出场**:`fast_species_exit_enabled=false`(代码已就位,默认关,随②灰度一起开)。

**🔒 铁律:禁止漏斗套漏斗(2026-07-03 17:41 用户架构纠偏)**:"先挑一批再从中选"是静态名单思维——慢线按 5m 标准选 20、池塘砍到 5、冷却冻剩 0-2,每层标准都不是超频策略自己的,幸存者=末段行情,进场即接反转。超频策略要的是**流程快 + 挑选范围全市场 + 资格当刻判定**:让事件(爆仓/被迫流)把注意力拉过去,秒级现场验资格,吃到肉就走。静态判定(池塘名单)只约束轮询线,不得约束事件线。

**🚀 v3④ liq_follow 事件驱动全市场入场(2026-07-03 18:16 部署,上述纠偏的直接执行)**:
- 通路:爆仓推送(全部 SWAP,已订阅)→ `fastNoteLiqHot` 热队列(20s 去重)→ `fastLiqFollowLoop` 每秒消费 ≤2 币 → 30s 内同向净强平 ≥2 笔(`fast_liq_follow_min_liqs`,单笔=噪声)→ 当刻资格:点差 ≤12bps、10bp 深度 ∈[5k,15万](薄盘吃不下,机构镜面盘不去)→ **顺被迫流方向 taker 进场**(多头爆=跟空;挂 maker 等成交=流已死接反转)。
- 离线依据:liq_follow 镜像 1622 事件四窗口全正(+60s +6.5bps,3h 窗口稳定),当日最稳机制;liq_catch(逆着接)已证伪且永久硬闸。
- 豁免与共用:静态池塘判定不拦 liq_follow(事件线当刻验);冷却/单币退避/日熔断/持仓互斥/急停全部照过;结算带 `fast_candidate_source=liq_follow`。
- **限频礼让(用户 17:45 插话"别把账号搞封了"的直接回应)**:每秒 ≤2 币 ≤4 个公共请求;buildFastEvent 连续 3 次失败 → 整线退让 60s,绝不硬扛 50011。审计:当日 11 次 50011 全部来自慢线选币的近窗量拉取(公共端点,按 IP 限频,有 K 线回退,不涉账号);私有下单频率 ~150 单/日,远低于任何风控线;真正的封号风险(高频挂撤比/自成交/API 滥用)本系统均无——maker 入场每次 1 挂 1 撤封顶,taker 是 IOC 单发。

**🚀 v3⑤ 出场能量单位制(2026-07-03 19:11 部署;起因=用户"别猜了,从前往后按日志逐步深挖"的直接执行)**:
- **逐笔对账结论:三条线管线无 bug**。12 笔 liq_follow 方向全部与被迫流一致(如 BASED L5/S0→short✓);18 笔 fade 方向全部正确反向 imp30;事件→成交延迟中位 2s;taker 滑点均值≈0(liq_follow 平均还赚 2.8bps 入场价)。**亏损不在"线路",在出场刻度。**
- **被日志钉死的结构矛盾**:出场阈值(灾难25/止损15/A斩6/TP12 bps)全是死水池时代(能量3-6bps,灾难线=4-8个噪声步长)标定的绝对值;池塘过滤把交易搬进 LAB(能量中位30.7bps)/ZKP(15.3)/SLX(11.0)后,**LAB 里灾难线只剩 0.8 个噪声步长**——纯噪声半数概率 30s 内触发"灾难"。回放证据:门开后 14 笔灾难止损 7 笔、12 笔 A 斩 7 笔,砍掉后 180s 价格回到比砍价更优 = 一半止损在收割自己选出来的能量;LAB 一个池占当期总亏 42%。盈亏不对称同源:TP 封顶 +12bps、止损实际成交 -29~-55bps(高能池滑点穿线),mfe30 多笔到过 +17~+26 却只落袋 12——噪声 30bps 的池子里 +12/-25 结构方向全对也是负期望。
- **修复(机械换尺,非新假设)**:`fast_exit_energy_scale_enabled=true`,能量成熟池(样本≥150)出场阈值 = max(原绝对下限, k×池塘能量中位):灾难 k=2.0、止损 k=1.2、A斩 mae k=0.6(cur k=0.4)、TP k=0.8;能量未知(全市场 liq_follow 新符号/热身期)回退原绝对值。LAB 实测:灾难 61.4 / 止损 36.8 / TP 24.6 bps。trail(唯一净正出场,+0.085U/65%胜率)本轮不动。单测 `TestFastScaledExitBps/TestFastExitUnitBps`;yaml 护栏已锚定。
- **代价自知**:高能池单笔学费上限变大(LAB 灾难 61bps×8U≈0.05U),由日熔断 -3U 兜底;换来的是不再花钱买噪声刻度的假止损。
- **部署后 40 分钟核验(20:00)**:出场端修复确认生效——fast_tp maker 出场 2 笔全净正;能量刻度砍单 0 笔白砍(修复前一半白砍);单笔最大亏被压到 -0.02U。但盈亏结构暴露入场端真相:胜率 25%、盈亏比 0.53(需 65% 胜率才平),亏损重心=liq_follow(8笔-0.056U)。

**🚀 v3⑥ liq_follow 盲区闸(2026-07-03 20:05 部署;用户看单质询后的逐笔解剖直接执行)**:
- **钉死的实现缺陷**:实盘 8 笔 liq_follow 里 7 笔进场时该币零 mid 历史(imp30=0)=蒙眼买瀑布尾,不知瀑布已跑多远;唯一有历史的一笔(BASED imp30=-192,瀑布刚砸完)顺势 +21bps 是全场最舒服单。离线 +6.5bps 依据的 1622 事件样本**全部有完整 mid 历史**——实盘却放零历史币盲进,条件与依据不对齐,这是缺陷不是运气。
- **修复**:入场资格加 `blind_no_history` 闸——mid 历史跨度 <15s 不下单只记观测(buildFastEvent 本身写入观测点);瀑布若真还在走,新爆仓会再把该币顶回热队列,15s 评估冷却后带真实冲量再评。**"瀑布还活着"由此成为入场资格的一部分**(死瀑布不会产生第二波爆仓,自然过滤)。单测 `TestFastLiqFollowBlindGate`。
- 下一裁决口径:liq_follow 首单全部带真实 imp;胜率/盈亏比是否离开 25%/0.53 的抛硬币区。

**📋 全量反思(2026-07-03 20:20,用户"整体思路有问题,从头反思"指令;606 笔全量结算解剖,无挑样)**:
- **五个钉死的事实**:①净-3.15U=毛-1.66U+费1.49U,免费也亏,非费用问题;②成交后3s顺向占优仅 **12%**(三线、maker/taker 全一样),10s 赛跑先-8bps:先+8bps=219:112,反向者 30s 平均多吃+3.9bps——**我们的成交时刻系统性在别人动手之后**;③60% 单子 30s 内从未顺向≥8bps(无肉单 366 笔合计-3.73U>全部亏损),**入场根本没选在能量释放时刻**;④信号→成交延迟中位 **4s**(4s扫描+800ms挂等),秒级战场拿旧照片进场;⑤maker 账面 edge≥3bps 的单 mae30 中位 -13.3 vs edge 小的 -7.6——**挂单成交本身就是坏消息,省3bps费付8bps逆选择税**。
- **出场端已修好不是问题**:有肉单(mfe30≥15,131笔)捕获率52%、107/131 落袋为正。修复三天效果 -0.29→-0.22U/h,亏得慢了但符号没变——**前半截 88% 逆风的入场没有任何出场能救**。
- **自我批判**:scorer 占 91% 子弹(551/606,净-2.67U),高分有肉率仅 26%(vs低分16%,不预测运动);我一直给它加池塘闸/物种出场/能量刻度=**在坏水流上加过滤器而不是换水源,自己犯了漏斗套漏斗**。注意力倒置:强平流覆盖392币68个簇,事件线只分到31发子弹。
- **数据指的活路**:唯一带真实冲量顺瀑布 taker 进的单(BASED imp30=-192)+21bps 全场最舒服=用户构想的"快速吃到就走"被真金白银验证过一次。**换枪方向(待用户拍板):主火力从4s轮询maker打分线换到全市场事件触发+能量释放确认+taker即进;scorer 降级 record-only(shadow 留作地图)。**

**🚀 v4 换枪(2026-07-03 20:32 用户拍板"听你的 我相信你";上述全量反思的直接执行)**:
- **`fast_scorer_live_enabled=false`**:scorer 轮询线降级 record-only(shadow 照记当地图,`scorer_record_only`)。依据=606 笔解剖:占 91% 子弹净 -2.67U、3s 顺向占优 12%、60% 无肉、maker edge 越大 mae30 越深(逆选择税)。**回开须用户明示(护栏测试锚定)。**
- **liq_follow 加第二道能量闸 `cascade_dead`**:盲区闸(零历史不进)之后,15s 冲量须与被迫流同向且 ≥5bps(`fast_liq_follow_min_imp15_bps`)——瀑布还在走才有肉,翻向/横住=进场即接反转。唯一 +21bps 舒服单(imp30=-192 顺势)是正面印证。
- **换枪后格局**:实盘子弹=liq_follow(事件驱动全市场 taker)+ fade(成熟OK池,maker,量小待观察);出场机器(能量刻度+物种+trail)不动;限频礼让不动。**交易频率会大幅下降,这是设计意图(宁缺毋滥)。**
- **裁决口径(骗不了人的两个数)**:换枪后"成交后 3s 顺向占优率"必须离开 12% 泥潭;"无肉单占比"必须从 60% 明显下降。单测 `TestFastScorerRecordOnlyGate`/`TestFastLiqCascadeAlive`。

**📌 v4 首验 + fade 解剖降级(2026-07-03 21:10)**:
- **换枪首 28 分钟(18 笔)两个裁决数全部大幅正移**:3s 顺向占优 12%→**33%**,无肉单 60%→**39%**,胜率 25-30%→44%。liq_follow 9 笔净 -0.009U≈打平(5 胜,TP 单全部净正;blind/cascade/深度三闸实战可见)。剩余亏损几乎全部来自 fade(9 笔 -0.070U)。
- **fade 解剖(35 笔实盘 vs 同期 9032 全体候选,信号口径前向)**:成交价无滑点(还赚 1.5bps),但被成交的单 60s 前向 **-10.6bps** vs 全体 **+1.0bps**——差 11.6bps = **maker 成交本身是力竭诊断的证伪器**(诊断对→价格反转够不到挂单;诊断错→人群还有子弹砸穿挂单继续冲)。且群体优势仅 +1.0bps@60s,taker 化也付不起执行成本(≥5bps)——当前无可收割形态。
- **处置(三问过后,非砍掉了事)**:`fast_fade_live_enabled=false` 降 record-only;候选生成不动(地图);学到的反向信息记档:**fade 挂单被成交 ≈ "人群还有子弹"信号**。重估条件:群体优势增厚到 >5bps 或找到吸收证据类入场确认。护栏测试已反锚(回开须用户明示)。
- **换枪后主火力=liq_follow 单线**,出场机器不动。

**🔒 铁律:每次更新服务必须 git push(2026-07-03 17:29 用户指令)**:任何"改代码/配置 → 重启上线"的动作,完成后立即 commit + push 到远端,不留过夜欠账。这是 07-02 工作区被毁事故的直接教训:远端是最后一道防线,本地未推送的工作 = 随时可能蒸发的工作。顺序:build/test 绿 → 重启验证存活 → git add -A → commit(带动机说明)→ push → 核对 `git rev-list origin/main..HEAD --count` = 0。

**🔒 铁律:禁止日历思维(2026-07-03 16:44 用户纠偏,当日第二次犯)**:"攒 24h 数据/明天再裁决/差 0.01bps 不过线" 全是把钟表当审批流程。秒级策略 2h=数万决策周期,样本充分性由机制预言的分辨率决定,不由日历决定;数据是结果,机制先行,结果只用来核对预言——有了结果再回头找流程没有意义。判据差 0.01bps 卡手 = 把噪声当圣旨。

**🚀 v3 三闸当日全开(2026-07-03 16:55 重启部署,上述纠偏的直接执行)**:
- `fast_pond_filter_enabled=true`:池塘过滤实盘生效,死水候选每轮拦 150-220 个(`pond_reject`);**重启回放已补**(`fastPondRestore` 从 shadow 尾部恢复 2h 样本,重启不失忆,免死水池吃热身 fail-open 免检窗口——CELO 曾在重启后 10 分钟窗口漏开 1 笔)。
- `fast_fade_live_enabled=true`:fade 接型入场灰度,**仅成熟 OK 池塘**(热身 fail-open 不适用于 fade 实盘资格);无 scorer 分数(机制条件即资格),跳过分数闸,冷却/退避/熔断/互斥全部照过;结算带 `fast_candidate_source` 分代对照。liq_catch 永远硬闸。
- `fast_species_exit_enabled=true`:A=3s mae≤-6 且当前≤-4 → `fast_species_a_cut` 豁免 minHold 立即认错(+入冷却);C=10s 无正反馈不深亏 → `fast_species_c_exit` 快撤。部署数分钟内两者均已正确触发。
- 生产 yaml 护栏测试已锚定 v3 三闸(回退即测试红);每日裁决:`python3 records/_fast_v3_daily_verdict.py`(口径固化,含 liq_follow 镜像跟踪)。
- **最小票适配(17:17 补,上线即暴露的结构矛盾)**:LAB 最小票=0.1张=1币≈8U、KORU=0.01张≈5.9U,都 > 5U 灰度名义 → 量化 0 张永远 `not_tradable`,**能量最高的战场反而从未入场过**(LAB 开仓数=0 印证)。修复:交易所最小票额=入场费,票价 ≤ 2×配置名义就买最小票(`minVenueTradableQty`,MinNotional 同步抬),更贵才真跳过。修后 fade 实盘首单已成交(SLX/ZKP `alt_opened`),KORU 首次进场。A物种=3s 内 mae≤-6bps 且当前≤-4 → `fast_species_a_cut` 立即认错(豁免 minHold;424笔实证 A 物种 PF0.26 仅26%回正,minHold 对它是刑具)+ 计入冷却;C物种=10s 无正反馈(mfe10<1)且不深亏 → `fast_species_c_exit` 快撤换弹(PF0.51 时间成本主导);B物种(打完即弹,PF1.50 唯一毛正)不动,交给既有 trail。

**v2 判定口径(修正后)**:① 机制层对比 v1 vs v2 的"看对单捕获量 / 看错单出血量"(不经时段);② v2 内部按点差×动量物理状态分桶;③ 灾难线穿透专项(名义 25bps 实际成交 -44.9bps,61 笔占 v2 亏损 ~68%);④ 时间只做因果校验轴。**毛均仍负 → 出场时序假设证伪 → 停线回 record-only(数据留作 v3 地图,不是扔掉)。**

**🔒 铁律:假设必须先有对手模型,禁止"分桶差→加补丁"式偷懒假设(2026-07-03 用户纠偏)**:"看到某桶更差就提议对该桶加闸/参数"不是假设,是把思考外包给数据、伺候症状的均匀刀。合格假设 = 先回答"**对面是谁、他为什么必须这么做**"→ 登记预言 → 再去数据对质。示范(fast 成交对手模型,P1 已证):我们的 maker 成交=有人付点差打我们,只有两种人——**A 点火者(后面有子弹)**:3s 深亏≤-6bps,n97 毛均-15.8/PF0.26/仅26%回正;**B 一次性被迫者(打完即弹)**:3s 浅亏且已弹≥1bps,n94 **毛均+4.0/PF1.50**(唯一毛正物种,60s MFE+18.5);C 死水 n114 PF0.51。→ **v2 minHold 是 A/B 混斩的均匀刀;v3 设计=成交后3-5s对手分类(B持有等弹/A立即认错/C快撤)**。P2 已登记待爆仓数据:成交±N秒同币有强平 → 更像A。(424笔单窗口,强线索非定论。)

**🔒 铁律:负结果禁止"砍掉了事"(2026-07-03 用户纠偏,已犯过一次)**:任何证伪/亏损/失效的方向,在决定去留前必须过三问——①哪部分是真的?②哪部分是假的?③**假的部分本身预示/教了什么?** 示范案例:scorer"超自信倒挂"(最高分 1000 笔仅 53% 跟随)解剖后发现:非市场反转信号(反向开同样 53.5%/无肉)、非"scorer 方向没价值",而是**具体构造缺陷 = 100% 集中在 SNDK(点差 0.06bps 的死水标的),scorer 把"容易交易"误当"值得交易",缺能量项 + 无逐币归一**——砍掉会留下错误结论,解剖得到可修的病因。同理:"刷 VIP"=挂起(重估条件:资金放大),不是划掉;weak_hours=便宜采样保留至物理状态闸替代,不是禁开;v1 账本孕育 v2,v2 数据孕育 v3。

## -1. 2026-07-02 事故与重建(最新状态,先读这段)

**事故**:07-02 下午,外部进程(疑似 `lingxiopt` 巡检 agent 的破坏性 `git checkout/reset`)把工作区 ~408 个已跟踪文件 + 全部未跟踪新文件抹掉。当日全部未提交工作(大精简 + 账户服务重建 + MAE 刹车等)源码丢失;**当日编译好的 binary 幸存并被备份**(`/root/lingxi_GOODBUILD_0702_0753.bak`),`.git` 备份在 `/root/lingxi_git_backup_0702`。

**重建(本次提交)**:按会话记录把当日工作全部重写并验证(build/vet/test/race 全绿),已部署重启:
- **大精简**:删 carry/五策略轮动/遗留 sniper(internal/app 238→~50 个 Go 文件);共享工具重新安家到 `venue_shared_helpers.go` / `quantity_helpers.go` / `store_helpers.go`。
- **账户服务重建**:`equity_snapshots.go` 的 `AccountEquityService`(修复 OKX 被排除在权益采样外的 bug)+ `strategy_settlements.go` 统一结算模型;`account_risk_watch.go` 改喝方向结算真数据(修复"账户风控空转"P0)。
- **风控新闸**:MAE 灾难刹车(-3% 砍,灰度 okx/bybit)+ runup 临门确认闸(灰度 okx)+ 方向开仓尊重 global_halt + maker 部分成交按实际成交额记账 + PositionID 平仓并发 claim。
- **风控阈值重校准**(88U 微仓口径):日损 8 / 账户回撤 115 / 策略累亏 80 / 笔数闸关。
- **用户指令已生效**:`max_concurrent=10`;OKX control.json `trading_capital_usdt=100`。
- **面板重写**:`webpanel/index.html` 全新方向交易面板(账户/各所资金/引擎控制/持仓/结算)。
- **研究与自动化**:`records/_fee_posture_advisor.py`(费率姿态顾问)、`records/_fast_readiness_report.py`(fast 重启三闸 GO/NO-GO)、`records/_fast_funding_moment_directional.py`(结算瞬间研究,systemd 每日 4 班自动积累)、`records/fee_rebate.json`(返佣谈成后填 `okx_rebate_share`)。

**数据损失(不可恢复,记账口径注意)**:已跟踪的 `records/*/settlements.jsonl` 被回滚到 6-28 版本,**6-28→7-02 的结算行丢失**(交易本身真实发生过,真数据在交易所;需要时可从交易所成交历史 API 重建)。持仓文件已按交易所实盘核对重写(7-02 21:26)。

**返佣落地后的"惊喜"操作步骤**:① 把谈到的比例填进 `records/fee_rebate.json` 的 `okx_rebate_share`(如 0.45);② 跑 `python3 records/_fee_posture_advisor.py` 看新成本结构;③ 跑 `python3 records/_fast_readiness_report.py` 看 fast 实盘线三闸是否转 GO;④ 三闸全过才把 `fast_scalper_v1_enabled: true` 灰度重启。

**🔄 fast v2 重启(2026-07-03 凌晨,用户明确决策,有意越过三闸)**:
- **依据(交易所账本,不是本地记录)**:`records/_fast_exchange_truth.py` 从 OKX fills-history 重建 v1 全部 **6304 笔往返**(本地结算 7-02 事故丢失;缓存 `records/fast_exchange_fills_cache.jsonl`)。解剖:整体毛均 -3.71bps/毛PF 0.65(扣费前就亏,返佣救不了 v1 跑法);**但 <15s 出场 1690 笔毛均 -12.8bps(PF 0.20)是出血主源,60-180s 桶 532 笔毛均 +2.7bps(PF 1.36)、≥180s 桶 929 笔 PF 1.09 毛正**;SKHYNIX(股票perp)490 笔 PF 1.27。结论:信号方向真实,出场翻炒杀死期望。注意 ≥60s 桶有幸存者偏差(未触止损才活到那),v2 即是对该假设的实盘验证。
- **v2 改动(只修出场时序,不动打分器/信号)**:`fast_min_hold_seconds=60`(期间禁 early_cut/stop/no_feedback 等一切亏损驱动出场;trail_tp 锁盈不受限)+ `fast_disaster_stop_bps=40`(任何时刻有效,防插针)+ `fast_daily_loss_limit_usdt=3`(fast 层独立日亏熔断,当日停开、北京日切恢复、重启从 settlements 重建计数)。
- **灰度**:仅 OKX、名义 5U(`fast_live_notional_usdt=5`)。结算带 `fast_exit_version:"v2"` 且另存 `records/okx_directional/fast_v2_settlements.jsonl`(与 v1 学费数据分代对照)。生产 yaml 测试已锚定全部 v2 护栏(缺一测试即红)。
- **复盘口径**:v1 vs v2 对比看毛均 bps/PF/出场原因分布;若 v2 仍毛负 → 出场时序假设证伪,停线回 record-only,别再调参。
- **v2.2 自适应惩罚(2026-07-03 02:30,用户纠偏"禁开=偷懒的黑名单,要自适应不要收网")**:
  ① **单币亏损退避**(v1 最大教训:同币日内无上限重复锤,CELO 两天 1124 笔,反事实模拟单币亏3笔止步可减 96% 出血):该币当日每净亏一笔升一档 → fast 冷却 300s×3^档(15m/45m/…cap 2h)+ 再入场分数门槛 +0.05/档(cap +0.25);**净赢一笔降一档**,北京日切重置,绝不禁开——行情反转、信号重新证明自己即自动恢复。
  ② **弱时段自适应**(v1:UTC 00-08 占一半亏损,唯一毛正时段 UTC08):该时段不禁开,门槛 +0.10 且名义 ×0.5,便宜地继续采样;结算带 `entry_adaptive_tags`(loss_lvlN/weak_hour)供分桶评估弱时段高分单是否可救。
  ③ **灾难线 40→25bps**(v2 标定:胜单最深回撤 P50=-1.9bps 几乎无破 -25,不误杀;灾难单 10s 内已 -26bps,早砍省 15-20bps/笔)。
  ④ **盈利出场 maker 化**:trail_tp/max_hold 平仓改非紧急 maker-first(省 ~3bps,taker 5→maker 2;费用曾吃掉 trail_tp 毛利一半),止损/灾难/早砍保持 urgent IOC 秒平;maker 未成交有平仓 IOC 兜底不挂死。
  单测:`fast_adaptive_test.go`(门槛递增/赢单降档/cap/弱时段/冷却退避)。
- **v2.1 参数对齐(2026-07-03 02:00 与用户逐条确认)**:① `fast_min_hold_seconds` 60→**30**(账本细分桶:0-30s 全负、30-45s/45-60s 桶 PF≈2 全曲线最强,60s 把最优出场窗口锁在门外);② fast 日熔断改**每所交易本金×30%**(`fast_daily_loss_pct_of_capital=0.30`,OKX 100U→30U,随页面调本金缩放);③ **账户级日亏硬停 8→35U**(用户明确接受单日 ~22% 权益回撤;不抬则 9.2U 账户级先全停,30% 学费预算是虚的——层级:fast 30U 先熔只停 fast,35U 才全停);④ 面板:今日统计拆 慢线/fast 两行 + 今日开仓数(含在场),持仓时长 <2min 显示秒。

**2026-07-02 深夜整改批次(全代码审计后落地,不改任何信号/入场/出场参数,不影响开单频率)**:
- **P0 二级全平接线**:`flattenFn` 此前从未绑定=死代码;现 `run.go` 收集全部方向引擎后绑定 `FlattenAll`(urgent IOC 逐仓平)。**是否触发仍由 `auto_flatten_on_hard_drawdown_enabled`(默认 false)决定,接线本身零行为变化**。
- **P0 风控数据源告警**:`settlements` 加载失败原先被 `_` 静默吞掉(回撤闸静默失效);现日志+critical 告警,权益口径闸继续生效。
- **P0 Telegram 风控推送**:secret.yaml 配了 Telegram 即自动启用 `AlertingConfig`(此前 Enabled=false,硬停只写日志不推送,人不在电脑前不知道出事)。MinSeverity=warning 不刷屏;硬停告警幂等收敛(已 halt 态不重复推)。
- **P1**:MAE 灾难刹车补进 90s manageOne(原仅 20s 快环,快环盘口失败时 -3%~-5% 有盲窗);fast 持仓改原子写+失败留日志(原 os.WriteFile 静默);fast 仓字段改持锁更新(修与 persist 的数据竞态);20s 快环盘口失败记 last_error(原静默跳过);Bybit `FetchDepth` 修 category(原硬编码 linear,现货深度查错市场);审计日志 append 加 fsync。
- **配置对齐**:config `stop_loss_pct` 0.20→0.05(=实际生效值,行为零变化);`take_profit_pct` 注明死参数;systemd 的 `LINGXI_AUTOSTART_LIVE` 是幽灵开关(代码不读,重启恢复交易由 control.json active 决定),deploy 文件已清理;flatten/posq/livecheck 支持 `LINGXI_SECRET_PATH`。
- **新增回归测试**:`account_risk_watch_wiring_test.go`(settlements 失败告警 / 硬停幂等 / 二级全平接线+幂等)。
- **4 小时快照备份已上线**(用户要求,直接回应 7-02 工作区被毁事故):`deploy/backup_snapshot.sh` + `lingxi-backup.timer`(每 4h 的 :03 触发,UTC 00/04/08/12/16/20)。关键记录(settlements/持仓/控制/权益/风控/审计)+ 服务日志 + config.yaml 打包到 **/root/lingxi_backups/**(独立于工作区,git reset 伤不到);服务日志归档后 truncate 切段(每包=一段 4h 日志),**交易记录只复制绝不截断**;保留 90 个包(~15 天);刻意排除 fast_dynamic_feature_events.jsonl(628M 研究流,可再生)。恢复:解包对应时间的 tar.gz 取 records/ 下文件。
- **面板令牌 cookie 回退**(只读路径):records/、审计日志是 <a href> 导航带不了请求头,保存令牌时同步写 lx_token cookie,后端只读鉴权回退认 cookie;**控制类接口仍只认请求头**(防 CSRF)。

---

> 给接手 agent:看这一份即可接手。系统为**四所方向量化"微仓广撒网探索档"**,目标是用户的"小资金广撒网多捞钱 / 超频 scalper"。当前**尚未找到扣费后稳健正 edge**,处于微仓采样阶段。

> ⚠️ **定性写死(务必认清,别再误判)**:**当前系统是"微仓 5m 慢信号探索器(micro 5m exploration)",不是最终的超频 scalper(ultra-frequency scalper)。**
> - 信号来自 **5m K 线 regime(慢)**;入场 = 5m 收盘即开(无 1m/30s 临门确认);管理 = 90s 完整循环 + 20s 硬止损(非秒级)。
> - **30–60min 的持仓时长是"慢信号"的必然结果**,不是 scalper 行为。离线已证:在 5m 慢信号上缩短持仓/scratch/价格型入场确认**都不能把 PF 拉过 1**(见 §4)。
> - **真正的 scalper 需要新增一个 fast layer**(秒级成交后反馈 + 1m/30s 微结构入场确认 + 3-5s 快速管理),而不是把 5m 慢信号跑快一点。
> - 已加 **fast scalper 骨架开关(全部默认 false、当前无任何引用=inert,改值不影响实盘)**:`fast_scalper_enabled / post_fill_feedback_enabled / fast_manage_enabled`。结算/持仓已加 **分层标记** `signal_layer / entry_layer / management_layer`(当前全 = `slow_5m` / `slow_5m_close` / `slow_90s_20s`),为未来"慢探索层 + 快 scalper 层"并存预留。**启用 fast 层前必须先实现该层 + 离线验证 + 用户确认。**
> - ⚠️ **骨架已实现但【未部署】(6-28 20:36)**:上述开关 + 分层字段的代码已在工作区(build/vet/test 全绿),但**运行中的 binary 尚未包含**,故新仓/结算暂时还不带 `signal_layer` 等字段。**刻意不为它单独重启**(避免打断 OpenMicro v2 / settlement env 采样节奏)。**下次因其他必要原因重启/部署时顺带带上即可。未 commit。**

---

## 0. 铁律(务必遵守)
- **单一写入者串行**:后台另有一个 45min 运维巡检 loop agent(`lingxiopt`)也会改代码/重启。改动前注意冲突;一次只一个写入者。
- **不凭直觉上量**:任何边际敏感改动(出场时序/选币/信号)**先用离线框架验证**再上线。已有惨痛教训(资费 carry 被单窗口过拟合骗到)。
- 改完必须 `go build ./... && go vet ./internal/app/ && go test ./internal/app/ -run 'Directional|OKX|...' -count=1` 全绿再部署。
- **只在用户明确要求时 git commit**。

---

## 1. 部署 / 运行
- 单 binary,systemd 管理。部署:`./start.sh`(go build + SELinux restorecon + `systemctl restart lingxi`)。
- 日志:`records/lingxi_server.log`。状态:`systemctl is-active lingxi`。
- 面板:`http://127.0.0.1:8080/`;方向 API:`/api/okx-directional?venue=okx|bybit|binance`(含 stats_today/stats_all_time)。
- 结算:`records/{venue}_directional/settlements.jsonl`;持仓:`.../positions.json`;控制:`.../control.json`。

## 2. 配置优先级(重要,踩过坑)
**`config.yaml` 是真源,覆盖 `internal/core/defaults.go`**;`records/{venue}_directional/control.json` 再覆盖 config 的 `active / trading_capital_usdt / stop_loss_pct`。改参数优先改 config.yaml(否则改 defaults 无效)。`stale_cut_minutes` 不在 config → 用 defaults(=30)。
- ✅ **三处不一致已于 2026-07-02 晚对齐**:`stop_loss_pct` config 已改 0.05(=control/defaults 实际生效值,行为零变化);`take_profit_pct` 仍是**死参数**(live 出场走 `decideDirectionalPositionAction`,不读它),config 注释已标注;`max_hold` config=60 是有意配置(覆盖 defaults=30),非漂移。
- ✅ **生效参数已上启动日志(2026-06-28)**:引擎启动打印 `[OKX方向][生效参数]` 四行(本金/止损/杠杆/并发/每仓名义、入场闸、出场闸、regime 闸),已含 control 覆盖。**排查"改了配置没生效"先看这四行。** 代码 `logEffectiveParams()`。

## 3. 当前线上配置(已部署,2026-06-30 09:30 更新)
- 活跃所:**OKX / Bybit**(active=true);**Binance 已停新开(active=false,6-30,仅看护在场仓)**;**Gate 已停用**。
- 资金档(capital_regime,**已分化,统计务必分开**):**OKX=50U、Bybit=50U(6/28 19:25 BJ 起)、Binance=15U(已停新开)**。`stop_loss_pct=0.05`。
- ✅ **regime_degrade 风险降档已上线灰度(6-30 09:30)**:命中坏环境只把**新开仓名义 7.5U→5U**(不停机/不禁开/不改信号/cross_venue/cost_floor/stop/TP/stale/cooldown)。**离散 OR 命中**(各记单一 `regime_degrade_reason`):`transition_band`(btc_trend∈[0.1,0.2))/ `extreme_trend`(btc_trend≥0.4)/ `broad_up`(market_mode)。**刻意不罩 0.2-0.4 与 mixed**。config:`regime_degrade_enabled=true`、`_notional_usdt=5`、`_btc_lo=0.1`、`_btc_hi=0.2`、`_btc_extreme=0.4`、`_modes="broad_up"`。回滚:`_btc_extreme:0`(关 extreme 段)或 `_enabled:false`(全关)+重启。详见 §11.5。
- **OpenMicro v2 record-only 埋点已部署(6/28 18:34 BJ)**:`open_micro_record_enabled=true`、`open_micro_record_venue=""`(所有活跃所)。**纯记录、不改任何交易行为**(详见 §7/§9)。
- ✅ **cross-venue lead-lag peer 采集器已上线(7-02,record-only)**:`internal/app/fast_peer_ws_collector.go`。**OKX + Binance + Bybit 三所公共 WS**(不接私有/不读账户/不下单/不改 scorer/exit/资金)。symbol=**三所合约清单交集**(种子=主流 + OKX shortlist,上限 `fast_ws_max_symbols`=15),写 `records/crossvenue/fast_peer_leadlag_events.jsonl`(venue=okx/binance/bybit,单文件跨所对齐)。config:`fast_peer_ws_enabled=true`、`fast_peer_venues="binance,bybit"`(OKX 恒为第三 venue)。**已知坑**:(1)Binance/Bybit 只有三所都上市的重叠币才有数据,故必须取交集(否则订阅 OKX 小币=收不到);(2)Binance 消息含 `"e"`(字符串事件类型)会被 Go json 大小写不敏感匹配污染 `"E"` float 字段→整条 unmarshal 失败,已加显式 `Ev string json:"e"` 吸收;(3)**本机到 Binance 期货 aggTrade 流 i/o timeout(仅 bookTicker 可用)**→ Binance 侧只有 book/mid_ret/book_imbalance,trade flow 由 OKX+Bybit 提供。复盘:`records/_fast_leadlag_review.py`(peer midret/tradeimb/bookimb/xspread → 预测 OKX 后续 3/5/10s 位移;门槛 n≥100 & grossPF>1.3 & net@5>1.05 & mean≥5bps & 不依赖单symbol/窗口)。**初步结论(3万事件):Binance midret→OKX 方向性强(grossPF 达 3.69)但幅度仅 0.6-0.9bps,net@5≈0 → 与 OKX 内部同病,暂未发现过成本 edge。** 数据持续累积。
- 🔴 **【定论】位移放大来源研究(7-02,只读 `records/_fast_leadlag_amplify.py`,46.6k 事件/15币)**:系统性搜 ≥5bps 过成本子集,6 维度(时间尺度/波动过滤/symbol分层/venue共振/spread过滤/方向类型)**全部不达标**。要点:(1)**OKX internal dynamic 与 cross-venue lead-lag 都有方向信息,但位移只有 0.5-2.1bps**;(2)**当前 OKX fast universe + 当前执行成本 5-10bps 下,无法支撑 fast live 盈利**;(3)时间尺度不释放位移(30-60s 反而衰减到~0);强 peer move 只提纯 grossPF、不放大 OKX 位移;venue 共振不放大;窄 spread 也救不了(net@5≈0);reversal 明确亏损(顺势才对)。(4)**唯一值得继续追的结构 = "peer 已动 / OKX 未动" 的 lag-follow**(mean 随 horizon 增长:5s+1.4→10s+1.6→**30s+2.1bps**,gPF~2),但 30s mean≈2.1bps **仍低于成本**、n 仅~240。→ 出路只剩 **换 universe(小币滞后空间更大:APT/NEAR/LTC 弹性最高)/ 换成本结构(往返压到~1-2bps)/ 换交易所 / 换执行方式**,而非再调 scorer。**fast live 继续保持 record-only,不恢复真实新开仓。**
- 🔬 **三线只读研究(7-02):**
  - **线一 Inefficient Universe(`_fast_universe_leadfollow.py`)**:peer universe 扩到 50(OKX∩(Binance∪Bybit),含非主流/长尾/股票 perp;`fast_ws_max_symbols=50`)。lag-follow(peer已动&OKX未动→OKX后续)证实**越低效率币滞后越大**:AAPL 30s +8.5bp(net@5 2.10)、AGLD +5.9bp(net@5 1.33)、ALAB +5.2、ACU +4.2;主流币(BCH/ADA/NEAR n大)mean<2.5bp。**但高mean币 n 全<100 → 候选池暂空,需继续采样。**
  - **线二 Cost Structure(`_fast_cost_structure.py`,基于5480笔真实fast结算)**:当前实测往返成本 **≈6.2bp**(maker入场+taker出场,实际fee 6.23bp);maker+maker 理论下限 **≈2.1bp**(=lag-follow位移,无余量);需 maker fee≈0/返佣才可能过成本。
  - **线三 Venue Pairs(`_fast_venue_pairs.py`,6个lead→lag全测)**:**OKX 是最慢=最佳执行端**(binance→okx +1.77bp@30s)、**Binance 最佳lead源**;但**无任何 pair≥5bp**(net@cost全<0.35)→换执行所解决不了位移不足。
  - **综合**:执行端/lead源已选对,成本地板≈2.1bp,**唯一可能放大位移的杠杆是 universe(低效率长尾币)**——待攒 n≥100 验证。
- 💰 **手续费折扣**:停在**只读审计**(`_fee_discount_manager_audit.py` 已提交;`fee_discount_audit.jsonl` 未提交)。OKX maker2/taker5、Binance BNB-10%机制明确、Bybit无平台币抵扣、Gate GT可抵扣;**账户级 VIP/余额/抵扣状态=NEED_API**(Phase2 只读账户费率查询待补)。**结论:fee是第二线,救不了入场edge。**
- `max_concurrent=30`(上限)、`per_position_notional_usdt=7.5`(**固定名义模型**:每仓7.5U名义/1.5U保证金,实际持仓数=资金/1.5U,上限30 → 15U/所≈10币、三所≈30币)。`leverage=5`。
- 入场(为"广撒网"已放宽,**但见 §6 待决策**):`min_open_conviction=0.55`、`zscore_entry=1.0`、`cost_floor_bps=40`、`open_taker_fallback_bps=30`(maker挂不上以≤30bps IOC吃单兜底)、`universe_top_n=20`。
- 出场:`max_hold=60`、`stale_cut=30`、`abs_max_hold=60`、quick_tp=0.12、partial_tp=0.06、trail_gap=0.06。
- 循环:完整 90s + **快速风控 20s**(仅盘口、触5%止损即吃单快平)。
- 跨所印证:**仅当≥2所反向且0同向才否决**(已从"0同向即否决"软化);否则按同向/反向数加减信心。
- global_halt:方向引擎**只认 `.halt-live` 人工急停**(`IsEmergencyHaltActive`),**不被账户回撤型持久 global_halt 锁死**(2026-06-28 14:42 修复)。
- 趋势态:**已禁用**(`TradeTrendRegime=false`,回测最大失血源),线上确认 0 trend 开仓。
- 仓位级统计:已实现(`PositionID` 合并分批止盈),面板显示仓位级胜率/PF/最大亏。

## 4. 关键已验证结论(别重复踩坑)
1. **当前 5m 价格信号(EMA/z-score 反转)扣费后无净 edge**:离线全配置(信心0.55→0.99 × 出场风格 × regime × 5/8/12bps成本)均净负;实盘**仓位级 PF≈0.77**(81仓)。
2. **砍得越快越亏**:stale5 净-535%/胜26% vs stale40 净-69%/胜49%;反转/棘轮 churn 在高信心档也伤 edge。→ 不要再缩短 stale。
3. **资费 carry-fade 不稳健**:5m/25h 窗口中度资费桶净+1.17%,但 1H/12天窗口符号翻转 → 过拟合,非真 edge。
4. **"高资费+高ATR 肥币池"有害**(跨窗口+实盘一致);极端 funding 是最差桶。
5. **覆盖受"信号供给"限制**:shortlist 中 85-89% 是"无信号";给再多资金也撒不满30币。"广撒网"瓶颈在信号数量,不在资金/并发。
7. **硬止损是当前亏损主因(post_50U_window 复算,n=264,只读)**:**硬止损仓 净 −10.34U(n107),非硬止损仓 净 +5.92U(n157)**——即"非硬止损整体为正,亏在硬止损"。最强可归因维度=**BTC regime**:btc_trend **0.1-0.2 过渡带 PF 0.30/净−3.08U(最大亏桶,n86)**、`broad_up` PF 0.22/硬止损59%(n17);**非单调**(强趋势 ≥0.2 反而 PF 0.77)。runup/same_side 同向但弱(见 §11)。**taker 非主罪**(taker PF0.58≈maker0.64)。
6. 离线研究工具(已加,默认skip,`EDGE_BT=1` 跑、需外网):`internal/app/directional_exitsim_test.go`(`TestDirectionalScalpExitSweep`/`TestDirectionalScalpEntryQuality`)、`directional_funding_edge_test.go`(`TestDirectionalFundingCarryEdge`)。

## 5. 待修真 bug(监控盯盘发现,优先级高)
- ✅ **`cooldownUntil` 不落盘 —— 已修(2026-06-28)**:已持久化 `cooldownUntil + coinLossCount + dayStartCST` 到 `records/{venue}_directional/cooldowns.json`,启动 `restoreCooldowns()` 恢复。跨日只恢复未到期冷却、丢弃旧亏损计数;同日恢复并钉死 `dayStartCST` 防 `refreshCSTDay` 误清。落盘点:`closePosition`、`refreshCSTDay`(跨日清零后)。代码 `okx_directional_engine.go`:`persistCooldowns/loadCooldowns/restoreCooldowns`。**自此"硬止损→当日 ban""单币亏2次→晾一天"重启后仍生效。**
- 🔴 **账户风控全天空转(仍未修,6/28 19:50 复查仍在刷:近300行50条)**:每分钟报"真实现金单 tick 骤降 > 阈值"→ 权益不可靠 → 跳过、回撤/日损保护未运行。根因疑**重启后权益快照基线重置** + `cum_fund_flow≈-64U` 来源不明。**修:权益基线跨重启持久化;排查 -64U 出流。**(注:不影响方向探索档开仓,因方向只认 .halt-live;但账户级日损保护空转。)
- 注:盯盘3 曾报"global_halt 回归锁死"是**误报**(分析跨越了14:42修复点),实际已解除。

## 6. ⚠️ 待用户决策(我已提出,未拍板)
**GPT 第7条主张"超频≠降门槛,应保持信号质量、提高检查频率"**,与我们一小时前为"广撒网"做的放宽(conviction 0.55、zscore 1.0、taker fallback)**直接冲突**。
→ **决策点:是否把入场门槛收回质量优先(0.55→0.7、1.0→1.2、关/留 taker fallback),改用"多币×高频检查"获取广度?** 我倾向收回(放宽大概率在加垃圾单)。**接手 agent 请先与用户确认此项再动入场参数。**

## 7. 下一步路线(GPT 超频 scalper 蓝图 + 我的"先测后建"纪律)
**硬事实:超频是放大器不是印钞机。核心 EV 现为负,先做"能创造 edge"的模块,别先堆速度。**
- ✅ **阶段A —— 已落地(2026-06-28)**:**成交后仪表化已上**。`settlements.jsonl` 每条新增 `hold_seconds / entry_slip_bps / exit_slip_bps / open_via_taker / close_via_taker / fee_usdt(交易所实扣) / mfe_account_pct / mae_account_pct`(本金口径运行态 MFE/MAE,在 90s+20s 两循环采样)。**仅记录、不改交易行为。** 注:MFE/MAE 当前是"自开仓起的运行态高/低水位"(非严格 10/30/60s 切片;要精确秒级切片需新增快采样器,见阶段B)。**接手第一件事:攒够样本后做离线归因——按 `open_via_taker` 分桶比 PF(taker fallback 是否在亏)、按 `mae_account_pct`/`hold_seconds` 验证秒级退出能否把 PF 拉过 1。这就是 §6 决策(收不收回放宽门槛)的数据依据。**
- **阶段B**:快速管理循环(止盈/移动止损/停滞/反转进 5s 循环;硬止损 3-5s);成交后无正反馈秒平(阈值用阶段A数据定)。
- **阶段C**:5m 只做候选,1m/30s bookTicker 做临门入场确认(spread稳定/盘口不抽流动性/方向已启动)——治 maker 逆选择。
- **阶段D**:自动降档(symbol/venue/global 的 PF+连亏驱动,只调小/调严/冷却,**不停机**)。
- **阶段E**:universe 重写为"可交易性优先"(spread稳定/盘口深度/滑点/tick适配),资费仅辅助;负分项:极端funding/高ATR/近期硬止损币/停滞亏损币/价差抖动/薄盘口降权。
- 判据始终是**仓位级 PF**:持续>1 才放大仓位/资金,否则维持微仓采样。

## 8. 进行中
- 盯盘 composer:执行健康/风控质量/广撒网覆盖 三份已完成(结论已并入本文档 §4/§5);**盈亏PF归因盯盘(agent a04b0b8d)可能仍在跑**,其结论(按币/资费/出场原因的盈亏归因、最赚最亏子集)接手后请查看并入。

---

## 9. 阶段(phase)与资金档(capital_regime)永久定义 —— 统计绝不能混
分析脚本 `records/_directional_feedback_audit.py`(只读)已固化以下口径:
- **A 旧版大仓**:无 micro(大名义)。历史失血主体(n=213,PF 0.65,净 −28.5U)。
- **B micro_7.5U**:固定名义,无 open_micro 字段。
- **C OpenMicro_v1**:有 `open_micro`、**无** `open_micro.env`。
- **D OpenMicro_v2**:有 `open_micro.env`(2026-06-28 18:34 起)。
- **E taker_fallback 灰度阶段**:`taker_fallback_venues="binance"` 部署起(**6-28 23:29:31 BJ = 6-28 15:29:31 UTC**;实证:该时刻后 OKX/Bybit 再无 taker 开仓、仅 Binance 留 taker;E ≈208 仓)。OKX/Bybit maker-only、Binance 留 taker。**E 是 phase,不是时间窗**。⚠️ 注意是 **6-28** 不是 6-29(曾 typo 成 6-29 → 误剔整整一天 E 数据,已修)。
- **capital_regime(正交)**:大仓 / 15U / **50U(OKX/Bybit ≥6-28 19:25 BJ)**。**50U≠D≠E**,必须单独交叉标记;Binance 全程 15U。
- **analysis_window(第三维,与 phase 区分)**:报告里的时间切片窗口。已命名:**`post_50U_window`=平仓 ≥6-28 19:25BJ(50U切换)的当前资金期**(≈264 仓,跨~30h 多时段;**不是 E,别混**)。
- **铁律**:任何 PF/胜率/亏损结论必须**同时标 phase + capital_regime + analysis_window**,不看全历史混算。**全历史 night PF 差 = 旧版大仓(A)污染,当前版本无 night 样本,严禁据此降档。**

## 10. 进入下一步的样本门槛(硬性,不达不动实盘)
- decision **成交侧 env ≥10**:只能做极早期 opened-vs-rejected 倾向对比;
- decision **成交侧 env ≥30**:才可讨论"过滤候选";
- **settlement env ≥30 且盈/亏样本都有**:才可讨论进入阶段2B(实盘规则);
- **50U 阶段平仓 ≥30**:才可判断 50U 效果(与 15U 分开统计);
- 当前(6-28 19:50)实况:decision env 总~419、成交侧 env~13~26、**settlement env 仅 7、D_v2 仅 5、50U 平仓 ≈3** → **全部不达门槛,处于"继续采样",不得进 2B、不得改任何实盘规则**。

## 11. 阶段2B 候选规则清单(草案;均未实现、未上线)
> 每条:依据 / 字段 / 当前样本是否够 / 进实盘最低门槛 / 可能误杀 / 灰度 / 回滚。**当前全部"继续采样/record-only",无一 active 上线。**
> **优先级(6-30 09:30,基于"硬止损=亏损主因"§4.7)**:🥇 **§11.5 BTC/market regime 降档 = ✅ 已上线灰度(downsize 7.5→5U,离散OR)** > §11.3 runup(继续观察,效应弱) > same_side(n<30 不足)。**taker(§11.1)、deep_book(§11.4)已降为观察项。**
1. **taker fallback 限制(分 venue/条件化;门槛已达,2B 方案已写未实现)**——
   - **数据(6-28 22:55,C/D 排除A,maker31/taker30 各≥30)**:C/D maker PF0.95 vs **taker0.49**;D_v2 maker0.99/taker0.36;**okx t0.29 · bybit t0.10 · binance t1.53(例外)**;50U maker1.19/**taker0.09**;taker entry_slip +4~6bps(maker −1)、快进快出3%/停滞27%(maker10%/16%)、spread5.1/runup2.5(maker3.9/1.2)。
   - **2B 方案**:A(首选)**okx/bybit 关 taker、binance 保留**;B 按 `spread≤S & runup≤R` 条件化。
   - **config(不写死所)**:新增 `taker_fallback_venues`(csv 允许名单,**空=现状全允许=默认不变**;设 "binance"=仅 binance 用 taker、okx/bybit maker-only)。可选 `taker_fallback_max_spread_bps/_max_runup_bps`(默认0=关)。
   - **实施点(唯一)**:`placeDirectionalLeg` 开仓腿 taker 兜底闸前加 `openTakerFallbackBpsEffective()`(本所不在名单→返0=maker-only);**只动开仓腿,平仓 IOC/止损/止盈不碰**。
   - **灰度**:`"binance"` 先禁 okx/bybit,观察 30-50 仓比 PF/成交率/错过率/硬止损。**回滚**:置 ""+重启,不影响在场仓。
   - **风险**:成交↓/样本变慢/错过个别好 taker 单;且 maker 自身也才 PF0.95 → 禁 taker 只少漏血、不造 edge(根因仍是 5m 无 edge)。
   - **已实施并灰度部署(E阶段起点 = 6-28 23:29:31 BJ = 6-28 15:29:31 UTC)**:`taker_fallback_venues="binance"`,OKX/Bybit 开仓 maker-only、Binance 留 taker;`openTakerFallbackBpsEffective()` 已上;只动开仓腿,平仓不受影响。启动日志确认 okx/bybit 本所生效=0bps、binance=30bps。
   - **E阶段验证(部署后)**:OKX/Bybit taker=0 ✓、Binance taker 仍可用 ✓;okx/bybit `maker_not_filled` 累计~40(少赚代价);E 阶段平仓 PF~0.5 仍亏(样本<30,未定论)。
   - **mnf 弱反事实(6-29 01:53,OKX 公共1m,bybit用OKX同币近似)**:**OKX 错过的 maker_not_filled 候选若 taker 成交大概率是亏损单**(+30m 盈利占比 0%、均值 −0.59%)→ **"少赚"实为"少亏",支持维持 OKX maker-only**;**Bybit 数据不足(n=1,多为 OKX 未上市币)→ 无恢复 taker 的证据,继续观察**。
   - **当前决定:维持灰度不缩**(不恢复 OKX/Bybit taker);待 E≥30 出 E vs D 正式对比;Bybit 反事实数据补足后再评估。
   - ⬇️ **降级为观察项(6-30 01:52)**:post_50U_window(n=264)复算 **taker PF0.58 ≈ maker0.64,taker 非当前亏损主罪**(主罪是硬止损,见 §4.7 / §11.5)。**不再列为第一候选,维持现状灰度不缩不放,仅继续观察。**
2. **MAE 灾难刹车**——依据:MAE≤−2% 救援率低(早期 80% 仍亏,n小)。字段:`mae_account_pct` 时间切片。门槛:settlement env≥30 且盈亏齐。误杀:深 V 反弹仓。灰度:仅极保守阈值(−2~−3% 账户)、单所。回滚:关闭开关。
3. **runup 防追末端 —— 继续观察(post_50U_window 复算效应偏弱)**——依据:`pre_open_runup_bps`。**post_50U_window(n=264)复算:abs(runup)<2 PF0.76/硬止损35%、≥10 PF0.60/硬止损48%(n33)——方向对(runup高→硬止损高)但弱于早期判断,且最大亏在 2-10 中间桶(PF0.61/净−2.38U/n135),单调性不强。** 字段:`pre_open_runup_bps`。门槛:各桶 env≥30 + 跨窗。误杀:强趋势启动初段;runup 在强 BTC 趋势里可能是正向(需与 §11.5 联动)。灰度:高 runup 候选 maker-only/提 conv(record→active)。回滚:开关。
4. **spread/depth 门槛**——依据:高 spread/低 depth_cover 桶 PF 差(当前被 cross_venue 选择性偏差污染,不可用)。字段:`open_spread_bps`/`depth_cover_ratio_10bps`。门槛:outcome env 足够且去偏。灰度:开仓预检阈值。回滚:开关。
   - 🔴 **deep_book 专项验证结论(6-30 01:31):不进入 2B,降级为"继续 record-only 观察 / 研究线索"。**
   - **背景**:影子快信号首份前向收益表(6-30 01:22)曾显示 `deep_book`(depth_cover≥100x)+5m 扣成本后为正(+0.11%),一度被列为"2B 第一优先候选"。**专项 8 维验证后已收回该定性**(脚本 `records/_deep_book_probe.py`,只读/反事实,`SHADOW_FWD=1` 跑;数据=单一 58 分钟窗口 06-29 16:29–17:27 UTC,n=221、deep_book 121、真实成交侧仅 opened 4/taker 0)。
   - **不进 2B 的原因(6 条)**:
     1. **depth 分桶不单调**:<20x +5m **+0.29%(83%)** / 20-100x **−0.30%(40%)** / ≥100x +0.24%(63%)——U 型,中桶最差;
     2. **浅盘口 <20x 在 +5m 反而最好**(+0.29% > ≥100x 的 +0.24%)→ +5m 的"正"不是深度梯度驱动;
     3. **实盘成交仓里 deep_book PF 0.53 < 非 deep 0.71**(带 depth 字段实盘仓 n=269;deep≥100x 151 仓净 −3.35U,非 deep 118 仓净 −1.44U);
     4. **deep_book 实盘硬止损率更高**(42% vs 37%);
     5. **反事实(deep 更好)与实盘 outcome(deep 更差)直接相反**;
     6. **当前反事实极可能是假阳性**(单一短窗口市场漂移 + 真实成交侧 shadow≈0,无法证伪),**不能上线**。
   - **后续追踪(仅观察项,不作上线候选)**:继续在报告里追 deep_book 的①反事实表现 ②实盘成交表现 ③分 venue ④分 market regime ⑤分不同时间窗口;**重点解释"为什么反事实和实盘相反"**(强假设:反事实把 rejected 候选当成已成交,未计真实下单冲击/滑点/逆选择,导致虚高)。
   - **当前不改任何实盘规则**:不加 depth 过滤、不改 universe、不改 taker、不上 scratch、不改参数。
   - **下一步重点 ≠ deep_book 上线**,而是继续寻找"真正能在实盘 outcome 中改善 PF"的字段或组合。
5. **BTC/market regime 降档 —— ✅ 已上线灰度(6-30 09:30,downsize 7.5→5U)**——
   - **依据(post_50U_window/历史 env,只读派生回测,n=298)**:按命中原因单笔毒性(名义归一收益):`transition_band`(btc 0.1-0.2)n87 PF0.31/硬止损48%/−0.472%;`extreme_trend`(btc≥0.4)n17 PF0.18/硬止损53%/−0.597%(⚠<30);`broad_up` n9 PF0.27/硬止损56%/−0.599%(⚠极少)。**区外 n185 PF0.84/−0.086%(干净)**。**btc_trend 是 W 形:0-0.1 好、0.1-0.2 差、0.2-0.4 好、≥0.4 差** → 故离散 OR 只罩 0.1-0.2 与 ≥0.4,**不罩 0.2-0.4(健康趋势)与 mixed(单笔毒性低,罩了≈全局缩仓)**。
   - **已上线规则**:命中坏环境只把**新开仓名义 7.5U→5U**;**只影响新开仓**,在场仓/平仓/信号/cross_venue/cost_floor/stop/TP/stale/cooldown/universe/taker **全不动**。命中=离散 OR,各记 `regime_degrade_reason`(transition_band/extreme_trend/broad_up)。**固定优先级:transition_band > extreme_trend > broad_up**(transition 与 extreme 是互斥 btc 区间;唯一重叠 = btc≥0.4 且 broad_up → 记 `extreme_trend`)。按 reason 对照务必用此口径。
   - **字段/记录**:`regime_degrade_hit` + `regime_degrade_reason` + `regime_degrade_notional_usdt` 已写入 `open_decision_events` / `position.json` / `settlements.jsonl`。
   - **后续验证门槛(实盘 recorded reason,不要 hit-vs-non-hit 一锅端)**:**每个 reason 单独**统计 n / PF / hard_stop / 名义归一收益;**特别盯 extreme_trend(n=17 仍灰度,未最终验证)实盘是否继续毒**。脚本 `records/_regime_degrade_backtest.py`【第4节】已按 reason 分桶,随样本累积。
   - **回滚**:`regime_degrade_btc_extreme:0`(关 extreme 段)或 `regime_degrade_enabled:false`(全关)+重启;在场仓不受影响。
   - **附:same_side 拥挤**:≥3 PF0.33/硬止损44%(**n=18<30 不足**);mixed(PF0.57,亏损额大但单笔毒性低)——**均不纳入降档**(继续采样)。
6. **time×venue 降档**——依据:**当前版本当前资金档**某时段某所 PF 差(**禁用旧版 A 数据**)。门槛:该(phase,cap,session,venue)桶≥30。动作=降档。回滚:开关。
7. **symbol/venue 自动降档**——依据:滚动仓位级 PF/连亏/硬止损率。**不停机,只调小/调严/延 cooldown。** 字段:实时仓位级统计反哺开仓(目前 stats 仅展示,未反哺)。门槛:滚动窗≥20-30。回滚:档位归位。
8. **不可交易币动态预过滤(低风险工程优化;非 edge 过滤)**——
   - **性质**:这是**工程预过滤,不是 edge 过滤**;目标=减少无效评估 + 不再让"永远开不出"的币占 shortlist 坑,**不改变任何可交易标的的交易规则/参数/edge**。
   - **依据(漏斗归因 D)**:`量化后不可交易` 共 203 次,高度集中:**LAB-USDT 160、KORU-USDT 29、SKHYNIX 10、VELVET 4** —— 这些币在 7.5U 名义下量化后不可交易,却反复入选 shortlist、每轮空跑评估。
   - **设计(动态,禁止写死黑名单)**:基于 `per_position_notional_usdt` × 当前 mid × instrument 的 `step/minQty/minNotional/tickSize` 计算"量化后是否可交易"(复用 `normalizeVenueQty`/`openLegTradableAfterQuantize`);不可交易则在 `scanOKXUniverse`(选币)或 `openFromShortlist`(候选环节)**剔除/降权**。币随名义/价格变化可恢复,故必须动态判定、**不得硬编码 LAB/KORU**。
   - **字段**:instrument 元数据(`lookupVenueInstrumentForMarket` 已有)+ `per_position_notional_usdt`。
   - **样本门槛**:无需 outcome 样本(纯执行可行性,确定性逻辑);但仍**先离线/只读核验命中集**(确认只剔真不可交易、不误剔可交易币)再上线。
   - **灰度/回滚**:`tradability_prefilter_enabled` 默认 false;开关关=行为不变。回滚=置 false。
   - **当前**:仅列候选,**不实现、不改 universe**(按纪律)。

> §11 其余项进度(6-28 21:01):A 信号供给不足=主因,解法在未来 fast layer(只观察);B cross_venue/cost_floor 暂不动(需 outcome 数据);C universe 可交易性优先=继续观察;E 资金非瓶颈,不再加钱。

## 12. 阶段2B 实现骨架(规划;不实现、不部署)
统一模式(所有规则共用,确保 record-only→灰度→回滚安全):
- **加规则位置**:入场过滤→`openFromShortlist`(在 micro 已建好之后、`openPosition` 之前);出场/刹车→`decideDirectionalPositionAction`(纯函数,可单测)或 `manageRiskFast`(20s,刹车类);降档→新 `degradeTier` 状态由 `runOnce` 每轮按滚动 PF 重算,`openFromShortlist`/`openPosition` 读它调 minConv/并发/taker/名义。
- **config 字段**:每规则一个 `*_enabled bool`(`core/config.go`+`defaults.go`+`config.yaml`),**默认 false**;+ 阈值字段;+ `*_venue` 灰度串(空=所有所,复用 OpenMicro 灰度模式)。
- **不影响现有交易**:开关 false 时代码不进任何分支(参照 `OpenMicroRecordEnabled` 写法);Fetch 失败留空不阻断。
- **测试**:纯函数规则加单测(参照 `decideDirectionalPositionAction` 测试);`go build ./... && go vet && go test -run 'Directional|OKX|Universe|Stats'` 全绿;先单所灰度→观察仓位级 PF→再全开。
- **回滚**:开关置 false + 重启(秒级);或 `git revert`。
- 优先序:taker 限制 > MAE 刹车 > runup 防追 > spread/depth 过滤 > 环境降档 > 自动降档。

## 13. 下一任 agent 第一件事(照做)
1. 跑 `python3 records/_directional_feedback_audit.py`(只读;读全部 directional 结算 + open_decision_events)。
2. 看 §1 样本量、§7 时间维度、第10节阶段切分、第8/9 节 env 分桶——**确认 phase + capital_regime 都分开了**。
3. 查 **D_v2 settlement env 是否≥30、50U 平仓是否≥30**(§10 门槛)。**不够就继续采样,什么实盘规则都不要改。**
4. 已就绪但**仅记录**的功能(都不改交易):time-slice MFE(mfe@30/60/180s)、OpenMicro v1(spread/depth/runup/maker轮数)、OpenMicro v2 env(BTC/funding/波动/拥挤/广度)、`open_decision_events.jsonl`(含 env,治幸存者偏差)、`shadow_fast_signal_events.jsonl`(影子快信号,record-only)、cooldown 持久化、仓位级 stats。
   - 只读研究脚本(需 `SHADOW_FWD=1` + 外网):`records/_deep_book_probe.py`(deep_book 8 维专项:venue/phase/成交侧/horizon/depth单调性/成本敏感/实盘对齐;结论见 §11.4)。
5. **当前禁止**:上 scratch、关 taker、改 universe、**加 depth/deep_book 过滤(专项验证已否决,见 §11.4)**、开 avoid_btc_chase、改参数、动资金、据 night 全历史降档、混 A/B/C/D、据 <30 settlement env 给规则、据单一行情窗口的反事实下结论、git commit(除非用户明确要求)。
6. 账户风控空转(§5 🔴)仍未修,优先级高,但属账户级、不影响方向开仓。
