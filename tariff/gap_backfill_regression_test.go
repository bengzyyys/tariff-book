package tariff

import (
	"errors"
	"testing"
	"time"
)

// 同一费率项已有前后两段有效期、中间存在空档时，可以补登记一个
// 不替代任何旧版的版本恰好填满空档。这是该既有行为的回归测试，
// 保护区间判断不受登记先后影响，也不把两侧端点相接误判为重叠。
//
// 时间线（UTC，结束时刻均不含）：
//
//	seat-v1：单价 150 分，2026-03-01 00:00 至 2026-03-10 00:00，不填写替代来源
//	seat-v3：单价 200 分，2026-03-20 00:00 起持续有效，不填写替代来源（最先登记）
//	seat-v2：单价 180 分，2026-03-10 00:00 至 2026-03-20 00:00，补登记填满空档
//
// v3 先于 v1 登记：版本列表必须按生效时刻排列（v1、v2、v3），与登记顺序无关。
var (
	gapV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	gapV1End   = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	gapV3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	gapMid     = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
)

// newGapBook 按“先 v3 后 v1”的顺序登记两段互不相邻的有效期，
// 中间留下 3 月 10 日至 3 月 20 日的空档；两版都不填写替代来源。
func newGapBook(t *testing.T) *Book {
	t.Helper()
	b := NewBook()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: gapV3Start,
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}
	v1End := gapV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: gapV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	return b
}

// assertGapBaseUnchanged 校验只有 v1、v3 的既有状态：列表按生效时刻
// 排列（先登记的 v3 排在后面），两版的登记边界、实际有效区间与
// 替代关系都保持登记时的样子。
func assertGapBaseUnchanged(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("应只有 v1、v3 两个版本，got %d", len(views))
	}
	// 按生效时刻排列，不是登记顺序（v3 先登记但排在后面）。
	if views[0].VersionID != "seat-v1" || views[1].VersionID != "seat-v3" {
		t.Fatalf("版本顺序=%q, %q, want seat-v1, seat-v3",
			views[0].VersionID, views[1].VersionID)
	}
	v1, v3 := views[0], views[1]
	if v1.UnitPrice != 150 || !v1.Start.Equal(gapV1Start) ||
		v1.End == nil || !v1.End.Equal(gapV1End) {
		t.Fatalf("v1 登记信息异常: %+v", v1)
	}
	if !v1.EffectiveStart.Equal(gapV1Start) ||
		v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(gapV1End) {
		t.Fatalf("v1 实际有效区间异常: %v ~ %v", v1.EffectiveStart, v1.EffectiveEnd)
	}
	if v1.Replaces != "" || v1.SupersededBy != "" {
		t.Fatalf("v1 替代关系应为空: Replaces=%q SupersededBy=%q", v1.Replaces, v1.SupersededBy)
	}
	if v3.UnitPrice != 200 || !v3.Start.Equal(gapV3Start) || v3.End != nil {
		t.Fatalf("v3 登记信息异常: %+v", v3)
	}
	if !v3.EffectiveStart.Equal(gapV3Start) || v3.EffectiveEnd != nil {
		t.Fatalf("v3 实际有效区间异常: %v ~ %v", v3.EffectiveStart, v3.EffectiveEnd)
	}
	if v3.Replaces != "" || v3.SupersededBy != "" {
		t.Fatalf("v3 替代关系应为空: Replaces=%q SupersededBy=%q", v3.Replaces, v3.SupersededBy)
	}
}

// 补登记前：空档中的 3 月 15 日按时刻查询返回 ErrNoEffectiveVersion，
// 列表按生效时刻列为 v1、v3，而不是登记顺序 v3、v1。
func TestGapBeforeBackfill(t *testing.T) {
	b := newGapBook(t)

	if _, err := b.EffectiveVersionAt("seat", gapMid); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("空档中的查询应返回 ErrNoEffectiveVersion, got %v", err)
	}
	assertGapBaseUnchanged(t, b)
}

