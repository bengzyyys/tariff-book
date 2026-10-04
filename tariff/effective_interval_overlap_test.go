package tariff

import (
	"errors"
	"testing"
	"time"
)

// 后续登记场景的固定时间线（UTC）：
//
//	旧版 seat-old：  单价 150 分，3 月 1 日零点生效，登记结束 3 月 31 日零点
//	短期 seat-mid： 单价 180 分，3 月 10 日零点起替代旧版，3 月 20 日零点结束
//	后续 seat-next：单价 200 分，3 月 20 日零点起，3 月 25 日零点结束，不填写替代来源
//
// 后续版本落在旧版登记的起止时间内，但旧版实际有效期 3 月 10 日零点即被截断，
// 短期版本 3 月 20 日零点到期，因此 3 月 20 日起没有任何仍实际有效的版本区间被占用。
var (
	gapOldStart  = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	gapOldEnd    = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	gapMidStart  = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	gapMidEnd    = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	gapNextStart = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	gapNextEnd   = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
)

// newGapBook 返回一本已登记旧版与短期版本交接关系的账本及其时钟。
func newGapBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-old", UnitPrice: 150,
		Start: gapOldStart, End: &gapOldEnd,
	}); err != nil {
		t.Fatalf("register seat-old: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-mid", UnitPrice: 180,
		Start: gapMidStart, End: &gapMidEnd, Replaces: "seat-old",
	}); err != nil {
		t.Fatalf("register seat-mid: %v", err)
	}
	return b, setNow
}

// checkGapViews 校验旧版与短期版本的登记边界、实际有效区间和替代关系。
// 后续登记（无论成败）都不得延长或恢复前两版的有效期，也不得改写替代关系。
func checkGapViews(t *testing.T, views []VersionView) {
	t.Helper()
	var old, mid *VersionView
	for i := range views {
		switch views[i].VersionID {
		case "seat-old":
			old = &views[i]
		case "seat-mid":
			mid = &views[i]
		}
	}
	if old == nil || mid == nil {
		t.Fatalf("views missing old/mid version: %+v", views)
	}
	// 旧版：登记结束仍是月底，实际结束仍是 3 月 10 日零点的交接点。
	if old.End == nil || !old.End.Equal(gapOldEnd) {
		t.Fatalf("old registered end changed: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(gapMidStart) {
		t.Fatalf("old effective end changed: %v", old.EffectiveEnd)
	}
	if old.SupersededBy != "seat-mid" {
		t.Fatalf("old superseded-by changed: %q", old.SupersededBy)
	}
	// 短期版本：结束仍是 3 月 20 日零点，替代来源不变，未被后续版本顶替。
	if mid.End == nil || !mid.End.Equal(gapMidEnd) {
		t.Fatalf("mid registered end changed: %v", mid.End)
	}
	if mid.EffectiveEnd == nil || !mid.EffectiveEnd.Equal(gapMidEnd) {
		t.Fatalf("mid effective end changed: %v", mid.EffectiveEnd)
	}
	if mid.Replaces != "seat-old" {
		t.Fatalf("mid replacement source changed: %q", mid.Replaces)
	}
	if mid.SupersededBy != "" {
		t.Fatalf("mid must not be superseded by the follow-up: %q", mid.SupersededBy)
	}
}

// 后续版本虽处在旧版登记的起止时间内，但不占用任何仍实际有效的区间，应登记成功；
// 登记后三个版本按生效时刻排列，前两版的边界与替代关系保持不变。
func TestFollowUpRegistrationUsesEffectiveInterval(t *testing.T) {
	b, _ := newGapBook(t, gapOldStart)

	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-next", UnitPrice: 200,
		Start: gapNextStart, End: &gapNextEnd,
	}); err != nil {
		t.Fatalf("follow-up registration must succeed: %v", err)
	}

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("want 3 versions, got %d", len(views))
	}
	// 按生效时刻排列：旧版、短期、后续。
	if views[0].VersionID != "seat-old" || views[1].VersionID != "seat-mid" || views[2].VersionID != "seat-next" {
		t.Fatalf("unexpected order: %v, %v, %v",
			views[0].VersionID, views[1].VersionID, views[2].VersionID)
	}
	checkGapViews(t, views)

	// 后续版本：登记边界与实际有效区间一致，未声明替代来源。
	next := views[2]
	if next.UnitPrice != 200 {
		t.Fatalf("next unit price: %d", next.UnitPrice)
	}
	if !next.Start.Equal(gapNextStart) || next.End == nil || !next.End.Equal(gapNextEnd) {
		t.Fatalf("next registered interval: %v -> %v", next.Start, next.End)
	}
	if next.EffectiveEnd == nil || !next.EffectiveEnd.Equal(gapNextEnd) {
		t.Fatalf("next effective end: %v", next.EffectiveEnd)
	}
	if next.Replaces != "" {
		t.Fatalf("next must not claim a replacement source: %q", next.Replaces)
	}
}

