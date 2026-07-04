#!/bin/sh
# backup_snapshot.sh —— 交易记录 + 日志 4 小时快照(2026-07-03,应对 7-02 工作区被毁事故)。
#
# 做什么:
#   1. 把关键交易记录(settlements/持仓/控制/冷却/权益/风控状态/审计)+ 服务日志 + 生效配置
#      打成一个带时间戳的 tar.gz,存到独立于工作区的 /root/lingxi_backups/(工作区被 git reset/checkout
#      抹掉也伤不到备份目录)。
#   2. 服务日志切段:归档后 truncate 归零(进程以 O_APPEND 写,truncate 安全=copytruncate 语义),
#      每个包正好是一段 ~4 小时的日志。**交易记录只复制绝不截断**(settlements 等是风控/面板的事实源)。
#   3. 保留最近 LINGXI_BACKUP_KEEP 个包(默认 90 个 ≈ 15 天),更早的自动清掉。
#
# 刻意排除: fast_dynamic_feature_events.jsonl 等百 MB 级研究采集流(record-only、可再生,
# 4 小时一份会把磁盘吃爆;需要长期保存请单独 rsync)。
#
# 用法: backup_snapshot.sh [手动执行亦可,幂等]
# 定时: lingxi-backup.timer 每 4 小时触发(见同目录 systemd 单元)。

set -u

LINGXI_DIR="${LINGXI_DIR:-/root/lingxi}"
RECORD_DIR="$LINGXI_DIR/records"
BACKUP_DIR="${LINGXI_BACKUP_DIR:-/root/lingxi_backups}"
KEEP="${LINGXI_BACKUP_KEEP:-90}"
TS=$(date -u +%Y%m%d_%H%M%S)
STAGE="$BACKUP_DIR/.staging_$TS"
OUT="$BACKUP_DIR/lingxi_${TS}.tar.gz"

mkdir -p "$BACKUP_DIR" || exit 1
mkdir -p "$STAGE/records" || exit 1

# copy_if 存在才拷,保持 records/ 相对结构。
copy_if() {
  src="$1"
  [ -e "$src" ] || return 0
  rel="${src#"$RECORD_DIR"/}"
  dest="$STAGE/records/$rel"
  mkdir -p "$(dirname "$dest")"
  cp -p "$src" "$dest" 2>/dev/null
}

# 1) 各所方向引擎:结算(钱的历史,最珍贵)/持仓/控制/冷却/fast 持仓/universe/开仓决策/影子信号
for v in okx bybit binance gate; do
  d="$RECORD_DIR/${v}_directional"
  [ -d "$d" ] || continue
  for f in settlements.jsonl position.json control.json cooldowns.json \
           fast_positions.json fast_cooldowns.jsonl universe.json \
           open_decision_events.jsonl shadow_fast_signal_events.jsonl \
           slow_position_cleanup.jsonl fast_position_cleanup.jsonl; do
    copy_if "$d/$f"
  done
done

# 2) 账户级:权益快照/采样健康/风控观测与状态/控制面/审计/费率
for f in crossvenue/equity_snapshots.jsonl crossvenue/equity_sampler_health.json \
         account_risk_watch.json account_risk_equity_state.json \
         control_plane.json dashboard_control_audit.jsonl \
         fee_rebate.json fee_discount_audit.jsonl execution_incidents.jsonl \
         recent_flatten.jsonl heartbeat.json; do
  copy_if "$RECORD_DIR/$f"
done

# 3) 生效配置快照(排查"当时参数是什么"用;secret.yaml 绝不入包)
cp -p "$LINGXI_DIR/config.yaml" "$STAGE/config.yaml" 2>/dev/null

# 4) 服务日志:先拷进包,归档成功后 truncate 切段(失败则不截断,宁可重复不可丢失)
LOG="$RECORD_DIR/lingxi_server.log"
LOG_COPIED=0
if [ -f "$LOG" ]; then
  cp -p "$LOG" "$STAGE/records/lingxi_server.log" && LOG_COPIED=1
fi

# 5) 打包(独立目录,原子 mv)
tar -czf "$OUT.tmp" -C "$STAGE" . && mv "$OUT.tmp" "$OUT"
RC=$?
rm -rf "$STAGE"
if [ $RC -ne 0 ]; then
  echo "[lingxi-backup] 打包失败 rc=$RC" >&2
  rm -f "$OUT.tmp"
  exit $RC
fi

# 6) 打包成功才切日志段(truncate 对 O_APPEND 写者安全)
if [ "$LOG_COPIED" = 1 ]; then
  truncate -s 0 "$LOG" 2>/dev/null || : > "$LOG"
fi

# 6.5) 研究流滚动(2026-07-04):可再生的大研究流 >2GB 时 rename 轮换(保留一代 .1),
# 防磁盘被吃爆(hot_micro ~3.5GB/天)。appendJSONLRecord 每次 open-append-close,rename 后
# 下次写入自动重建新文件,零丢失窗口;近期研究窗口(~半天到一天)始终可用(当前文件+.1)。
for big in "$RECORD_DIR/okx_directional/fast_dynamic_feature_events.jsonl" \
           "$RECORD_DIR/okx_directional/fast_hot_micro.jsonl"; do
  [ -f "$big" ] || continue
  sz=$(stat -c %s "$big" 2>/dev/null || echo 0)
  if [ "$sz" -gt 2147483648 ]; then
    mv -f "$big" "$big.1" && echo "[lingxi-backup] 研究流轮换 $(basename "$big") ($(numfmt --to=iec "$sz" 2>/dev/null || echo "$sz"B))"
  fi
done

# 7) 保留策略:按名字排序(=按时间),删最旧的
count=$(ls -1 "$BACKUP_DIR"/lingxi_*.tar.gz 2>/dev/null | wc -l)
if [ "$count" -gt "$KEEP" ]; then
  ls -1 "$BACKUP_DIR"/lingxi_*.tar.gz | head -n $((count - KEEP)) | while read -r old; do
    rm -f "$old"
  done
fi

echo "[lingxi-backup] OK $OUT ($(du -h "$OUT" | cut -f1)) 现存 $(ls -1 "$BACKUP_DIR"/lingxi_*.tar.gz | wc -l) 个包"
