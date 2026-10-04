package tariff

import (
	"errors"
	"testing"
	"time"
)

// 旧版本被替代后，后续登记的重叠判断必须依据各版本的“实际有效区间”，
// 不能继续把旧版本登记时填写的结束时间当成占用期限。这是该既有行为的回归测试。
//
// 时间线（UTC，结束时刻均不含）：
//
//	旧版本 seat-v1：单价 150 分，2026-03-01 00:00 生效，登记结束 2026-03-31 00:00
//	短期版本 seat-v2：单价 180 分，2026-03-10 00:00 起替代旧版本，2026-03-20 00:00 结束
//	后续版本 seat-v3：单价 200 分，2026-03-20 00:00 至 2026-03-25 00:00，不填写替代来源
//
// 旧版本实际有效区间被截断到 3 月 10 日，短期版本占用到 3 月 20 日；
// 因此后续版本虽然落在旧版本“登记”的起止时间内，却不与任何仍实际有效的区间重叠。
var (
	marchOldStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	marchOldEnd     = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	marchShortStart = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	marchShortEnd   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	marchFollowEnd  = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
)

// newMarchBook 登记旧版本与短期版本，形成“旧版实际止于 3 月 10 日、
// 短期版本占用到 3 月 20 日”的既有状态。
func newMarchBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	oldEnd := marchOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: marchOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	shortEnd := marchShortEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: marchShortStart, End: &shortEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// assertMarchBaseUnchanged 校验失败登记没有延长或恢复前两版的有效期，
// 也没有改写原来的替代关系。
func assertMarchBaseUnchanged(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("失败登记后应仍是两个版本，got %d", len(views))
	}
	old, short := views[0], views[1]
	if old.VersionID != "seat-v1" || short.VersionID != "seat-v2" {
		t.Fatalf("版本顺序异常: %q, %q", old.VersionID, short.VersionID)
	}
	if old.End == nil || !old.End.Equal(marchOldEnd) {
		t.Fatalf("旧版登记结束被改写: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(marchShortStart) {
		t.Fatalf("旧版实际结束应仍是交接点 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系被改写: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}
	if short.End == nil || !short.End.Equal(marchShortEnd) ||
		short.EffectiveEnd == nil || !short.EffectiveEnd.Equal(marchShortEnd) {
		t.Fatalf("短期版本边界被改写: 登记结束=%v 实际结束=%v", short.End, short.EffectiveEnd)
	}
	if short.Replaces != "seat-v1" || short.SupersededBy != "" {
		t.Fatalf("短期版本替代关系被改写: Replaces=%q SupersededBy=%q",
			short.Replaces, short.SupersededBy)
	}
}

// 提前到 3 月 19 日开始的后续登记与短期版本实际有效期重叠，必须返回现有的
// ErrOverlap；失败不留任何痕迹。把开始改到 3 月 20 日、沿用同一版本标识即可成功。
func TestFollowUpOverlapJudgedByEffectiveInterval(t *testing.T) {
	b, _ := newMarchBook(t, marchShortStart)

	tooEarly := time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC)
	failed := RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: tooEarly, End: &marchFollowEnd,
	}
	if err := b.RegisterVersion(failed); !errors.Is(err, ErrOverlap) {
		t.Fatalf("与短期版本实际有效期重叠应返回 ErrOverlap, got %v", err)
	}

	// 失败后：前两版时间边界和替代关系与操作前一致，失败版本不在查询结果中。
	assertMarchBaseUnchanged(t, b)

	// 开始改到 3 月 20 日零点（短期版本结束时刻，不含），沿用同一版本标识成功：
	// 未成功的登记不能把标识当成已被占用。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: marchShortEnd, End: &marchFollowEnd,
	}); err != nil {
		t.Fatalf("相邻于实际有效区间的后续登记应成功: %v", err)
	}

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("want 3 versions, got %d", len(views))
	}
	// 三个版本按生效时间排列。
	got := []string{views[0].VersionID, views[1].VersionID, views[2].VersionID}
	want := []string{"seat-v1", "seat-v2", "seat-v3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("版本顺序=%v, want %v", got, want)
		}
	}
	old, short, follow := views[0], views[1], views[2]

	// 前两版边界与替代关系保持操作前状态：后续登记不能延长或恢复它们。
	if old.End == nil || !old.End.Equal(marchOldEnd) {
		t.Fatalf("旧版登记结束应仍是月底: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(marchShortStart) {
		t.Fatalf("旧版实际结束应仍是 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版被替代关系: %q", old.SupersededBy)
	}
	if short.End == nil || !short.End.Equal(marchShortEnd) ||
		short.EffectiveEnd == nil || !short.EffectiveEnd.Equal(marchShortEnd) {
		t.Fatalf("短期版本结束应仍是 3 月 20 日: 登记=%v 实际=%v",
			short.End, short.EffectiveEnd)
	}
	if short.Replaces != "seat-v1" {
		t.Fatalf("短期版本替代来源: %q", short.Replaces)
	}

	// 后续版本：未填写替代来源，实际区间即登记区间。
	if follow.UnitPrice != 200 || follow.Replaces != "" || follow.SupersededBy != "" {
		t.Fatalf("后续版本登记信息异常: %+v", follow)
	}
	if !follow.Start.Equal(marchShortEnd) {
		t.Fatalf("后续版本开始: %v", follow.Start)
	}
	if follow.End == nil || !follow.End.Equal(marchFollowEnd) ||
		follow.EffectiveEnd == nil || !follow.EffectiveEnd.Equal(marchFollowEnd) {
		t.Fatalf("后续版本结束: 登记=%v 实际=%v, want 3 月 25 日",
			follow.End, follow.EffectiveEnd)
	}
}

