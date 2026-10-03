package tariff

import "testing"

// “版本尚未生效”首次拒绝的回归测试。
//
// 场景：费率版本已登记、开始时间在未来（单价整数分、数量合法正整数、
// 结束时间晚于开始时间，不混入数量错误或金额溢出）。生效前用非空标识
// 首次提交，得到已受理但未确认的拒绝（version_not_yet_effective），
// 这份首次拒绝同样被保存。进入有效期后，原标识原样重试仍返回首次拒绝，
// 而不是按当前费率重新计算；要按已生效版本报价必须换用新标识。
//
// 全程使用注入的可推进时钟，重复执行不依赖真实时间跨过生效边界。
func TestNotYetEffectiveRejectionSurvivesEffective(t *testing.T) {
	// 时间线：版本 v-future 单价 250 分，有效区间 [at(100), at(200))；
	// 首次提交发生在 at(50)，即生效之前。
	now, setNow := fixedClock(at(50))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v-future", UnitPrice: 250,
		Start: at(100), End: atPtr(200),
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	req := QuoteRequest{RequestID: "early", ItemID: "seat", VersionID: "v-future", Quantity: 3}

	// 生效前首次提交：调用本身不返回错误，结果为未确认的拒绝，
	// 单价、总价为零，请求内容与受理时刻完整保留。
	first, err := b.Quote(req)
	if err != nil {
		t.Fatalf("first quote must not error: %v", err)
	}
	if first.Confirmed || first.Reason != ReasonVersionNotYetEffective {
		t.Fatalf("want not-yet-effective rejection: %+v", first)
	}
	if first.UnitPrice != 0 || first.Total != 0 {
		t.Fatalf("rejection must carry zero amounts: %+v", first)
	}
	if first.Request != req {
		t.Fatalf("request not preserved: %+v vs %+v", first.Request, req)
	}
	if !first.AcceptedAt.Equal(at(50)) {
		t.Fatalf("first acceptance time: %v", first.AcceptedAt)
	}

	// 首次拒绝也会保存：按标识能查到这份结果，而不是当作未受理过。
	got, err := b.Lookup("early")
	if err != nil {
		t.Fatalf("saved rejection must be queryable: %v", err)
	}
	if got != first {
		t.Fatalf("lookup mismatch: %+v vs %+v", got, first)
	}

	// 推进到生效时刻本身（起点含在有效区间内）：原样重试仍返回首次拒绝。
	// 边界上的拒绝来自保存的首次结果，并非版本依旧不可用——
	// 同一时刻换用新标识即可正常确认。
	setNow(at(100))
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatal(err)
	}
	if replay != first {
		t.Fatalf("replay at effective start recomputed: %+v vs %+v", replay, first)
	}
	boundary, err := b.Quote(QuoteRequest{RequestID: "boundary", ItemID: "seat", VersionID: "v-future", Quantity: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !boundary.Confirmed || boundary.UnitPrice != 250 || boundary.Total != 750 {
		t.Fatalf("effective start is inclusive, fresh id must confirm: %+v", boundary)
	}

	// 推进到生效后、结束时间之前：原标识重试与查询仍指向首次受理的结果与时刻。
	setNow(at(150))
	replay, err = b.Quote(req)
	if err != nil {
		t.Fatal(err)
	}
	if replay != first {
		t.Fatalf("replay after effective recomputed: %+v vs %+v", replay, first)
	}
	got, err = b.Lookup("early")
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("lookup after effective: %+v vs %+v", got, first)
	}
	if !got.AcceptedAt.Equal(at(50)) {
		t.Fatalf("acceptance time moved: %v", got.AcceptedAt)
	}

	// 换用从未使用过的标识，费率项、版本、数量保持一致：
	// 新报价正常确认，单价来自所引用版本，总价为单价乘数量。
	fresh, err := b.Quote(QuoteRequest{RequestID: "fresh", ItemID: "seat", VersionID: "v-future", Quantity: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.Confirmed || fresh.UnitPrice != 250 || fresh.Total != 750 {
		t.Fatalf("fresh quote must confirm at version price: %+v", fresh)
	}
	if !fresh.AcceptedAt.Equal(at(150)) {
		t.Fatalf("fresh acceptance time: %v", fresh.AcceptedAt)
	}

	// 新报价确认后，原请求的确认状态、拒绝原因、金额与首次受理时刻不变；
	// 双方各自按标识查询，互不覆盖。
	got, err = b.Lookup("early")
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("original rejection changed after new quote: %+v vs %+v", got, first)
	}
	if got.Confirmed || got.Reason != ReasonVersionNotYetEffective || got.UnitPrice != 0 || got.Total != 0 {
		t.Fatalf("original rejection mutated: %+v", got)
	}
	if !got.AcceptedAt.Equal(at(50)) {
		t.Fatalf("original acceptance time changed: %v", got.AcceptedAt)
	}
	freshGot, err := b.Lookup("fresh")
	if err != nil {
		t.Fatal(err)
	}
	if freshGot != fresh {
		t.Fatalf("fresh lookup: %+v vs %+v", freshGot, fresh)
	}
}
