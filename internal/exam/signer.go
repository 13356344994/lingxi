package exam

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// HMACSigner 为本地密封考场提供真实的完整性签名边界。生产密钥必须从进程环境或外部
// secret manager 注入，不得写入 registry、命令行历史或仓库。
type HMACSigner struct {
	KeyID string
	Key   []byte
}

func (s HMACSigner) SignReceipt(_ context.Context, payload ReceiptPayload) (Receipt, error) {
	if len(s.Key) < 32 {
		return Receipt{}, errors.New("exam signing key must be at least 32 bytes")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Receipt{}, err
	}
	mac := hmac.New(sha256.New, s.Key)
	_, _ = mac.Write(raw)
	return Receipt{
		Payload: payload, KeyID: strings.TrimSpace(s.KeyID), Algorithm: "HMAC-SHA256",
		Signature: base64.StdEncoding.EncodeToString(mac.Sum(nil)), Signed: true,
		SecurityBoundary: "local_hmac_key_injected_at_runtime",
	}, nil
}

func VerifyHMACReceipt(receipt Receipt, key []byte) bool {
	if !receipt.Signed || receipt.Algorithm != "HMAC-SHA256" || receipt.Signature == "" || len(key) < 32 {
		return false
	}
	got, err := base64.StdEncoding.DecodeString(receipt.Signature)
	if err != nil {
		return false
	}
	raw, err := json.Marshal(receipt.Payload)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return hmac.Equal(got, mac.Sum(nil))
}