// 后续版本若提前到 3 月 19 日零点开始，就与短期版本的实际有效期重叠，应返回重叠错误；
// 失败后已登记版本的边界与替代关系不变，失败版本不出现；把开始改回 3 月 20 日零点，
// 沿用同一版本标识即可成功——未成功的登记不占用版本标识。
func TestFollowUpRegistrationOverlapRejected(t *testing.T) {
	b, _ := newGapBook(t, gapOldStart)

	early := time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-next", UnitPrice: 200,
		Start: early, End: &gapNextEnd,
	}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("want ErrOverlap, got %v", err)
	}

	// 失败后账本与操作前一致：仍只有两个版本，边界与替代关系未变。
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("failed registration left changes behind: %d versions", len(views))
	}
	checkGapViews(t, views)

	// 同一版本标识未被失败登记占用，改到 3 月 20 日零点开始即可成功。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-next", UnitPrice: 200,
		Start: gapNextStart, End: &gapNextEnd,
	}); err != nil {
		t.Fatalf("retry with same version id must succeed: %v", err)
	}
	views, err = b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 || views[2].VersionID != "seat-next" {
		t.Fatalf("follow-up version missing after retry: %+v", views)
	}
	checkGapViews(t, views)
}

// 3 月 20 日零点用未使用过的请求标识分别引用三个版本：
// 旧版与短期版本均以 version_expired 拒绝（单价、总价为零，err 为空），
// 后续版本按 200 分确认，数量 4 得 800 分；结果保留指定的费率项、版本和数量。
// 结束时刻不含在有效期内：3 月 25 日零点后续版本同样失效。
func TestFollowUpQuotesAtBoundary(t *testing.T) {
	b, setNow := newGapBook(t, gapOldStart)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-next", UnitPrice: 200,
		Start: gapNextStart, End: &gapNextEnd,
	}); err != nil {
		t.Fatalf("register seat-next: %v", err)
	}

	setNow(gapNextStart)
	cases := []struct {
		name      string
		requestID string
		versionID string
		confirmed bool
		unitPrice int64
		total     int64
		reason    RejectReason
	}{
		{"旧版已失效", "gap-q-old", "seat-old", false, 0, 0, ReasonVersionExpired},
		{"短期版本已失效", "gap-q-mid", "seat-mid", false, 0, 0, ReasonVersionExpired},
		{"后续版本确认", "gap-q-next", "seat-next", true, 200, 800, ReasonNone},
	}
	for _, c := range cases {
		req := QuoteRequest{RequestID: c.requestID, ItemID: "seat", VersionID: c.versionID, Quantity: 4}
		out, err := b.Quote(req)
		if err != nil {
			t.Fatalf("%s: 拒绝/确认都是正常受理，err 应为空，got %v", c.name, err)
		}
		if out.Confirmed != c.confirmed {
			t.Fatalf("%s: Confirmed=%v, want %v (%+v)", c.name, out.Confirmed, c.confirmed, out)
		}
		if out.UnitPrice != c.unitPrice || out.Total != c.total {
			t.Fatalf("%s: 单价/总价=%d/%d, want %d/%d", c.name, out.UnitPrice, out.Total, c.unitPrice, c.total)
		}
		if out.Reason != c.reason {
			t.Fatalf("%s: 原因=%q, want %q", c.name, out.Reason, c.reason)
		}
		// 结果必须保留请求中的费率项、版本和数量，不能替调用者切换版本。
		if out.Request != req {
			t.Fatalf("%s: 请求来源未保留: %+v vs %+v", c.name, out.Request, req)
		}
		if !out.AcceptedAt.Equal(gapNextStart) {
			t.Fatalf("%s: 受理时刻=%v, want %v", c.name, out.AcceptedAt, gapNextStart)
		}
	}

	// 结束时刻不含在有效期内：3 月 25 日零点后续版本也已失效。
	setNow(gapNextEnd)
	out, err := b.Quote(QuoteRequest{RequestID: "gap-q-end", ItemID: "seat", VersionID: "seat-next", Quantity: 4})
	if err != nil {
		t.Fatalf("拒绝是正常受理，err 应为空: %v", err)
	}
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("end instant is exclusive, want expired rejection: %+v", out)
	}
	if out.UnitPrice != 0 || out.Total != 0 {
		t.Fatalf("拒绝结果金额应为 0: 单价=%d 总价=%d", out.UnitPrice, out.Total)
	}
}
