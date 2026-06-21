#!/bin/sh
# watchdog.sh —— lingxi 外部看门狗(S7-D / R14)。
#
# 职责:独立于主进程,周期性判断「主进程是否还健康地活着」,失活/卡死/不健康时告警并拉起。
# 判活双信号(任一不达标即视为不健康):
#   1) 心跳文件 records/heartbeat.json 的 ts 字段:now - ts 超过 STALE_THRESHOLD 秒 → 卡死/已死;
#   2) /healthz 探针:HTTP 状态码非 200 → 引擎未就绪/已熔断。
#
# 设计要点:
#   - 看门狗必须是独立进程/定时器,不能跑在被监控进程内(否则进程死了看门狗也死了);
#   - 拉起走幂等的 RESTART_CMD(默认 systemctl restart;无 systemd 时可指向 start.sh);
#   - 连续 FAIL_THRESHOLD 次不健康才动作,避免单次抖动(如重启窗口)误杀;
#   - 重启后进入 RESTART_COOLDOWN 静默,避免重启风暴。
#
# 用法:
#   作为 systemd timer 周期触发(见 lingxi-watchdog.{service,timer}),或 cron: * * * * * /path/watchdog.sh
#   也可 WATCHDOG_LOOP=1 常驻循环(自带 sleep CHECK_INTERVAL)。
#
# 可配置环境变量(均有默认值):
set -eu

RECORD_DIR="${LINGXI_RECORD_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")/../records" && pwd)}"
HEARTBEAT_FILE="${LINGXI_HEARTBEAT_FILE:-$RECORD_DIR/heartbeat.json}"
HEALTH_URL="${LINGXI_HEALTH_URL:-http://127.0.0.1:8080/healthz}"
STALE_THRESHOLD="${LINGXI_STALE_THRESHOLD:-90}"          # 心跳超过多少秒视为卡死(建议 ≥ 心跳周期 15s 的数倍)
FAIL_THRESHOLD="${LINGXI_FAIL_THRESHOLD:-3}"             # 连续多少次不健康才重启
CHECK_INTERVAL="${LINGXI_CHECK_INTERVAL:-30}"            # WATCHDOG_LOOP 模式下的检查间隔(秒)
RESTART_COOLDOWN="${LINGXI_RESTART_COOLDOWN:-300}"       # 重启后静默秒数
RESTART_CMD="${LINGXI_RESTART_CMD:-systemctl restart lingxi}"
ALERT_WEBHOOK="${LINGXI_ALERT_WEBHOOK:-}"               # 可选:不健康/重启时 POST 的告警 webhook(钉钉/飞书/自建)
# Telegram 直推:复用主程序同一份 secret.yaml 的 Telegram_Token / Telegram_Chat_ID(顶层扁平键)。
# watchdog 的告警价值恰在「主进程已死」场景——进程内 Telegram 告警此时发不出来,只有 watchdog 能通知到手机。
SECRET_FILE="${LINGXI_SECRET_FILE:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)/secret.yaml}"
STATE_DIR="${LINGXI_WATCHDOG_STATE_DIR:-$RECORD_DIR}"
FAIL_COUNT_FILE="$STATE_DIR/.watchdog_fail_count"
LAST_RESTART_FILE="$STATE_DIR/.watchdog_last_restart"
# 连通性盲区健康信号:主程序权益采样器在「全部 venue 拉取失败」时写此文件(connectivity_blind=true)。
# 这是环境问题(代理/交易所不可达),重启主进程治不了,反而会上演重启风暴(见 2026-06-21 事故)。
SAMPLER_HEALTH_FILE="${LINGXI_SAMPLER_HEALTH_FILE:-$RECORD_DIR/crossvenue/equity_sampler_health.json}"

