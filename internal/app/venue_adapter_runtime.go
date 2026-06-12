package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"lingxi/internal/venue"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// venue_adapter_runtime.go 把 P1 跨所对冲腿所(okx/gate)的 venue.Venue 适配器接线进编排层:
// 注册时按 secret.yaml 构造适配器并挂到 venueRuntime.venueAdapter,供面板可见、凭证注入、拉行情/账户。
// **不启动任何交易引擎**——Start 仍由 runtime_controller 的安全闸拒绝(与 bybit 不同,bybit 适配器在 executor 内按需构造)。

// venuesRequiringAdapter 注册时必须成功注入 venueAdapter 的所;凭证缺失 → 启动隔离跳过该所。
var venuesRequiringAdapter = map[string]bool{
	okx.VenueName:  true,
	gate.VenueName: true,
}

// venueAdapterFactory 可注入(单测替换 fake,避免触网)。
var venueAdapterFactory = buildRegisteredVenueAdapter

// defaultSecretPath 返回 secret.yaml 路径(LINGXI_SECRET_PATH 可覆盖,与 run.go 一致)。
func defaultSecretPath() string {
	path := strings.TrimSpace(os.Getenv("LINGXI_SECRET_PATH"))
	if path == "" {
		return "secret.yaml"
	}
	return path
}

// buildRegisteredVenueAdapter 按 venue 名加载凭证并构造已登记的适配器(仅 okx/gate)。
func buildRegisteredVenueAdapter(venueName, secretPath string) (venue.Venue, error) {
	venueName = strings.ToLower(strings.TrimSpace(venueName))
	sec, err := LoadVenueSecretConfig(secretPath, venueName)
	if err != nil {
		return nil, err
	}
	cfg := venue.Config{
		Name:       venueName,
		APIKey:     sec.APIKey,
		APISecret:  sec.Secret,
		Passphrase: sec.Passphrase,
		ProxyURL:   sec.ProxyURL,
	}
	switch venueName {
	case okx.VenueName:
		return okx.New(cfg)
	case gate.VenueName:
		return gate.New(cfg)
	default:
		return nil, fmt.Errorf("venue %q 未登记适配器工厂", venueName)
	}
}

// wireVenueAdapter 为 okx/gate 注入 venueAdapter;其它所 no-op(币安/bybit 行为不变)。
func wireVenueAdapter(rt *venueRuntime, secretPath string, logger *log.Logger) error {
	if rt == nil || !venuesRequiringAdapter[rt.name] {
		return nil
	}
	if secretPath == "" {
		secretPath = defaultSecretPath()
	}
	adp, err := venueAdapterFactory(rt.name, secretPath)
	if err != nil {
		return fmt.Errorf("venue=%s 凭证/适配器: %w", rt.name, err)
	}
	rt.venueAdapter = adp
	if logger != nil {
		logger.Printf("[编排] venue=%s 适配器已注入(仅行情/账户,不启动交易引擎)", rt.name)
	}
	return nil
}

// buildRegisteredVenueRuntime 构建某所运行时并接线 okx/gate 适配器(供 registerEnabledVenues 消费)。
func buildRegisteredVenueRuntime(ctx context.Context, p VenuePlan, logger *log.Logger) (*venueRuntime, error) {
	rt, err := buildVenueRuntime(ctx, p.Config, p.Name, logger)
	if err != nil {
		return nil, err
	}
	if err := wireVenueAdapter(rt, defaultSecretPath(), logger); err != nil {
		return nil, err
	}
	return rt, nil
}