// 在短期版本结束时刻（不含）3 月 20 日零点报价：旧版本、短期版本都应以
// version_expired 拒绝，后续版本按 200 分确认。拒绝是正常受理，不返回错误，
// 金额为零，且保留调用者指定的费率项与版本，不自动切换。
func TestFollowUpBoundaryQuotesAtShortEnd(t *testing.T) {
	b, setNow := newMarchBook(t, marchShortEnd)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: marchShortEnd, End: &marchFollowEnd,
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}

	setNow(marchShortEnd) // 2026-03-20 00:00：结束时刻不含在有效期内
	cases := []struct {
		name      string
		requestID string
		versionID string
		confirmed bool
		reason    RejectReason
		unitPrice int64
		total     int64
	}{
		{"旧版本已失效", "q-old-at-follow-start", "seat-v1", false, ReasonVersionExpired, 0, 0},
		{"短期版本已失效", "q-short-at-follow-start", "seat-v2", false, ReasonVersionExpired, 0, 0},
		{"后续版本确认", "q-follow-at-start", "seat-v3", true, ReasonNone, 200, 800},
	}
	for _, c := range cases {
		req := QuoteRequest{
			RequestID: c.requestID, ItemID: "seat",
			VersionID: c.versionID, Quantity: 4,
		}
		out, err := b.Quote(req)
		if err != nil {
			t.Fatalf("%s: 拒绝/确认都是正常受理，err 应为空, got %v", c.name, err)
		}
		if out.Confirmed != c.confirmed {
			t.Fatalf("%s: Confirmed=%v, want %v (%+v)", c.name, out.Confirmed, c.confirmed, out)
		}
		if out.Reason != c.reason {
			t.Fatalf("%s: 原因=%q, want %q", c.name, out.Reason, c.reason)
		}
		if out.UnitPrice != c.unitPrice || out.Total != c.total {
			t.Fatalf("%s: 单价/总价=%d/%d, want %d/%d",
				c.name, out.UnitPrice, out.Total, c.unitPrice, c.total)
		}
		// 结果必须保留调用者指定的费率项、版本和数量，不能替调用者切换版本。
		if out.Request != req {
			t.Fatalf("%s: 请求来源未保留: %+v vs %+v", c.name, out.Request, req)
		}
		if !out.AcceptedAt.Equal(marchShortEnd) {
			t.Fatalf("%s: 受理时刻=%v, want %v", c.name, out.AcceptedAt, marchShortEnd)
		}
	}
}

// 后续版本自己的结束时刻 3 月 25 日零点同样不含在有效期内：此刻引用它应被拒绝。
func TestFollowUpExpiresAtOwnEnd(t *testing.T) {
	b, setNow := newMarchBook(t, marchFollowEnd)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: marchShortEnd, End: &marchFollowEnd,
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}

	setNow(marchFollowEnd)
	req := QuoteRequest{
		RequestID: "q-follow-at-end", ItemID: "seat", VersionID: "seat-v3", Quantity: 4,
	}
	out, err := b.Quote(req)
	if err != nil {
		t.Fatalf("拒绝是正常受理，err 应为空: %v", err)
	}
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("结束时刻不含在有效期内，应 version_expired 拒绝: %+v", out)
	}
	if out.UnitPrice != 0 || out.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: %d/%d", out.UnitPrice, out.Total)
	}
	if out.Request != req {
		t.Fatalf("拒绝结果必须保留原请求来源: %+v vs %+v", out.Request, req)
	}
}
