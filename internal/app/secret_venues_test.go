package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadSimpleYAMLIgnoresNestedSections 守护「混合格式」铁律:顶层扁平 api/secret/Telegram_*
// 必须被 readSimpleYAML 正确读出,而 bybit/okx/gate 嵌套节的缩进子键(`  api:` 等)绝不能覆盖币安凭证。
func TestReadSimpleYAMLIgnoresNestedSections(t *testing.T) {
	content := "api: \"BINANCE_KEY\"\nsecret: \"BINANCE_SEC\"\nTelegram_Token: \"tok\"\nTelegram_Chat_ID: \"cid\"\n\nbybit:\n  api: \"BYBIT_KEY\"\n  secret: \"BYBIT_SEC\"\n\ngate:\n  api: \"GATE_KEY\"\n  secret: \"GATE_SEC\"\n"
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := readSimpleYAML(p)
	if err != nil {
		t.Fatalf("readSimpleYAML error = %v", err)
	}
	if values["api"] != "BINANCE_KEY" || values["secret"] != "BINANCE_SEC" {
		t.Fatalf("嵌套节污染了币安凭证: api=%q secret=%q", values["api"], values["secret"])
	}
	if values["Telegram_Token"] != "tok" || values["Telegram_Chat_ID"] != "cid" {
		t.Fatalf("Telegram 凭证读取错误: %+v", values)
	}
	// 嵌套节交由 parseVenueSecrets 取用,flat 读取层应能拿到顶层 bybit/gate 占位键但不含其子凭证。
	if values["api"] == "BYBIT_KEY" || values["api"] == "GATE_KEY" {
		t.Fatalf("flat 读取被嵌套子键覆盖")
	}
}

func TestParseVenueSecretsFlatBackwardCompat(t *testing.T) {
	// 现网扁平格式:顶层 api/secret(+Telegram_*)→ 归到 binance,零破坏。
	data := []byte("api: KEY123\nsecret: SEC456\nproxy_url: http://127.0.0.1:7890\nTelegram_Token: tok\n")
	secrets, err := parseVenueSecrets(data)
	if err != nil {
		t.Fatalf("parseVenueSecrets() error = %v", err)
	}
	b, ok := secrets["binance"]
	if !ok {
		t.Fatalf("扁平格式应归到 binance, got %v", secrets)
	}
	cfg, err := secretConfigFromFields("binance", b)
	if err != nil {
		t.Fatalf("secretConfigFromFields() error = %v", err)
	}
	if cfg.APIKey != "KEY123" || cfg.Secret != "SEC456" || cfg.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("扁平币安凭证解析错误: %+v", cfg)
	}
}

func TestParseVenueSecretsNestedSections(t *testing.T) {
	data := []byte(`binance:
  api: BKEY
  secret: BSEC
okx:
  api: OKEY
  secret: OSEC
  passphrase: OPASS
  proxy_url: http://proxy:1080
bybit:
  api: YKEY
  secret: YSEC
`)
	secrets, err := parseVenueSecrets(data)
	if err != nil {
		t.Fatalf("parseVenueSecrets() error = %v", err)
	}
	if len(secrets) != 3 {
		t.Fatalf("应解析出 3 所, got %d (%v)", len(secrets), secrets)
	}
	okx, err := secretConfigFromFields("okx", secrets["okx"])
	if err != nil {
		t.Fatalf("okx 解析失败: %v", err)
	}
	if okx.APIKey != "OKEY" || okx.Secret != "OSEC" || okx.Passphrase != "OPASS" || okx.ProxyURL != "http://proxy:1080" {
		t.Fatalf("okx 凭证(含 passphrase)解析错误: %+v", okx)
	}
	bn, err := secretConfigFromFields("binance", secrets["binance"])
	if err != nil || bn.APIKey != "BKEY" || bn.Passphrase != "" {
		t.Fatalf("binance 节解析错误: %+v err=%v", bn, err)
	}
}

func TestSecretConfigFromFieldsMissingCredentials(t *testing.T) {
	if _, err := secretConfigFromFields("okx", venueSecretFields{"api": "only"}); err == nil {
		t.Fatal("缺 secret 应报错")
	}
}