# 已成交裸仓兜底强平(补全交易所侧 dead-man 的盲区:dead-man 只撤未成交挂单,平不掉已成交的对冲腿)。
# 默认关闭(0):这是会动用资金的实盘动作,须显式开启。开启后,仅当①主进程确已死(心跳年龄 > FLATTEN_STALE_THRESHOLD,
# 取一个明显大于普通卡死阈值的值,避免把「短暂不健康」当死)②本地未平仓台账非空(确有裸仓)时,在重启前先触发一次
# 应急强平。flatten 命令自身还会抢单写租约做二次互锁:若主进程其实还活着持锁,flatten 会自动拒绝,绝不并发平仓。
FLATTEN_ON_DEAD="${LINGXI_FLATTEN_ON_DEAD:-0}"
FLATTEN_STALE_THRESHOLD="${LINGXI_FLATTEN_STALE_THRESHOLD:-300}"   # 心跳超过多少秒才视为「确已死」可兜底强平(须 ≫ STALE_THRESHOLD)
OPEN_HEDGES_FILE="${LINGXI_OPEN_HEDGES_FILE:-$RECORD_DIR/live_open_hedges.json}"
FLATTEN_CMD="${LINGXI_FLATTEN_CMD:-systemctl stop lingxi; lingxi flatten watchdog}"

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] watchdog: $*"; }

# secret_value 从 secret.yaml 读顶层扁平键的值(容忍引号/行尾注释/空格)。文件缺失返回空。
secret_value() {
  key="$1"
  [ -f "$SECRET_FILE" ] || { echo ""; return; }
  sed -n "s/^${key}:[[:space:]]*//p" "$SECRET_FILE" 2>/dev/null | head -1 \
    | sed 's/[[:space:]]*#.*$//; s/^"//; s/"$//; s/^'"'"'//; s/'"'"'$//' | tr -d ' '
}

alert() {
  msg="$1"
  log "ALERT: $msg"
  if [ -n "$ALERT_WEBHOOK" ]; then
    payload=$(printf '{"level":"critical","title":"lingxi watchdog","detail":"%s"}' "$msg")
    curl -fsS -m 5 -X POST -H 'Content-Type: application/json' -d "$payload" "$ALERT_WEBHOOK" >/dev/null 2>&1 || true
  fi
  tg_token=$(secret_value "Telegram_Token")
  tg_chat=$(secret_value "Telegram_Chat_ID")
  if [ -n "$tg_token" ] && [ -n "$tg_chat" ]; then
    curl -fsS -m 5 -X POST "https://api.telegram.org/bot${tg_token}/sendMessage" \
      -d chat_id="${tg_chat}" --data-urlencode text="⚠️ lingxi watchdog: $msg" >/dev/null 2>&1 || true
  fi
}

now_epoch() { date -u +%s; }

read_int_file() {
  f="$1"; default="$2"
  if [ -f "$f" ]; then cat "$f" 2>/dev/null || echo "$default"; else echo "$default"; fi
}

