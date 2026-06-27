package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// venue_adapter_runtime.go 把 P1 跨所对冲腿所(okx/gate)的 venue.Venue 适配器接线进编排层:
// 注册时按 secret.yaml 构造适配器并挂到 venueRuntime.venueAdapter,供面板可见、凭证注入、拉行情/账户。
// 交易由 runtime_controller 的 funding 执行器在 Start 时按需构造适配器并启动(默认 OFF模式)。

// venuesRequiringAdapter 注册时必须成功注入 venueAdapter 的所;凭证缺失 → 启动隔离跳过该所。
var venuesRequiringAdapter = map[string]bool{
	okx.VenueName:  true,
	gate.VenueName: true,
}

// venuesOptionalAdapter best-effort 注入 venueAdapter 的所:方向量化需要其 venue.Venue 适配器
// (供方向引擎下单 + 作为 OKX 等所的跨所交叉印证 peer);凭证缺失只跳过该所方向能力,绝不禁用整个 venue 运行时。
var venuesOptionalAdapter = map[string]bool{
	bybit.VenueName: true,
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
	case bybit.VenueName:
		return bybit.New(cfg)
	default:
		return nil, fmt.Errorf("venue %q 未登记适配器工厂", venueName)
	}
}

// wireVenueAdapter 为 okx/gate(必需)与 bybit(best-effort)注入 venueAdapter;其它所 no-op(币安行为不变)。
// 必需所凭证缺失 → 返回错误(启动隔离跳过该所);best-effort 所凭证缺失 → 仅日志跳过其方向能力,不致命。
func wireVenueAdapter(rt *venueRuntime, secretPath string, logger *log.Logger) error {
	if rt == nil {
		return nil
	}
	mandatory := venuesRequiringAdapter[rt.name]
	optional := venuesOptionalAdapter[rt.name]
	if !mandatory && !optional {
		return nil
	}
	if secretPath == "" {
		secretPath = defaultSecretPath()
	}
	adp, err := venueAdapterFactory(rt.name, secretPath)
	if err != nil {
		if optional && !mandatory {
			if logger != nil {
				logger.Printf("[编排] venue=%s 方向适配器注入失败(best-effort,跳过该所方向能力): %v", rt.name, err)
			}
			return nil
		}
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
