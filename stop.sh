#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PID_FILE="$SCRIPT_DIR/.lingxi.pid"
PORT=8080

stop_pid() {
  TARGET_PID="$1"
  if [ -z "${TARGET_PID:-}" ]; then
    return 1
  fi
  if ! kill -0 "$TARGET_PID" 2>/dev/null; then
    return 1
  fi

  echo "正在停止 lingxi，PID=$TARGET_PID"
  kill "$TARGET_PID" 2>/dev/null || true

  WAIT_COUNT=0
  while kill -0 "$TARGET_PID" 2>/dev/null; do
    WAIT_COUNT=$((WAIT_COUNT + 1))
    if [ "$WAIT_COUNT" -ge 10 ]; then
      echo "进程未在预期时间内退出，发送强制停止信号"
      kill -9 "$TARGET_PID" 2>/dev/null || true
      break
    fi
    sleep 1
  done
  return 0
}

STOPPED=0

# 首选:停 systemd 单元(显式 stop 不会被 Restart=always 重新拉起);其后再兜底清理可能残留的手动 nohup 进程。
if command -v systemctl >/dev/null 2>&1 && systemctl cat lingxi >/dev/null 2>&1; then
  if systemctl is-active --quiet lingxi; then
    echo "正在停止 lingxi(systemctl stop)..."
    systemctl stop lingxi 2>/dev/null || true
    STOPPED=1
  fi
fi

if [ -f "$PID_FILE" ]; then
  FILE_PID=$(cat "$PID_FILE" 2>/dev/null || true)
  if stop_pid "$FILE_PID"; then
    STOPPED=1
  fi
  rm -f "$PID_FILE"
fi

if [ "$STOPPED" -eq 0 ]; then
  PORT_PID=$(lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null || true)
  if [ -n "${PORT_PID:-}" ] && stop_pid "$PORT_PID"; then
    STOPPED=1
  fi
fi

if [ "$STOPPED" -eq 1 ]; then
  echo "lingxi 已停止"
  exit 0
fi

echo "未发现正在运行的 lingxi 进程"
exit 0
