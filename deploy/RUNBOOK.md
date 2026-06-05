# lingxi 事故运行手册（Runbook）与演练清单（R28 / S8-D）

> 目标：真出事时**操作者有章可循**，把处置延迟与误操作导致的损失降到最低。本手册覆盖
> 断连 / 单腿裸露 / 熔断触发 / 交易所故障 / 密钥泄漏五类场景的人工 SOP，并给出上线前/定期演练清单。
> 配套机制（代码侧）：紧急停机文件、交易所 dead-man（`countdownCancelAll`）、单写租约（flock）、外部看门狗、
> `reconcile` 对账、pending funding 升级隔离、运行期改参 diff/回滚、告警分发。

---

## 0. 速查（出事先做这三步）

1. **判级**：是「策略不赚钱」（慢性，走灰度回退/降档）还是「资金/裸腿/异常下单」（急性，立即止血）？
2. **急性止血**：创建紧急停机文件 → 引擎在下一个安全检查点 fail-safe 停机（只停不下单）：
   ```sh
   touch <RecordDir 同级或配置的 emergency_stop 文件路径>   # 见 cfg.RiskConfig/EmergencyStopFile
   ```
   若进程已失控/不响应：`systemctl stop lingxi`（触发 SIGTERM → 优雅停机收尾：停用 dead-man、落盘、平仓善后）。
   若怀疑挂单裸奔且进程已死：**等待交易所 dead-man 倒计时到期自动撤单**（启用时 30s 内），或登录交易所手动撤单/平仓。
3. **通报 + 取证**：截图账户持仓/挂单，备份 `records/`（尤其 `settlements_journal.jsonl` /
   `pending_funding_journal.jsonl` / `domain_events.jsonl` / `*_server.log`），再开始处置。

---

## 1. 行情/账户断连（WebSocket 掉线、REST 超时）

**识别**：日志出现断连/重连；`/healthz` 转 503；看门狗告警；面板行情停更。

**系统已有兜底**：
- 断连盲窗守卫：行情流不可信时**暂停本轮开仓**（不会在盲区里裸开）。
- 已有持仓继续按本地状态与定时器走平仓/结算路径；恢复行情后自动继续。

**人工 SOP**：
1. 确认是本地网络还是交易所侧（`curl -m5 https://fapi.binance.com/fapi/v1/ping`）。
2. 本地网络：修复网络即可，引擎自愈；持续超过数分钟仍不恢复 → 进入「交易所故障」流程评估是否撤离。
3. 确认没有「以为开了实际没开 / 以为平了实际没平」：跑 `lingxi reconcile` + 交易所对账（见 §6）。

---

## 2. 单腿裸露（spot/futures 只成交一条腿）

**识别**：恢复协议日志报单腿；`residual_position`/`residual_spot` 告警；对账出现异常 delta。

**系统已有兜底**：开仓走双腿原子化 + 失败恢复协议；`MinLiquidationDistanceRate` 等爆仓距离守卫；
残腿会触发恢复动作（在允许的档位下自动补腿/撤离）。

**人工 SOP**（自动恢复未收敛时）：
1. **立即判断净敞口方向与大小**：交易所看 spot 持仓 vs futures 仓位，算出未对冲的净 delta。
2. **手动对冲到中性**：用市价单把裸露腿补齐或把多出的腿平掉，**先到中性、再谈盈亏**。
3. 创建紧急停机文件，阻止系统继续开新仓，待人工核实根因（执行超时？部分成交？精度？）。
4. 记录事故到事故日志，更新 `MaxExecutionIncidents` 等阈值是否需收紧。

---

## 3. 熔断/风控触发（日亏/单周期亏损/连亏/账户回撤/平台异常）

**识别**：日志 `[CRITICAL]` + 停机；`StageStopped`；告警「触发日级风控限制」；面板档位转 `observe`。

**系统已有兜底**：触发即 fail-safe 停机（只停不下单），并持久化当日限制状态（重启不会绕过）。
edge 衰减会自动降档到 `observe`（停开新仓、可自动恢复）；灰度门会建议回退档位。

**人工 SOP**：
1. **先理解为什么触发**：看 `currentDailyLimitReasonLocked` 给出的具体原因（日亏/单周期/连亏/回撤/下单数/名义）。
2. **核实是否真实亏损**：跑 `lingxi reconcile`——是策略真的偏离模型（→ 缩档/停策略排查），还是
   funding 未到账导致的「假亏」（→ 见 §7 pending funding）。
3. 确认无残腿/无裸奔挂单后，决定：当日不再交易（等次日自动重置）/ 收紧阈值 / 回退灰度档位。
4. **切勿盲目清除熔断重启**就继续放大——熔断是保护，不是噪声。

---

## 4. 交易所故障 / 维护 / 异常拒单

**识别**：大面积下单被拒、`-1xxx` 错误风暴、`consecutivePlatformRejects` 飙升、平台熔断 `FuseTripped`。

**系统已有兜底**：平台异常熔断 + 冷却；健康分下降自动降档；连续请求错误计入健康分。

**人工 SOP**：
1. 查交易所公告/状态页确认是否维护。
2. **维护窗口**：创建紧急停机文件，等交易所恢复后再清除并重启；其间已有持仓由 dead-man 兜底（挂单到期撤销）。
3. **持仓在维护期到达 funding/平仓点**：维护结束后人工核对是否错过结算，必要时手动平仓并用 `reconcile` 校正账本。
4. 多 venue 热备（R36）属未来项，当前单 venue 故障即整体不可交易，应优先保证「停得安全」。

---

## 5. 密钥泄漏 / 凭证异常

