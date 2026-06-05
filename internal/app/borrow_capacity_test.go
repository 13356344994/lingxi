package app

import "testing"

func TestDecideBorrowAdmissionDisabledAlwaysAdmits(t *testing.T) {
	res := decideBorrowAdmission(borrowAdmissionInput{
		Asset:              "BTC",
		RequiredBaseQty:    100,
		MaxBorrowableQty:   1,
		MaxBorrowableKnown: true,
		HourlyInterestRate: 9.9,
	}, borrowAdmissionConfig{Enabled: false, MinHeadroomRate: 0.1, MaxHourlyInterestRate: 0.001})
	if !res.Admit {
		t.Fatalf("守卫关闭时应恒放行, got reject: %s", res.Reason)
	}
}

func TestDecideBorrowAdmissionNoBorrowNeeded(t *testing.T) {
	// 正向 carry / 需借量为 0 → 放行(asset 空或 RequiredBaseQty<=0)。
	for _, in := range []borrowAdmissionInput{
		{Asset: "", RequiredBaseQty: 10, MaxBorrowableKnown: true, MaxBorrowableQty: 0},
		{Asset: "BTC", RequiredBaseQty: 0, MaxBorrowableKnown: true, MaxBorrowableQty: 0},
	} {
		res := decideBorrowAdmission(in, borrowAdmissionConfig{Enabled: true, MinHeadroomRate: 0.1})
		if !res.Admit {
			t.Fatalf("无需借币应放行, in=%+v got reject: %s", in, res.Reason)
		}
	}
}

func TestDecideBorrowAdmissionInsufficientCapacityRejects(t *testing.T) {
	// 需借 100,要求 10% 余量 → 需 110;可借 105 < 110 → 剔除。
	res := decideBorrowAdmission(borrowAdmissionInput{
		Asset:              "BTC",
		RequiredBaseQty:    100,
		MaxBorrowableQty:   105,
		MaxBorrowableKnown: true,
	}, borrowAdmissionConfig{Enabled: true, MinHeadroomRate: 0.10})
	if res.Admit {
		t.Fatalf("可借量不足应剔除")
	}
}

func TestDecideBorrowAdmissionSufficientCapacityAdmits(t *testing.T) {
	// 可借 120 ≥ 100×1.1=110 → 放行。
	res := decideBorrowAdmission(borrowAdmissionInput{
		Asset:              "BTC",
		RequiredBaseQty:    100,
		MaxBorrowableQty:   120,
		MaxBorrowableKnown: true,
	}, borrowAdmissionConfig{Enabled: true, MinHeadroomRate: 0.10})
	if !res.Admit {
		t.Fatalf("可借量充足应放行, got reject: %s", res.Reason)
	}
}

func TestDecideBorrowAdmissionUnknownCapacityDoesNotReject(t *testing.T) {
	// 查询失败(MaxBorrowableKnown=false)→ 容量维度不剔除(advisory 不误杀)。
	res := decideBorrowAdmission(borrowAdmissionInput{
		Asset:              "BTC",
		RequiredBaseQty:    100,
		MaxBorrowableQty:   0,
		MaxBorrowableKnown: false,
	}, borrowAdmissionConfig{Enabled: true, MinHeadroomRate: 0.10})
	if !res.Admit {
		t.Fatalf("可借量未知不应据此剔除, got reject: %s", res.Reason)
	}
}

func TestDecideBorrowAdmissionRateCeilingRejects(t *testing.T) {
	// 容量充足但利率 0.002 > 上限 0.001 → 剔除。
	res := decideBorrowAdmission(borrowAdmissionInput{
		Asset:              "BTC",
		RequiredBaseQty:    100,
		MaxBorrowableQty:   1000,
		MaxBorrowableKnown: true,
		HourlyInterestRate: 0.002,
	}, borrowAdmissionConfig{Enabled: true, MinHeadroomRate: 0.10, MaxHourlyInterestRate: 0.001})
	if res.Admit {
		t.Fatalf("利率超上限应剔除")
	}
}

func TestDecideBorrowAdmissionRateCeilingZeroMeansNoLimit(t *testing.T) {
	// 上限 0 = 不限利率,只要容量够就放行。
	res := decideBorrowAdmission(borrowAdmissionInput{
		Asset:              "BTC",
		RequiredBaseQty:    100,
		MaxBorrowableQty:   1000,
		MaxBorrowableKnown: true,
		HourlyInterestRate: 9.9,
	}, borrowAdmissionConfig{Enabled: true, MinHeadroomRate: 0.10, MaxHourlyInterestRate: 0})
	if !res.Admit {
		t.Fatalf("利率上限=0 应不限利率, got reject: %s", res.Reason)
	}
}