// 补登记 v2 恰好填满空档：登记成功，列表变为 v1、v2、v3，三版登记的
// 起止时刻与实际有效区间一致，替代关系都为空；v1 的结束与 v3 的开始
// 不得为了容纳 v2 而移动。空档时刻归 v2，两侧端点各归其主。
func TestGapBackfillFillsGap(t *testing.T) {
	b := newGapBook(t)

	v2End := gapV3Start
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: gapV1End, End: &v2End,
	}); err != nil {
		t.Fatalf("恰好填满空档的补登记应成功: %v", err)
	}

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("want 3 versions, got %d", len(views))
	}
	got := []string{views[0].VersionID, views[1].VersionID, views[2].VersionID}
	want := []string{"seat-v1", "seat-v2", "seat-v3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("版本顺序=%v, want %v", got, want)
		}
	}
	v1, v2, v3 := views[0], views[1], views[2]

	// 三版登记的起止时刻与实际有效区间一致，替代关系都保持为空。
	if v1.UnitPrice != 150 || !v1.Start.Equal(gapV1Start) ||
		v1.End == nil || !v1.End.Equal(gapV1End) ||
		!v1.EffectiveStart.Equal(gapV1Start) ||
		v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(gapV1End) {
		t.Fatalf("v1 边界被移动: %+v", v1)
	}
	if v2.UnitPrice != 180 || !v2.Start.Equal(gapV1End) ||
		v2.End == nil || !v2.End.Equal(gapV3Start) ||
		!v2.EffectiveStart.Equal(gapV1End) ||
		v2.EffectiveEnd == nil || !v2.EffectiveEnd.Equal(gapV3Start) {
		t.Fatalf("v2 登记区间与实际有效区间应一致: %+v", v2)
	}
	if v3.UnitPrice != 200 || !v3.Start.Equal(gapV3Start) || v3.End != nil ||
		!v3.EffectiveStart.Equal(gapV3Start) || v3.EffectiveEnd != nil {
		t.Fatalf("v3 边界被移动: %+v", v3)
	}
	for _, v := range views {
		if v.Replaces != "" || v.SupersededBy != "" {
			t.Fatalf("%s 替代关系应为空: Replaces=%q SupersededBy=%q",
				v.VersionID, v.Replaces, v.SupersededBy)
		}
	}

	// 空档与两侧端点的归属：3 月 10 日零点归 v2，3 月 20 日零点归 v3，
	// 端点前一纳秒仍分别归 v1 和 v2。
	cases := []struct {
		name      string
		at        time.Time
		versionID string
		unitPrice int64
	}{
		{"v1 结束前一纳秒", gapV1End.Add(-time.Nanosecond), "seat-v1", 150},
		{"v2 开始时刻", gapV1End, "seat-v2", 180},
		{"原空档中间", gapMid, "seat-v2", 180},
		{"v3 开始前一纳秒", gapV3Start.Add(-time.Nanosecond), "seat-v2", 180},
		{"v3 开始时刻", gapV3Start, "seat-v3", 200},
	}
	for _, c := range cases {
		view, err := b.EffectiveVersionAt("seat", c.at)
		if err != nil {
			t.Fatalf("%s (%v): %v", c.name, c.at, err)
		}
		if view.VersionID != c.versionID || view.UnitPrice != c.unitPrice {
			t.Fatalf("%s (%v): got %s/%d 分, want %s/%d 分",
				c.name, c.at, view.VersionID, view.UnitPrice, c.versionID, c.unitPrice)
		}
	}

	// v3 持续有效：实际结束时间仍为空。
	view, err := b.EffectiveVersionAt("seat", gapV3Start)
	if err != nil {
		t.Fatal(err)
	}
	if view.EffectiveEnd != nil {
		t.Fatalf("v3 应持续有效，实际结束应为空: %v", view.EffectiveEnd)
	}
}

// 补登记同样保护两侧的重叠失败：v2 起点比 3 月 10 日零点早一纳秒，
// 或终点比 3 月 20 日零点晚一纳秒，其余内容合法，都返回既有的
// ErrOverlap；失败不留痕迹，修正为恰好填满空档后同一标识仍能登记成功。
func TestGapBackfillOverlapRejected(t *testing.T) {
	cases := []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{"起点早一纳秒与 v1 重叠", gapV1End.Add(-time.Nanosecond), gapV3Start},
		{"终点晚一纳秒与 v3 重叠", gapV1End, gapV3Start.Add(time.Nanosecond)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newGapBook(t)

			end := c.end
			failed := RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: c.start, End: &end,
			}
			if err := b.RegisterVersion(failed); !errors.Is(err, ErrOverlap) {
				t.Fatalf("应返回 ErrOverlap, got %v", err)
			}

			// 失败后：列表仍只有原来两版，单价、起止时刻与关系不变。
			assertGapBaseUnchanged(t, b)
			// 空档中的查询仍返回 ErrNoEffectiveVersion。
			if _, err := b.EffectiveVersionAt("seat", gapMid); !errors.Is(err, ErrNoEffectiveVersion) {
				t.Fatalf("失败登记后空档查询仍应返回 ErrNoEffectiveVersion, got %v", err)
			}

			// 修正为恰好填满空档后，同一 v2 标识仍能登记成功：
			// 未成功的登记不能把标识当成已被占用。
			okEnd := gapV3Start
			if err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: gapV1End, End: &okEnd,
			}); err != nil {
				t.Fatalf("修正后的补登记应成功: %v", err)
			}
			view, err := b.EffectiveVersionAt("seat", gapMid)
			if err != nil {
				t.Fatal(err)
			}
			if view.VersionID != "seat-v2" || view.UnitPrice != 180 {
				t.Fatalf("空档应归 v2/180 分: got %s/%d 分", view.VersionID, view.UnitPrice)
			}
		})
	}
}
