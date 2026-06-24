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
# 注入版本信息（与 Makefile 同口径）：appVersion/gitCommit/buildTime 默认在 internal/app/version.go 里是
# dev/unknown/unknown，必须在编译时用 -ldflags -X 覆盖，否则启动横幅与通知里会显示 dev/unknown。
# 取值无空格（git describe / 短哈希 / ISO 时间），可安全放进单个 -ldflags 字符串。
VERSION_PKG="lingxi/internal/app"
APP_VERSION=$(git -C "$SCRIPT_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_COMMIT=$(git -C "$SCRIPT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-X ${VERSION_PKG}.appVersion=${APP_VERSION} -X ${VERSION_PKG}.gitCommit=${GIT_COMMIT} -X ${VERSION_PKG}.buildTime=${BUILD_TIME}"
echo "构建版本: version=${APP_VERSION} commit=${GIT_COMMIT} build_time=${BUILD_TIME}"
go build -ldflags "$LDFLAGS" -o "$BIN_FILE" ./cmd/lingxi

# SELinux(Enforcing,RHEL8):go build 产物默认带 user_tmp_t/admin_home_t 标签,systemd(init_t)无权 execute
# → 起进程报 203/EXEC。按已登记的 fcontext 规则(/root/lingxi/lingxi → bin_t)重新打标,确保 systemctl 能拉起。
# 一次性前置:semanage fcontext -a -t bin_t '/root/lingxi/lingxi'(已在本机登记)。
if command -v restorecon >/dev/null 2>&1; then
  restorecon -F "$BIN_FILE" 2>/dev/null || true
fi

# 记录启动前的日志行数，便于只在「本次启动」新追加的日志里提取控制令牌，避免误取上一次的旧令牌。
LOG_LINES_BEFORE=0
if [ -f "$LOG_FILE" ]; then
  LOG_LINES_BEFORE=$(wc -l <"$LOG_FILE" 2>/dev/null || echo 0)
fi

# 令牌每次启动都会重新随机生成；stop.sh 不会清理旧令牌文件。这里在启动新进程前先删除残留文件，
# 否则下方读取循环会立刻读到上一次运行的旧令牌（新进程要等冷启动检查后才会覆盖该文件），
# 导致控制台打印的令牌与当前服务器内存中的新令牌不一致，页面会报「后台控制令牌无效」。
TOKEN_FILE="$LOG_DIR/.dashboard_control_token"
rm -f "$TOKEN_FILE"

echo "通过 systemd 启动 lingxi(systemctl restart,受 Restart=always + 看门狗保护;手动 nohup 不受保护)..."
# 用 systemd 作为唯一运行者:崩溃由 Restart=always 自愈,卡死由 watchdog `systemctl restart lingxi` 兜底。
# stdout/stderr 由 unit 内 sh -c + exec 重定向到 $LOG_FILE,与原 nohup 同一份日志。
if command -v systemctl >/dev/null 2>&1 && systemctl cat lingxi >/dev/null 2>&1; then
  systemctl restart lingxi
  sleep 2
  if ! systemctl is-active --quiet lingxi; then
    echo "lingxi 启动失败,请检查: journalctl -u lingxi -n 50  或  $LOG_FILE" >&2
    exit 1
  fi
  NEW_PID=$(systemctl show -p MainPID --value lingxi 2>/dev/null || true)
  echo "$NEW_PID" >"$PID_FILE"
else
  # 回退:无 systemd(本地开发等)时仍用 nohup(不受 Restart=always 保护)。
  echo "未检测到 systemd lingxi 单元,回退 nohup 启动(无崩溃自愈保护)"
  nohup "$BIN_FILE" >>"$LOG_FILE" 2>&1 &
  NEW_PID=$!
  echo "$NEW_PID" >"$PID_FILE"
  sleep 1
  if ! kill -0 "$NEW_PID" 2>/dev/null; then
    echo "lingxi 启动失败，请检查日志: $LOG_FILE" >&2
    rm -f "$PID_FILE"
    exit 1
  fi
fi

echo "lingxi 已启动"
echo "PID: $NEW_PID"
echo "日志: $LOG_FILE"
echo "面板: http://127.0.0.1:$PORT/"

# 程序通过 nohup 把输出重定向到日志文件，控制令牌不会直接出现在终端。
# 程序启动时永远会打印完整控制令牌：既写入仅本机可读(0600)的令牌文件，也以明文写进日志。
# 这里优先从令牌文件读取并打印到本机控制台，其次从本次启动新追加的日志里提取（最多等待约 5 秒）。
# 注意：TOKEN_FILE 已在上方启动前删除，确保这里读到的是本次新进程写出的令牌，而非上一次的残留。
TOKEN=""
i=0
while [ "$i" -lt 25 ]; do
  # 安全:令牌只从仅本机可读(0600)的令牌文件读取;不再从日志解析(日志已不含明文令牌)。
  if [ -f "$TOKEN_FILE" ]; then
    TOKEN=$(tr -d '\r\n' <"$TOKEN_FILE" 2>/dev/null || true)
  fi
  if [ -n "$TOKEN" ]; then
    break
  fi
  sleep 0.2
  i=$((i + 1))
done
if [ -n "$TOKEN" ]; then
  echo "控制令牌: $TOKEN"
else
  echo "控制令牌: 暂未捕获，可执行: cat \"$TOKEN_FILE\"  或  grep '控制令牌' \"$LOG_FILE\""
fi
exit 0
