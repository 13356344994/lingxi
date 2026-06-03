#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PID_FILE="$SCRIPT_DIR/.lingxi.pid"
STOP_SCRIPT="$SCRIPT_DIR/stop.sh"
LOG_DIR="$SCRIPT_DIR/records"
LOG_FILE="$LOG_DIR/lingxi_server.log"
BIN_FILE="$SCRIPT_DIR/lingxi"
PORT=8080

mkdir -p "$LOG_DIR"

RUNNING=0
if [ -f "$PID_FILE" ]; then
  EXISTING_PID=$(cat "$PID_FILE" 2>/dev/null || true)
  if [ -n "${EXISTING_PID:-}" ] && kill -0 "$EXISTING_PID" 2>/dev/null; then
    RUNNING=1
  fi
fi

PORT_PID=$(lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null || true)
if [ -n "${PORT_PID:-}" ]; then
  RUNNING=1
fi

if [ "$RUNNING" -eq 1 ]; then
  echo "检测到 lingxi 已启动，先执行重启"
  "$STOP_SCRIPT"
fi

echo "开始构建 lingxi..."
go build -o "$BIN_FILE" ./cmd/lingxi

echo "启动 lingxi..."
nohup "$BIN_FILE" >>"$LOG_FILE" 2>&1 &
NEW_PID=$!
echo "$NEW_PID" >"$PID_FILE"

sleep 1
if kill -0 "$NEW_PID" 2>/dev/null; then
  echo "lingxi 已启动"
  echo "PID: $NEW_PID"
  echo "日志: $LOG_FILE"
  echo "面板: http://127.0.0.1:$PORT/"
  exit 0
fi

echo "lingxi 启动失败，请检查日志: $LOG_FILE" >&2
rm -f "$PID_FILE"
exit 1
