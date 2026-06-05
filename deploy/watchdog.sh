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
STATE_DIR="${LINGXI_WATCHDOG_STATE_DIR:-$RECORD_DIR}"
FAIL_COUNT_FILE="$STATE_DIR/.watchdog_fail_count"
LAST_RESTART_FILE="$STATE_DIR/.watchdog_last_restart"

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] watchdog: $*"; }

alert() {
  msg="$1"
  log "ALERT: $msg"
  if [ -n "$ALERT_WEBHOOK" ]; then
    payload=$(printf '{"level":"critical","title":"lingxi watchdog","detail":"%s"}' "$msg")
    curl -fsS -m 5 -X POST -H 'Content-Type: application/json' -d "$payload" "$ALERT_WEBHOOK" >/dev/null 2>&1 || true
  fi
}

now_epoch() { date -u +%s; }

read_int_file() {
  f="$1"; default="$2"
  if [ -f "$f" ]; then cat "$f" 2>/dev/null || echo "$default"; else echo "$default"; fi
}

# heartbeat_age 返回心跳文件年龄(秒);文件缺失/无 ts 返回一个超大值(视为不健康)。
heartbeat_age() {
  if [ ! -f "$HEARTBEAT_FILE" ]; then echo 999999; return; fi
  ts=$(sed -n 's/.*"ts"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$HEARTBEAT_FILE" | head -1)
  if [ -z "${ts:-}" ]; then echo 999999; return; fi
  echo $(( $(now_epoch) - ts ))
}

# health_ok 返回 0 表示 /healthz 200;非 200 / 无法连接返回 1。
health_ok() {
  code=$(curl -fsS -m 5 -o /dev/null -w '%{http_code}' "$HEALTH_URL" 2>/dev/null || echo 000)
  [ "$code" = "200" ]
}

check_once() {
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
