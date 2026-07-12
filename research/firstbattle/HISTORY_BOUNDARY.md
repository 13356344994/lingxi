# 首战历史资产与考试边界

## 当前公开结论

- 多年/多月历史行情立即用于研究，不等待未来数据。
- 已完成全量目录：7 个数据族、2,218 个 `.done` 文件、8,891,185 行。
- 目录 `DataHash`：`f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c`。
- 现有旧研究脚本引用过的数据族默认归 `RESEARCH_ONLY`。
- 若某个子区间在研究前已有独立 `DataHash/ChallengeHash` 封存承诺，可由考官证明后作为隐藏卷；公开污染扫描器不读取也不否定该隐藏范围。
- 无研究前封存承诺的数据默认 `RESEARCH_ONLY`。
- 公开层只保存 `DataHash/ChallengeHash/ProtocolHash`；隐藏时间范围、路径和逐笔数据不得出现在本文档。

公开污染账本：

```text
research/firstbattle/history_contamination.json
```

逐叶子历史数据需求：

```text
research/firstbattle/history_data_requirements.json
```

逐叶子可检验性裁决：

```text
research/firstbattle/history_testability.json
```

历史行情分层契约：

```text
research/firstbattle/history_regime_contract.json
```

## 当前可检验性

- `global_context.router_node`：`TESTABLE`，使用历史 OKX 1H。
- 四个 `panic_liquidation relay/exhaustion` 叶子：`UNTESTABLE/NO_GO`。

强平叶子缺少可证明的历史 liquidation/OI/depth/xvenue/PIT 联合数据。按用户硬约束：

> 不等待未来数据，不用“再收几天看看”续命；当前 generation 关闭这些叶子。

这只关闭强平叶子，不阻塞 OHLCV/funding 支持的其它机制树。

## 行情分层纪律

- 只使用决策时刻之前已经完整收盘的 bar。
- BTC 趋势效率、实现波动、回撤与 funding 状态只在 research partition 上按特征分布拟合分桶。
- 分桶不读取未来收益。
- 切点必须在生成 challenge commitment 前冻结。
- 考试结果不得反向修改行情定义。
- 数据缺失进入 `OOD_NO_TRADE`。

## 考试边界

1. research/WF 区可见。
2. 历史挑战卷与黄金卷只向研究者暴露 commitment。
3. 同一时间/标的/机制簇不得跨区。
4. 同一 `DataHash/ChallengeHash` 不得跨 generation 复用。
5. 解封、崩溃或返回结果都永久消费该卷。
6. 研究者只获得签名 pass/fail 与 coarse reason。
7. 真实 grader 必须从 hidden paper 计算结果；禁止命令行人工传入 pass/fail。

实现状态：

- hidden paper 支持 0600 file-backed 流式读取与同流 DataHash 校验；
- generation manifest hash 绑定排序后的 exact tickets；
- CLI 不能传入阈值或 pass/fail，只能使用代码评审后注册的 protocol evaluator；
- 当前仅注册 synthetic boundary-test evaluator，尚未注册任何生产赚钱叶子的 evaluator；
- 本轮没有创建、解封或消费任何真实历史考卷，也没有读取用户此前预留卷的隐藏范围。

## 禁止事项

- 不读取现有隐藏考试文件或时间范围。
- 不把旧研究看过的 holdout 重新命名为新考卷。
- 不把收益用于定义行情分类。
- 不因历史字段不足而等待未来数据。
- 不把某个叶子缺数据扩大为整棵路由树停工。
