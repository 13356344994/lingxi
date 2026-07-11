package exam

import (
	"context"
	"testing"
	"time"
)

func TestHMACSignerRoundTripAndTamperDetection(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	payload := ReceiptPayload{ExamID: "e", Generation: "g", AttemptID: "a", Status: StatusConsumedPassed, ConsumedAt: time.Now().UTC()}
	receipt, err := (HMACSigner{KeyID: "test", Key: key}).SignReceipt(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyHMACReceipt(receipt, key) {
		t.Fatal("有效签名未通过验证")
	}
	receipt.Payload.Generation = "tampered"
	if VerifyHMACReceipt(receipt, key) {
		t.Fatal("篡改后的 receipt 不应通过")
	}
}
