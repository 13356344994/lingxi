#!/bin/bash
# 第二代判决监视循环(2026-07-06 接管 agent 部署;用户"你来主导"授权)。
# 每 30min:①分拣器影子(fresh mark=07-05 18:00Z 后新样本=第二代判决集)②冠军流水线 verdict。
# 只读研究,不碰实盘。输出追加 records/_secondgen_watch.log;死线=07-08 18:00Z(72h)。
cd "$(dirname "$0")/.." || exit 1
LOG=records/_secondgen_watch.log
FRESH="2026-07-05T18:00:00"
while true; do
  {
    echo "===== $(date -u '+%Y-%m-%dT%H:%M:%SZ') (BJ $(TZ=Asia/Shanghai date '+%m-%d %H:%M')) ====="
    echo "--- 分拣器影子(判决集 since $FRESH) ---"
    timeout 900 python3 records/_sorter_shadow_run.py "$FRESH" 2>&1 | grep -v '^→'
    echo "--- 冠军流水线 verdict ---"
    timeout 900 python3 records/_particle_pipeline.py verdict 2>&1
    echo ""
  } >> "$LOG" 2>&1
  sleep 1800
done
