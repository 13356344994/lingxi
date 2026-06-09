package app

import (
	"math"
	"testing"
)

// TestSumMarginInterestByAsset 钉死 P4：interestHistory 回执按资产汇总真实扣息（同资产多笔累加、空资产跳过）。
func TestSumMarginInterestByAsset(t *testing.T) {
	body := []byte(`{"rows":[
		{"asset":"ENA","interest":"0.05","interestAccuredTime":1700000000000,"type":"PERIODIC"},
		{"asset":"ENA","interest":"0.03","interestAccuredTime":1700003600000,"type":"PERIODIC"},
		{"asset":"TAO","interest":"0.0001","interestAccuredTime":1700000000000,"type":"PERIODIC"},
		{"asset":"","interest":"9.99"}
	],"total":4}`)

	byAsset, err := sumMarginInterestByAsset(body)
	if err != nil {
		t.Fatalf("sumMarginInterestByAsset() error = %v", err)
	}
	if math.Abs(byAsset["ENA"]-0.08) > 1e-9 {
		t.Fatalf("ENA 应累加为 0.08，实际 %.8f", byAsset["ENA"])
	}
	if math.Abs(byAsset["TAO"]-0.0001) > 1e-12 {
		t.Fatalf("TAO 应为 0.0001，实际 %.8f", byAsset["TAO"])
	}
	if _, ok := byAsset[""]; ok {
		t.Fatal("空资产行应被跳过，不应出现在结果里")
	}
}

// TestSumMarginInterestByAssetEmpty 空 rows / 空 body 不报错、返回空 map。
func TestSumMarginInterestByAssetEmpty(t *testing.T) {
	byAsset, err := sumMarginInterestByAsset([]byte(`{"rows":[],"total":0}`))
	if err != nil {
		t.Fatalf("空 rows 不应报错: %v", err)
	}
	if len(byAsset) != 0 {
		t.Fatalf("空 rows 应返回空 map，实际 %+v", byAsset)
	}
	if _, err := sumMarginInterestByAsset([]byte(`not-json`)); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
}
