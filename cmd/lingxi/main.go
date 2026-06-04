package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"lingxi/internal/app"
)

// main 是 lingxi 的可执行入口薄壳：只负责进程级信号编排（Ctrl-C / SIGTERM → 取消 ctx）与退出策略。
// 真正的模式选择与依赖装配下沉到 app.Run（返回 error，不再自行 log.Fatalf）；退出码由这里统一决定，
// 满足 Gate 3「main 退化为编排」并让 app 层可被宿主/测试/supervisor 复用（R20）。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx); err != nil {
		stop()
		log.Fatal(err)
	}
}
