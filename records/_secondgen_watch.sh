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
    echo "--- 烟花反手刀前向判决(预注册 07-06 21:20,判决集=21:00Z 后) ---"
    timeout 900 python3 records/_firework_verdict.py 2>&1
    echo "--- E-v1 冻结版前向判决(00:50Z 封样) ---"
    timeout 900 python3 records/_xgap_reversion_verdict.py 2>&1
    echo "--- E-v2 新鲜价差前向判决(04:15Z 封样) ---"
    timeout 900 python3 records/_xgap_v2_verdict.py 2>&1
    echo "--- 高维分拣器影子(章程㉕,record-only) ---"
    timeout 120 python3 records/_meta_sorter_shadow.py 2>&1
    echo ""
  } >> "$LOG" 2>&1
  sleep 1800
done
