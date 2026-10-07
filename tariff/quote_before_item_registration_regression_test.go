package tariff

import (
	"testing"
	"time"
)

// “首次报价时整个费率项尚未登记，随后才补登记费率”的回归保障。
//
// 与“费率项存在、补登缺失版本后原样重试”的既有检查不同，这里保护的是
// 费率项一个版本都没有时受理的拒绝：首次受理结果不会被后来的登记改写，
// 旧拒绝与之后的新确认能够同时被核对。
//
// 固定时间线（全部为 2026 年 UTC 时刻，结束时刻不含）：
//
//	首次报价：2026-03-15 10:00，seat/v1、数量 4，此时 seat 尚未登记任何版本
//	  → 受理但拒绝，原因 version_not_found，单价与总价均为零
//	补登记：seat/v1，单价 150 分，2026-03-01 00:00 至 2026-03-31 00:00，
//	  有效期覆盖先前的受理时刻
//	重试与新报价：2026-03-16 10:00，原标识原样重试仍返回首次拒绝；
//	  换用从未使用过的新标识才按 v1 确认 150 分 × 4 = 600 分
var (
	qiFirstAccept  = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC) // 首次受理时刻
	qiV1Start      = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	qiV1End        = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 结束时刻不含
	qiSecondAccept = time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
)