# heartbeat_age 返回心跳年龄(秒)。多 venue 共存一个进程:取主目录与各 venue 子目录
# (如 records/bybit/heartbeat.json)里**最新**的一份——任一执行循环活着即进程活着,
# 避免「只跑 Bybit 时主心跳缺失被误判卡死」。全部缺失/无 ts 返回超大值(视为不健康)。
heartbeat_age() {
  newest_ts=""
  for f in "$HEARTBEAT_FILE" "$RECORD_DIR"/*/heartbeat.json; do
    [ -f "$f" ] || continue
    ts=$(sed -n 's/.*"ts"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$f" | head -1)
    [ -n "${ts:-}" ] || continue
    if [ -z "$newest_ts" ] || [ "$ts" -gt "$newest_ts" ]; then newest_ts="$ts"; fi
  done
  if [ -z "${newest_ts:-}" ]; then echo 999999; return; fi
  echo $(( $(now_epoch) - newest_ts ))
}

# health_ok 返回 0 表示 /healthz 200;非 200 / 无法连接返回 1。
health_ok() {
  code=$(curl -fsS -m 5 -o /dev/null -w '%{http_code}' "$HEALTH_URL" 2>/dev/null || echo 000)
  [ "$code" = "200" ]
}

# ledger_has_hedges 返回 0 表示本地未平仓台账存在且含在场仓。
# 覆盖:legacy live_open_hedges.json、五类策略 positions.json、各 venue bybit_positions.json。
# 未覆盖:venue funding_positions.json 等单所专用台账(与 flatten CLI 范围一致,见 emergency_flatten.go)。
ledger_has_hedges() {
  if [ -f "$OPEN_HEDGES_FILE" ] && grep -q '"symbol"' "$OPEN_HEDGES_FILE" 2>/dev/null; then
    return 0
  fi
  for f in \
    "$RECORD_DIR/crossvenue/positions.json" \
    "$RECORD_DIR/single_venue_spot_perp/positions.json" \
    "$RECORD_DIR/cross_venue_spot_perp/positions.json" \
    "$RECORD_DIR/single_venue_perp_spot/positions.json" \
    "$RECORD_DIR/cross_venue_perp_spot/positions.json"
  do
    if strategy_positions_file_has_active "$f"; then
      return 0
    fi
  done
  for f in "$RECORD_DIR"/*/bybit_positions.json "$RECORD_DIR"/bybit_positions.json; do
    [ -f "$f" ] || continue
    if grep -q '"symbol"' "$f" 2>/dev/null; then return 0; fi
  done
  return 1
}

