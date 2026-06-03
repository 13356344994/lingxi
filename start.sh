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

# 记录启动前的日志行数，便于只在「本次启动」新追加的日志里提取控制令牌，避免误取上一次的旧令牌。
LOG_LINES_BEFORE=0
if [ -f "$LOG_FILE" ]; then
  LOG_LINES_BEFORE=$(wc -l <"$LOG_FILE" 2>/dev/null || echo 0)
fi

echo "启动 lingxi..."
nohup "$BIN_FILE" >>"$LOG_FILE" 2>&1 &
NEW_PID=$!
echo "$NEW_PID" >"$PID_FILE"

sleep 1
if ! kill -0 "$NEW_PID" 2>/dev/null; then
  echo "lingxi 启动失败，请检查日志: $LOG_FILE" >&2
  rm -f "$PID_FILE"
  exit 1
fi

echo "lingxi 已启动"
echo "PID: $NEW_PID"
echo "日志: $LOG_FILE"
echo "面板: http://127.0.0.1:$PORT/"

# 程序通过 nohup 把输出重定向到日志文件，控制令牌不会直接出现在终端；
# 这里从本次启动新追加的日志里把它提取出来并打印到控制台（最多等待约 5 秒）。
TOKEN=""
i=0
while [ "$i" -lt 25 ]; do
  TOKEN=$(tail -n "+$((LOG_LINES_BEFORE + 1))" "$LOG_FILE" 2>/dev/null | sed -n 's/.*Dashboard 控制令牌: //p' | tail -1 || true)
  if [ -n "$TOKEN" ]; then
    break
  fi
  sleep 0.2
  i=$((i + 1))
done
if [ -n "$TOKEN" ]; then
  echo "控制令牌: $TOKEN"
else
  echo "控制令牌: 暂未捕获，可执行: grep '控制令牌' \"$LOG_FILE\""
fi
exit 0