func TestRejectionBeforeItemRegistrationSurvivesLaterRegistration(t *testing.T) {
	now, setNow := fixedClock(qiFirstAccept)
	b := NewBook(WithClock(now))

	req := QuoteRequest{RequestID: "q-first-rejected", ItemID: "seat", VersionID: "v1", Quantity: 4}

	// 费率项一个版本都没有时首次提交：调用本身不返回错误，
	// 结果是未确认的拒绝，单价与总价均为零，保留原请求内容和首次受理时刻。
	first, err := b.Quote(req)
	if err != nil {
		t.Fatalf("拒绝是正常受理，err 应为空: %v", err)
	}
	if first.Confirmed {
		t.Fatalf("费率项尚未登记，报价不能确认: %+v", first)
	}
	if first.Reason != ReasonVersionNotFound {
		t.Fatalf("拒绝原因=%q, want %q", first.Reason, ReasonVersionNotFound)
	}
	if first.UnitPrice != 0 || first.Total != 0 {
		t.Fatalf("拒绝结果单价和总价都应为零: 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	if first.Request != req {
		t.Fatalf("首次结果未保留原请求内容: %+v vs %+v", first.Request, req)
	}
	if !first.AcceptedAt.Equal(qiFirstAccept) {
		t.Fatalf("首次受理时刻=%v, want %v", first.AcceptedAt, qiFirstAccept)
	}

	// 费率尚未登记时，这份拒绝就应能按请求标识查到，
	// 不能因为该项没有版本而把拒绝当作未受理过。
	got, err := b.Lookup("q-first-rejected")
	if err != nil {
		t.Fatalf("首次拒绝应可按标识查询: %v", err)
	}
	if got != first {
		t.Fatalf("查询结果与首次拒绝不一致: %+v vs %+v", got, first)
	}

	// 同样应出现在 seat 项的已受理报价列表中，即使该项从未登记过版本。
	listed, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatalf("未登记费率项的列表查询不应报错: %v", err)
	}
	if len(listed) != 1 || listed[0] != first {
		t.Fatalf("首次拒绝应列入 seat 的已受理报价: %+v", listed)
	}

	// 补登记 seat/v1：有效期覆盖先前报价的受理时刻，登记应成功。
	v1End := qiV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: qiV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("有效期覆盖先前受理时刻的补登记应成功: %v", err)
	}

	// 按先前的受理时刻查询生效版本能取得 v1，
	// 但这不表示当时的拒绝可以被修正为确认。
	eff, err := b.EffectiveVersionAt("seat", qiFirstAccept)
	if err != nil {
		t.Fatalf("补登记后按 3 月 15 日 10:00 查询应取得生效版本: %v", err)
	}
	if eff.VersionID != "v1" || eff.UnitPrice != 150 {
		t.Fatalf("3 月 15 日 10:00 的生效版本应为 v1/150, got %s/%d", eff.VersionID, eff.UnitPrice)
	}

	// 登记本身不生成报价：列表仍只有那份首次拒绝。
	listed, err = b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != first {
		t.Fatalf("登记本身不应生成报价记录: %+v", listed)
	}

	// 受理时刻推进到 3 月 16 日 10:00。
	setNow(qiSecondAccept)

	// 原标识、原内容再次提交：仍返回首次拒绝，原因、零金额、
	// 请求来源和 3 月 15 日的受理时刻都保持原值。
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatalf("原样重试: %v", err)
	}
	if replay != first {
		t.Fatalf("原样重试被重算: %+v -> %+v", first, replay)
	}
	if replay.Confirmed || replay.Reason != ReasonVersionNotFound ||
		replay.UnitPrice != 0 || replay.Total != 0 {
		t.Fatalf("重试必须仍是首次拒绝: %+v", replay)
	}
	if !replay.AcceptedAt.Equal(qiFirstAccept) {
		t.Fatalf("重试的受理时刻被改写: %v", replay.AcceptedAt)
	}

	// 按标识查询也一样指向首次拒绝。
	got, err = b.Lookup("q-first-rejected")
	if err != nil {
		t.Fatalf("补登记后查询原标识: %v", err)
	}
	if got != first {
		t.Fatalf("补登记改写了原标识的结果: %+v -> %+v", first, got)
	}

	// 仅换一个从未使用过的请求标识，仍引用 seat/v1、数量 4：
	// 版本已生效，应确认单价 150 分、总价 600 分，受理时刻为 3 月 16 日 10:00。
	fresh, err := b.Quote(QuoteRequest{
		RequestID: "q-second-confirmed", ItemID: "seat", VersionID: "v1", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("新标识报价: %v", err)
	}
	if !fresh.Confirmed || fresh.Reason != ReasonNone {
		t.Fatalf("版本已生效，新标识报价应确认: %+v", fresh)
	}
	if fresh.UnitPrice != 150 || fresh.Total != 600 {
		t.Fatalf("新报价金额: 单价=%d 总价=%d, want 150/600", fresh.UnitPrice, fresh.Total)
	}
	if !fresh.AcceptedAt.Equal(qiSecondAccept) {
		t.Fatalf("新报价受理时刻=%v, want %v", fresh.AcceptedAt, qiSecondAccept)
	}

	// 按 seat 查看：先前拒绝与这笔新确认同时保留，按首次受理时刻先后排列。
	listed, err = b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("应同时保留旧拒绝与新确认, got %d: %+v", len(listed), listed)
	}
	if listed[0] != first || listed[1] != fresh {
		t.Fatalf("列表应按首次受理时刻列为旧拒绝、新确认: %+v", listed)
	}

	// 原样重试不增加记录。
	if _, err := b.Quote(req); err != nil {
		t.Fatal(err)
	}
	listed, err = b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("原样重试不应增加记录, got %d: %+v", len(listed), listed)
	}

	// 两笔分别按自己的标识查询，结果与各自首次返回值一致。
	gotOld, err := b.Lookup("q-first-rejected")
	if err != nil {
		t.Fatal(err)
	}
	if gotOld != first {
		t.Fatalf("旧拒绝被改写: %+v -> %+v", first, gotOld)
	}
	// 旧拒绝里的零金额不能被解释成免费确认价：未确认且带拒绝原因。
	if gotOld.Confirmed || gotOld.Reason != ReasonVersionNotFound {
		t.Fatalf("零金额拒绝不能被解释成免费确认价: %+v", gotOld)
	}
	gotFresh, err := b.Lookup("q-second-confirmed")
	if err != nil {
		t.Fatal(err)
	}
	if gotFresh != fresh {
		t.Fatalf("新确认查询结果不一致: %+v vs %+v", gotFresh, fresh)
	}
}