# strategy_positions_file_has_active 判断五类策略 positions.json 是否含非 closed 持仓。
strategy_positions_file_has_active() {
  f="$1"
  [ -f "$f" ] || return 1
  if command -v jq >/dev/null 2>&1; then
    jq -e '
      (if type == "array" then . elif .positions then .positions else [] end)
      | map(select((.status // "") != "closed" and (.symbol // "") != ""))
      | length > 0
    ' "$f" >/dev/null 2>&1
    return $?
  fi
  grep -q '"symbol"' "$f" 2>/dev/null || return 1
  grep -E '"status"[[:space:]]*:[[:space:]]*"(open|opening_|closing|orphan)' "$f" >/dev/null 2>&1
}

# maybe_flatten_naked_positions 在「确认主进程已死 + 台账有裸仓 + 开关开启」时,于重启前触发一次应急强平兜底。
# flatten 命令内部还会抢单写租约二次互锁(主进程仍存活则自动拒绝),这里只做粗筛与触发。
maybe_flatten_naked_positions() {
  age="$1"
  [ "$FLATTEN_ON_DEAD" = "1" ] || return 0
  if [ "$age" -le "$FLATTEN_STALE_THRESHOLD" ]; then
    log "未达兜底强平的「确已死」阈值(心跳 ${age}s ≤ ${FLATTEN_STALE_THRESHOLD}s),跳过强平,仅常规重启"
    return 0
  fi
  if ! ledger_has_hedges; then
    log "本地未平仓台账无裸仓,无需兜底强平"
    return 0
  fi
  alert "主进程确已死(心跳 ${age}s)且台账存在裸仓,重启前触发应急强平兜底: $FLATTEN_CMD"
  # shellcheck disable=SC2086
  if sh -c "$FLATTEN_CMD"; then
    log "应急强平命令已执行"
  else
    alert "应急强平命令失败,裸仓可能仍在!需立即人工介入: $FLATTEN_CMD"
  fi
}

# connectivity_blind 返回 0 表示主程序正处于「连通性盲区」(全部 venue 采样失败,疑代理/交易所不可达)。
# 仅当健康信号文件较新(5min 内,避免陈旧文件误抑制)且 connectivity_blind=true 才认定。
# 此时进程通常还活着(心跳在更),问题在环境;重启无益且会风暴,故看门狗跳过重启、只告警等待恢复。
connectivity_blind() {
  [ -f "$SAMPLER_HEALTH_FILE" ] || return 1
  [ -n "$(find "$SAMPLER_HEALTH_FILE" -mmin -5 2>/dev/null)" ] || return 1
  grep -q '"connectivity_blind"[[:space:]]*:[[:space:]]*true' "$SAMPLER_HEALTH_FILE" 2>/dev/null
}

# halt_active 返回 0 表示存在急停文件(全局 records/.halt-live 或任一 venue 子目录的 .halt-live)。
# 急停 = 人工有意停止:引擎停转后心跳停更、/healthz 503 都是**预期状态**,看门狗绝不能在此时重启
# (重启 + LINGXI_AUTOSTART_LIVE=1 会复活交易,且循环上演重启风暴)。删除急停文件后恢复正常监护。
halt_active() {
  for f in "$RECORD_DIR"/.halt-live "$RECORD_DIR"/*/.halt-live; do
    [ -f "$f" ] && return 0
  done
  return 1
}

check_once() {
  if halt_active; then
    echo 0 >"$FAIL_COUNT_FILE"
    log "急停文件存在(人工停止),跳过健康检查与重启"
    return 0
  fi
  # 连通性盲区 + 心跳仍新鲜 = 进程活着但代理/交易所不可达:环境问题,重启无益且会风暴 → 跳过重启。
  # 注意必须同时要求心跳新鲜:若进程其实已死(心跳陈旧),仍走下方正常重启逻辑,不被盲区信号误抑制。
  if connectivity_blind && [ "$(heartbeat_age)" -le "$STALE_THRESHOLD" ]; then
    echo 0 >"$FAIL_COUNT_FILE"
    log "连通性盲区(全部 venue 采样失败,疑代理 127.0.0.1:43203 或交易所不可达)且心跳新鲜:环境问题,跳过重启,等待连通性恢复"
    return 0
  fi
  reasons=""
  age=$(heartbeat_age)
  if [ "$age" -gt "$STALE_THRESHOLD" ]; then
    reasons="心跳已 ${age}s 未更新(>阈值 ${STALE_THRESHOLD}s)"
  fi
  if ! health_ok; then
    reasons="${reasons:+$reasons; }/healthz 非 200"
  fi

  if [ -z "$reasons" ]; then
    echo 0 >"$FAIL_COUNT_FILE"
    log "OK: 心跳年龄=${age}s, /healthz=200"
    return 0
  fi

  fails=$(read_int_file "$FAIL_COUNT_FILE" 0)
  fails=$((fails + 1))
  echo "$fails" >"$FAIL_COUNT_FILE"
  log "不健康($fails/$FAIL_THRESHOLD): $reasons"

  if [ "$fails" -lt "$FAIL_THRESHOLD" ]; then
    return 0
  fi

  # 达到连续失败阈值:检查重启冷却。
  last_restart=$(read_int_file "$LAST_RESTART_FILE" 0)
  since=$(( $(now_epoch) - last_restart ))
  if [ "$since" -lt "$RESTART_COOLDOWN" ]; then
    log "处于重启冷却中(${since}s/${RESTART_COOLDOWN}s),本次不重启"
    return 0
  fi

  alert "连续 $fails 次不健康($reasons),执行重启: $RESTART_CMD"
  now_epoch >"$LAST_RESTART_FILE"
  echo 0 >"$FAIL_COUNT_FILE"
  # 重启前的已成交裸仓兜底强平(默认关闭,仅在确认主进程已死时触发;见文件头说明)。
  maybe_flatten_naked_positions "$age"
  # shellcheck disable=SC2086
  if sh -c "$RESTART_CMD"; then
    log "重启命令已执行"
  else
    alert "重启命令失败: $RESTART_CMD"
  fi
}

mkdir -p "$STATE_DIR"

if [ "${WATCHDOG_LOOP:-0}" = "1" ]; then
  log "进入常驻循环模式,间隔 ${CHECK_INTERVAL}s"
  while true; do
    check_once || true
    sleep "$CHECK_INTERVAL"
  done
else
  check_once
fi
