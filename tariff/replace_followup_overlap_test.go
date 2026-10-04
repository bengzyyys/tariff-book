package tariff

import (
	"errors"
	"testing"
	"time"
)

// 替代旧版本的新版本，即使交接点合法，也不能占用同一费率项下
// 另一个已登记版本（与本次替代无关的后续版本）的有效时间。
// 这是该既有行为的回归测试。
//
// 时间线（UTC 零点，结束时刻均不含）：
//
//	旧版本 seat-v1：单价 150 分，2026-03-01 00:00 生效，登记结束 2026-03-31 00:00
//	后续版本 seat-v2：单价 200 分，2026-04-01 00:00 起持续有效，与旧版本无替代关系
//	新版本 seat-v3：单价 180 分，2026-03-10 00:00 起替代旧版本
//
// 新版本不填结束时间时会延伸进后续版本的有效期，必须返回 ErrOverlap；
// 结束时间晚于 4 月 1 日零点（哪怕只超出一纳秒）同样属于重叠，
// 不能因为已指定被替代版本就放行。结束时间恰好是 4 月 1 日零点时
// 与后续版本端点相接，应当成功。
var (
	repOldStart  = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	repOldEnd    = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	repHandoff   = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	repNextStart = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
)

// newReplaceChainBook 登记旧版本与后续版本，两者没有替代关系。
// 登记与查询不读取时钟，结论与运行当天的真实日期无关。
func newReplaceChainBook(t *testing.T) *Book {
	t.Helper()
	b := NewBook()
	oldEnd := repOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: repOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 200,
		Start: repNextStart,
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b
}

// assertReplacePairUnchanged 校验失败的登记没有留下任何痕迹：
// 仍只有原来的两个版本，旧版本没有提前止于交接点、也没有显示被
// 失败的新版本替代，后续版本保持原来的持续有效区间。
func assertReplacePairUnchanged(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("失败登记后应仍是两个版本，got %d", len(views))
	}
	old, next := views[0], views[1]
	if old.VersionID != "seat-v1" || next.VersionID != "seat-v2" {
		t.Fatalf("版本顺序异常: %q, %q", old.VersionID, next.VersionID)
	}
	if old.End == nil || !old.End.Equal(repOldEnd) {
		t.Fatalf("旧版登记结束被改写: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(repOldEnd) {
		t.Fatalf("旧版实际结束不能提前到交接点，应仍是 3 月 31 日: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "" {
		t.Fatalf("旧版不能显示被失败的新版本替代: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}
	if !next.Start.Equal(repNextStart) || next.End != nil || next.EffectiveEnd != nil {
		t.Fatalf("后续版本应保持 4 月 1 日起持续有效: %+v", next)
	}
	if next.UnitPrice != 200 || next.Replaces != "" || next.SupersededBy != "" {
		t.Fatalf("后续版本登记信息被改写: %+v", next)
	}
}

// 新版本从 3 月 10 日起替代旧版本，交接点本身合法；但不填结束时间、
// 或结束时间晚于后续版本起点 4 月 1 日零点，都会侵占后续版本的有效期，
// 必须返回已有的 ErrOverlap，且失败不留痕迹。
func TestReplaceMustNotOccupyFollowUpInterval(t *testing.T) {
	endJustAfter := repNextStart.Add(time.Nanosecond)
	endNextDay := repNextStart.Add(24 * time.Hour)
	cases := []struct {
		name string
		end  *time.Time
	}{
		{"不填结束时间", nil},
		{"结束晚于4月1日零点1纳秒", &endJustAfter},
		{"结束晚于4月1日零点一天", &endNextDay},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newReplaceChainBook(t)
			err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v3", UnitPrice: 180,
				Start: repHandoff, End: c.end, Replaces: "seat-v1",
			})
			if !errors.Is(err, ErrOverlap) {
				t.Fatalf("侵占后续版本有效期应返回 ErrOverlap, got %v", err)
			}
			assertReplacePairUnchanged(t, b)
		})
	}
}

// 被拒绝后修正结束时间为 4 月 1 日零点，沿用刚才的版本标识和其余登记内容
// 应当成功：与后续版本只是端点相接，且失败的登记不能把标识当成已被占用。
func TestReplaceRetryWithBoundaryEndAfterOverlap(t *testing.T) {
	b := newReplaceChainBook(t)

	// 先尝试不填结束时间的登记：截短旧版本合法，但会延伸进后续版本有效期。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 180,
		Start: repHandoff, Replaces: "seat-v1",
	}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("不填结束时间应返回 ErrOverlap, got %v", err)
	}
	assertReplacePairUnchanged(t, b)

	// 修正结束时间为后续版本起点，沿用同一版本标识重试，应当成功。
	boundaryEnd := repNextStart
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 180,
		Start: repHandoff, End: &boundaryEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("结束时间与后续版本端点相接应成功: %v", err)
	}

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("want 3 versions, got %d", len(views))
	}
	// 三个版本按生效时刻排列：旧版、新版、后续版本。
	got := []string{views[0].VersionID, views[1].VersionID, views[2].VersionID}
	want := []string{"seat-v1", "seat-v3", "seat-v2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("版本顺序=%v, want %v", got, want)
		}
	}
	old, nv, next := views[0], views[1], views[2]

	// 旧版本：登记结束仍是 3 月 31 日，实际有效结束被截短到 3 月 10 日。
	if old.End == nil || !old.End.Equal(repOldEnd) {
		t.Fatalf("旧版登记结束应仍是月底: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(repHandoff) {
		t.Fatalf("旧版实际结束应被截短到 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v3" {
		t.Fatalf("旧版替代关系: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}

	// 新版本：实际有效区间为 3 月 10 日至 4 月 1 日，保留与旧版的替代关系。
	if nv.UnitPrice != 180 || !nv.Start.Equal(repHandoff) {
		t.Fatalf("新版本登记信息异常: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(repNextStart) ||
		nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(repNextStart) {
		t.Fatalf("新版本结束应均为 4 月 1 日: 登记=%v 实际=%v", nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版本替代关系: Replaces=%q SupersededBy=%q", nv.Replaces, nv.SupersededBy)
	}

	// 后续版本：边界和替代关系保持原值。
	if !next.Start.Equal(repNextStart) || next.End != nil || next.EffectiveEnd != nil {
		t.Fatalf("后续版本应保持 4 月 1 日起持续有效: %+v", next)
	}
	if next.UnitPrice != 200 || next.Replaces != "" || next.SupersededBy != "" {
		t.Fatalf("后续版本登记信息被改写: %+v", next)
	}
}
