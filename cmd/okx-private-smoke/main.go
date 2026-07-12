// okx-private-smoke 是 OKX 私有 WS(orders/positions) + REST 持仓的只读冒烟探针。
//
// 【纪律 — 全程只读,零下单/撤单/改配置】
//   - 默认 dry 模式:仅校验凭证存在性、适配器能力、离线 fixture 解析;不拨任何真实网络。
//   - 必须显式 --connect 才允许真实只读连接(StreamUserData + FetchPositions 对账)。
//   - 绝不下单/撤单/划转/改参数;不打印 api/secret/passphrase 或登录签名原文。
//
// 用法:
//
//	go run ./cmd/okx-private-smoke [-secret secret.yaml]
//	go run ./cmd/okx-private-smoke --connect [-secret secret.yaml] [-duration 30s]
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func defaultSecretPath() string {
	if p := strings.TrimSpace(os.Getenv("LINGXI_SECRET_PATH")); p != "" {
		return p
	}
	return "secret.yaml"
}

func main() {
	if err := run(os.Args[1:], os.Stdout, defaultDeps(os.Stdout)); err != nil {
		fmt.Fprintf(os.Stderr, "okx-private-smoke: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, deps smokeDeps) error {
	deps.out = out
	fs := flag.NewFlagSet("okx-private-smoke", flag.ContinueOnError)
	secret := fs.String("secret", defaultSecretPath(), "密钥配置路径(绝不打印)")
	connect := fs.Bool("connect", false, "拨真实私有 WS 只读探针(默认仅 dry 离线校验)")
	duration := fs.Duration("duration", 30*time.Second, "connect 模式监听时长,超时后退出")
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*connect {
		_, err := runDryValidation(deps, *secret)
		return err
	}
	sec, err := deps.loadSecret(*secret)
	if err != nil {
		return fmt.Errorf("connect 模式凭证加载失败: %w", err)
	}
	if !sec.present().complete() {
		return fmt.Errorf("connect 模式凭证字段不完整(需 api/secret/passphrase)")
	}
	adp, err := deps.newAdapter(sec)
	if err != nil {
		return fmt.Errorf("connect 模式适配器构造失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration+20*time.Second)
	defer cancel()
	_, err = runConnectProbe(ctx, adp, *duration, out)
	return err
}