**识别**：账户出现非预期下单/划转；交易所安全告警；怀疑 `secret.yaml` 外泄。

**系统已有兜底**：启动期 API Key 权限自检（带提现权限的 Key 被拒启动）；控制令牌写 0600 文件、日志只留指纹；
secret 路径日志脱敏。**但密钥静态加密 + IP 白名单需在交易所侧/基建侧配置（代码无法替代）。**

**人工 SOP（按分钟级执行）**：
1. **交易所后台立即删除/禁用该 API Key**（这是最快的止血，优于停进程）。
2. `systemctl stop lingxi`，停所有节点（单写租约可帮助确认没有多实例）。
3. 检查账户是否有非授权划转/提现；若有，走交易所安全/客服冻结流程。
4. 轮换密钥：生成新 Key（**禁用提现 + 绑定服务器出口 IP 白名单 + 只给现货/合约交易必需权限**），更新 `secret.yaml`（0600），重启。
5. 复盘泄漏路径（日志明文？仓库误提交？主机入侵？），修补后再恢复交易。

---

## 6. 与交易所对账（每次事故后必做）

```sh
lingxi reconcile     # 实盘 ↔ 模型预期 对账；只读，可在运行期随时跑
```
- 看 **HEALTHY/UNHEALTHY** 结论、净利偏差、资金费预测误差、执行摩擦累计。
- 未配对周期数（仅实盘/仅预期）应为 0；非 0 说明记录缺失/重复，重点排查。
- 事实源可回放：`settlements_journal.jsonl` / `pending_funding_journal.jsonl` 是 append-only 审计源，
  与交易所流水逐笔核对，确认「钱按模型赚到 / 没有遗漏的腿或结算」。

---

## 7. Pending funding 长期未到账

**识别**：告警 `pending_funding_stuck`；某 cycle 净利迟迟不定稿。

**系统已有兜底**：重试达 `MaxFundingSettlementAttempts` 次或挂起超 `MaxFundingSettlementAge` →
升级 critical 告警 + 停止自动重试 + **把该 cycle 从连亏/回撤口径隔离**（回退平仓时已记入的日内净利，避免误熔断）。

**人工 SOP**：核对交易所资金费流水是否真的产生 → 已产生但未被抓到则手工补记/校正；确属未产生（如提前平仓错过结算）则
按实际结果定稿该 cycle，清理对应 pending 记录。

---

## 8. 运行期改参（收紧风控 / 调灰度档）

- 预览：`POST /api/runtime/config/preview`（dry-run，返回字段级 before/after diff，不生效）。
- 应用：经鉴权的改参接口（带 audit）；**只允许收紧**（tighten-only）。
- 回滚：`POST /api/runtime/config/rollback`（一键回到上一版）。
- 放大档位（灰度 promote）：引擎只给**建议**（灰度门告警），实际调高 `MaxPortfolioNotionalUSDT` 由人工核实
  （净 edge/滑点/成交率/对账健康均达标）后改参落地。

---

## 9. 演练清单（上线前一次 + 之后定期）

> 全部在 `shadow` 或最小档位（数十~数百 U）下演练，验证「人 + 系统」联合处置链路真的能跑通。

- [ ] **紧急停机演练**：创建/删除 emergency stop 文件，确认引擎在安全点停机、清除后可恢复。
- [ ] **SIGTERM 优雅停机**：`systemctl stop`，确认日志有 `graceful_shutdown` 事件、状态/账本已落盘、dead-man 已停用。
- [ ] **dead-man 兜底**：启用 dead-man，`kill -9` 主进程（模拟崩溃），确认交易所在倒计时到期后自动撤掉测试挂单。
- [ ] **看门狗拉起**：停掉心跳/healthz（或杀进程），确认 `watchdog.sh` 连续失败达阈值后重启服务并告警。
- [ ] **防双开**：在已运行时再启动一个实例，确认单写租约拒绝第二个并打印既有持有者 pid/host。
- [ ] **熔断演练**：把某项 `Max*Loss/Drawdown` 阈值临时调到极小触发熔断，确认 fail-safe 停机 + 持久化 + 告警。
- [ ] **单腿裸露演练**（谨慎，最小额）：模拟一腿失败，确认恢复协议或人工 SOP 能把敞口拉回中性。
- [ ] **对账演练**：跑 `lingxi reconcile`，确认报告可读、未配对=0、摩擦/偏差口径理解一致。
- [ ] **改参/回滚演练**：preview → 收紧某阈值 → rollback，确认 diff 与回滚均符合预期。
- [ ] **密钥轮换演练**：禁用旧 Key、换新 Key（无提现 + IP 白名单）、重启，确认权限自检通过。

---

## 10. 关键文件 / 命令速查

| 用途 | 路径 / 命令 |
|---|---|
| 紧急停机 | `touch <emergency_stop 文件>`；清除后自动恢复 |
| 停服务（优雅） | `systemctl stop lingxi`（SIGTERM → 优雅收尾） |
| 看门狗 | `deploy/watchdog.sh`（systemd timer / cron） |
| 对账 | `lingxi reconcile` |
| 结算事实源 | `records/settlements_journal.jsonl`（append-only） |
| pending 事实源 | `records/pending_funding_journal.jsonl`（append-only） |
| 事件流 | `records/domain_events.jsonl`（含 critical 告警事件） |
| 心跳 | `records/heartbeat.json`（`ts` 判活） |
| 健康探针 | `GET /healthz`（503=未就绪/熔断）、`/livez`、`/metrics` |
| 单写租约 | `records/.lingxi-live.lock`（flock，含持有者诊断） |
