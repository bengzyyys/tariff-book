package tariff

import (
	"math"
	"testing"
)

// 金额边界回归：单价不为一、数量恰好达到或刚刚超过可计算上限时的报价。
// 各版本在受理时刻均已生效，结论不依赖真实运行时刻，也不需要等待版本生效。
func TestQuoteAmountBoundary(t *testing.T) {
	now, setNow := fixedClock(at(5))
	b := NewBook(WithClock(now))
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{ItemID: "three", VersionID: "v1", UnitPrice: 3, Start: at(0), End: atPtr(10)})
	must(RegisterRequest{ItemID: "one", VersionID: "v1", UnitPrice: 1, Start: at(0), End: atPtr(10)})
	must(RegisterRequest{ItemID: "free", VersionID: "v1", UnitPrice: 0, Start: at(0), End: atPtr(10)})

	// 三分单价下恰好可计算的最大数量：3 * q == math.MaxInt64 - 2
	const maxQtyForPrice3 = int64(3074457345618258602)
	if maxQtyForPrice3 != math.MaxInt64/3 {
		t.Fatalf("boundary constant drifted: %d", maxQtyForPrice3)
	}

	// 数量恰好达到上限：确认，总价 9223372036854775806 分，实际单价三分，拒绝原因为空
	req := QuoteRequest{RequestID: "b-max3", ItemID: "three", VersionID: "v1", Quantity: maxQtyForPrice3}
	out, err := b.Quote(req)
	if err != nil {
		t.Fatalf("quote at limit: %v", err)
	}
	if !out.Confirmed || out.UnitPrice != 3 || out.Total != 9223372036854775806 || out.Reason != ReasonNone {
		t.Fatalf("at-limit quote: %+v", out)
	}
	if out.Total != 3*maxQtyForPrice3 {
		t.Fatalf("total mismatch: %d", out.Total)
	}
	if !out.AcceptedAt.Equal(at(5)) {
		t.Fatalf("acceptance time: %v", out.AcceptedAt)
	}
	if got, err := b.Lookup("b-max3"); err != nil || got != out {
		t.Fatalf("lookup at-limit: %+v (%v)", got, err)
	}

	// 数量再增加一：总价超出可表示范围，拒绝为 total_overflow。
	// 溢出是受理后的拒绝，调用本身不报错，请求来源保留在结果中。
	over := QuoteRequest{RequestID: "b-over3", ItemID: "three", VersionID: "v1", Quantity: maxQtyForPrice3 + 1}
	rej, err := b.Quote(over)
	if err != nil {
		t.Fatalf("overflow must be an accepted rejection, not a call error: %v", err)
	}
	if rej.Confirmed || rej.Reason != ReasonTotalOverflow {
		t.Fatalf("overflow rejection: %+v", rej)
	}
	if rej.UnitPrice != 0 || rej.Total != 0 {
		t.Fatalf("rejected quote must keep zero amounts: %+v", rej)
	}
	if rej.Request != over {
		t.Fatalf("request source lost: %+v", rej.Request)
	}
	if !rej.AcceptedAt.Equal(at(5)) {
		t.Fatalf("acceptance time: %v", rej.AcceptedAt)
	}
	// 不能留下已确认记录：查询取回的仍是这次拒绝
	got, err := b.Lookup("b-over3")
	if err != nil {
		t.Fatal(err)
	}
	if got != rej || got.Confirmed {
		t.Fatalf("lookup shows confirmed or recomputed: %+v", got)
	}
	// 时钟推进后重试同一请求，仍返回首次受理时刻的首次拒绝
	setNow(at(7))
	replay, err := b.Quote(over)
	if err != nil || replay != rej {
		t.Fatalf("replay recomputed: %+v (%v)", replay, err)
	}
	setNow(at(5))

	// 金额上限本身允许确认：一分单价 × 最大正数量，总价即上限
	maxReq := QuoteRequest{RequestID: "b-max1", ItemID: "one", VersionID: "v1", Quantity: math.MaxInt64}
	maxOut, err := b.Quote(maxReq)
	if err != nil {
		t.Fatal(err)
	}
	if !maxOut.Confirmed || maxOut.UnitPrice != 1 || maxOut.Total != math.MaxInt64 || maxOut.Reason != ReasonNone {
		t.Fatalf("max total: %+v", maxOut)
	}
	if got, _ := b.Lookup("b-max1"); got != maxOut {
		t.Fatalf("lookup max total: %+v", got)
	}

	// 合法的零单价版本：数量再大总价仍为零，确认且不误报溢出
	freeReq := QuoteRequest{RequestID: "b-free", ItemID: "free", VersionID: "v1", Quantity: math.MaxInt64}
	freeOut, err := b.Quote(freeReq)
	if err != nil {
		t.Fatal(err)
	}
	if !freeOut.Confirmed || freeOut.UnitPrice != 0 || freeOut.Total != 0 || freeOut.Reason != ReasonNone {
		t.Fatalf("zero price with max quantity: %+v", freeOut)
	}
	if got, _ := b.Lookup("b-free"); got != freeOut {
		t.Fatalf("lookup zero price: %+v", got)
	}
}

// 零单价不放宽数量要求：数量为零或负数仍按 invalid_quantity 拒绝，单价和总价保持零。
func TestQuoteZeroPriceStillRequiresPositiveQuantity(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "free", VersionID: "v1", UnitPrice: 0, Start: at(0), End: atPtr(10)}); err != nil {
		t.Fatal(err)
	}
	cases := []QuoteRequest{
		{RequestID: "z-zero", ItemID: "free", VersionID: "v1", Quantity: 0},
		{RequestID: "z-neg", ItemID: "free", VersionID: "v1", Quantity: -1},
	}
	for _, req := range cases {
		out, err := b.Quote(req)
		if err != nil {
			t.Fatalf("%+v: %v", req, err)
		}
		if out.Confirmed || out.Reason != ReasonInvalidQuantity {
			t.Fatalf("%+v: want invalid_quantity, got %+v", req, out)
		}
		if out.UnitPrice != 0 || out.Total != 0 {
			t.Fatalf("%+v: amounts must stay zero: %+v", req, out)
		}
		got, err := b.Lookup(req.RequestID)
		if err != nil || got != out {
			t.Fatalf("lookup %+v: %+v (%v)", req, got, err)
		}
	}
}
