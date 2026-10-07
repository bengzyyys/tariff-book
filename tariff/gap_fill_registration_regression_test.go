package tariff

import (
	"errors"
	"testing"
	"time"
)

// “前后两段有效期中间有空档时，补登记一个不替代任何旧版的版本”回归保障。
//
// 固定时间线（全部为 2026 年 UTC 时刻，结束时刻不含）：
//
//	seat-v3：单价 200 分，2026-03-20 00:00 起持续有效（不填结束时间）
//	seat-v1：单价 150 分，2026-03-01 00:00 至 2026-03-10 00:00
//	seat-v2：单价 180 分，拟补登记为 2026-03-10 00:00 至 2026-03-20 00:00
//
// v1 与 v3 之间在 [03-10, 03-20) 存在空档。登记时刻意采用“先 v3 后 v1”的
// 先后顺序，以保护：版本列表与生效选择只按实际有效区间、按生效时刻排列，
// 不受登记先后影响；两侧端点恰好相接属于半开区间相接，不算重叠；
// 不填写 Replaces 的补登记不得移动两侧旧版的任何边界，也不得凭空建立替代关系。
var (
	gfV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	gfV1End   = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 也是 v2 的起点（不含 v1）
	gfV2Start = gfV1End
	gfV2End   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // 也是 v3 的起点（不含 v2）
	gfV3Start = gfV2End
	gfGapAt   = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // 补登记前的空档内部
)

// newGapBook 准备“只有 v1、v3，中间空档”的账本。
// 刻意先登记较晚生效的 v3，再登记较早的 v1：补登记前后的一切结论都不得
// 依赖登记先后。两版都不填写替代来源。
func newGapBook(t *testing.T) *Book {
	t.Helper()
	b := NewBook()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: gfV3Start, // 不填结束时间：持续有效
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}
	v1End := gfV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: gfV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	return b
}

// assertGapBaseUnchanged 校验账本仍处于“只有 v1、v3”的补登记前状态：
// 版本按生效时刻（而非登记顺序）列为 v1、v3；两版单价、登记起止与实际有效
// 区间一致且无替代关系；v3 的结束时间仍为空；空档时刻仍无生效版本。
func assertGapBaseUnchanged(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("补登记前/失败回滚后应只有两个版本，got %d: %+v", len(views), views)
	}
	v1, v3 := views[0], views[1]
	if v1.VersionID != "seat-v1" || v3.VersionID != "seat-v3" {
		t.Fatalf("版本应按生效时刻列为 v1、v3（与登记顺序 v3、v1 无关），got %q、%q",
			v1.VersionID, v3.VersionID)
	}

	if v1.UnitPrice != 150 || !v1.Start.Equal(gfV1Start) || !v1.EffectiveStart.Equal(gfV1Start) {
		t.Fatalf("v1 登记起点/单价异常: %+v", v1)
	}
	if v1.End == nil || !v1.End.Equal(gfV1End) ||
		v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(gfV1End) {
		t.Fatalf("v1 的结束不得为容纳补登记而移动: 登记结束=%v 实际结束=%v, want %v",
			v1.End, v1.EffectiveEnd, gfV1End)
	}
	if v1.Replaces != "" || v1.SupersededBy != "" {
		t.Fatalf("v1 不应存在替代关系: Replaces=%q SupersededBy=%q",
			v1.Replaces, v1.SupersededBy)
	}

	if v3.UnitPrice != 200 || !v3.Start.Equal(gfV3Start) || !v3.EffectiveStart.Equal(gfV3Start) {
		t.Fatalf("v3 登记起点/单价异常: %+v", v3)
	}
	if v3.End != nil || v3.EffectiveEnd != nil {
		t.Fatalf("v3 应仍持续有效: 登记结束=%v 实际结束=%v", v3.End, v3.EffectiveEnd)
	}
	if v3.Replaces != "" || v3.SupersededBy != "" {
		t.Fatalf("v3 不应存在替代关系: Replaces=%q SupersededBy=%q",
			v3.Replaces, v3.SupersededBy)
	}

	if _, err := b.EffectiveVersionAt("seat", gfGapAt); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("空档 %s 应返回 ErrNoEffectiveVersion, got %v", gfGapAt.Format(time.RFC3339), err)
	}
}

// 补登记前的基线：尽管 v3 先于 v1 登记，列表仍按生效时刻列为 v1、v3，
// 且 3 月 15 日落在两版之间的空档，按时刻查询返回 ErrNoEffectiveVersion。
func TestGapBaseOrderingByEffectiveTimeAndGapHasNoVersion(t *testing.T) {
	b := newGapBook(t)
	assertGapBaseUnchanged(t, b)
}

// 不填写替代来源、区间恰好填满空档 [03-10, 03-20) 的 v2 必须登记成功：
// 列表变为 v1、v2、v3；三版各自登记的起止与实际有效区间一致、替代关系全空；
// v1 的结束与 v3 的开始都不移动，v3 持续有效的结束时间仍为空；
// 按时刻查询在两个交接点上分别取右侧版本，端点前一纳秒仍取左侧版本。
func TestFillGapWithoutReplacementSucceeds(t *testing.T) {
	b := newGapBook(t)

	v2End := gfV2End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: gfV2Start, End: &v2End, // 不填写 Replaces：不替代任何旧版
	}); err != nil {
		t.Fatalf("恰好填满空档且不替代旧版的登记应成功: %v", err)
	}

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("补登记成功后应有三个版本, got %d: %+v", len(views), views)
	}
	v1, v2, v3 := views[0], views[1], views[2]
	if v1.VersionID != "seat-v1" || v2.VersionID != "seat-v2" || v3.VersionID != "seat-v3" {
		t.Fatalf("版本应按生效时刻列为 v1、v2、v3, got %q、%q、%q",
			v1.VersionID, v2.VersionID, v3.VersionID)
	}
	for i, v := range views {
		if v.Replaces != "" || v.SupersededBy != "" {
			t.Fatalf("第 %d 个版本 %s 不应存在替代关系: Replaces=%q SupersededBy=%q",
				i, v.VersionID, v.Replaces, v.SupersededBy)
		}
		if !v.Start.Equal(v.EffectiveStart) {
			t.Fatalf("%s 登记起点与实际起点应一致: %v vs %v",
				v.VersionID, v.Start, v.EffectiveStart)
		}
	}

	// v1 的边界与补登记前完全一致：没有 Replaces 就不能截断或平移它。
	if v1.UnitPrice != 150 || !v1.Start.Equal(gfV1Start) {
		t.Fatalf("v1 登记信息异常: %+v", v1)
	}
	if v1.End == nil || !v1.End.Equal(gfV1End) ||
		v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(gfV1End) {
		t.Fatalf("v1 结束应仍为 3 月 10 日零点: 登记=%v 实际=%v", v1.End, v1.EffectiveEnd)
	}

	// v2 登记的起止时刻就是它的实际有效区间 [03-10, 03-20)。
	if v2.UnitPrice != 180 {
		t.Fatalf("v2 单价应为 180, got %d", v2.UnitPrice)
	}
	if !v2.Start.Equal(gfV2Start) || v2.End == nil || !v2.End.Equal(gfV2End) ||
		v2.EffectiveEnd == nil || !v2.EffectiveEnd.Equal(gfV2End) {
		t.Fatalf("v2 实际有效区间应为 [03-10, 03-20): 起点=%v 登记结束=%v 实际结束=%v",
			v2.Start, v2.End, v2.EffectiveEnd)
	}

	// v3 的开始不移动，持续有效的结束时间仍为空。
	if v3.UnitPrice != 200 || !v3.Start.Equal(gfV3Start) {
		t.Fatalf("v3 登记信息异常: %+v", v3)
	}
	if v3.End != nil || v3.EffectiveEnd != nil {
		t.Fatalf("v3 的持续有效结束时间应仍为空: 登记=%v 实际=%v", v3.End, v3.EffectiveEnd)
	}

	// 按时刻查询：开始时刻含、结束时刻不含；端点相接处选右侧版本，
	// 端点前一纳秒仍选左侧版本；空档内部现在归 v2；v3 在 3 月 20 日后持续生效。
	cases := []struct {
		name      string
		at        time.Time
		versionID string
		unitPrice int64
	}{
		{"v2 起点前一纳秒仍归 v1", gfV2Start.Add(-time.Nanosecond), "seat-v1", 150},
		{"v2 起点（含）归 v2", gfV2Start, "seat-v2", 180},
		{"原空档 3 月 15 日归 v2", gfGapAt, "seat-v2", 180},
		{"v3 起点前一纳秒仍归 v2", gfV3Start.Add(-time.Nanosecond), "seat-v2", 180},
		{"v3 起点（含）归 v3", gfV3Start, "seat-v3", 200},
		{"3 月 20 日之后 v3 持续有效", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), "seat-v3", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := b.EffectiveVersionAt("seat", c.at)
			if err != nil {
				t.Fatalf("%s: 查询 %s 意外失败: %v", c.name, c.at.Format(time.RFC3339Nano), err)
			}
			if got.VersionID != c.versionID || got.UnitPrice != c.unitPrice {
				t.Fatalf("%s: 查询 %s 得到 %s/%d, want %s/%d",
					c.name, c.at.Format(time.RFC3339Nano),
					got.VersionID, got.UnitPrice, c.versionID, c.unitPrice)
			}
		})
	}
}

// 补登记两侧的重叠都必须沿用既有的 ErrOverlap：起点早一纳秒会侵入 v1 的
// 实际有效期，终点晚一纳秒会侵入持续有效的 v3。失败整次回滚，原两版的
// 单价、起止时刻与关系不变，空档查询仍返回 ErrNoEffectiveVersion；
// 修正为恰好填满空档后，同一 v2 标识仍能登记成功（失败不占用标识）。
func TestFillGapOverlapOneNanosecondEitherSideRejectedThenSameIDSucceeds(t *testing.T) {
	cases := []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{"起点比 3 月 10 日早一纳秒", gfV2Start.Add(-time.Nanosecond), gfV2End},
		{"终点比 3 月 20 日晚一纳秒", gfV2Start, gfV2End.Add(time.Nanosecond)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newGapBook(t)

			end := c.end
			err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: c.start, End: &end,
			})
			if !errors.Is(err, ErrOverlap) {
				t.Fatalf("%s 应返回 ErrOverlap, got %v", c.name, err)
			}

			// 失败整次回滚：仍只有 v1、v3，单价、起止与关系不变，空档仍在。
			assertGapBaseUnchanged(t, b)

			// 修正为恰好填满空档，沿用同一版本标识 seat-v2 登记成功。
			fixedEnd := gfV2End
			if err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: gfV2Start, End: &fixedEnd,
			}); err != nil {
				t.Fatalf("失败登记不应占用标识，恰好填满空档后沿用 seat-v2 应成功: %v", err)
			}

			views, err := b.ItemVersions("seat")
			if err != nil {
				t.Fatal(err)
			}
			if len(views) != 3 {
				t.Fatalf("修正后应有三个版本, got %d: %+v", len(views), views)
			}
			if views[0].VersionID != "seat-v1" || views[1].VersionID != "seat-v2" ||
				views[2].VersionID != "seat-v3" {
				t.Fatalf("修正后版本顺序异常: %q、%q、%q",
					views[0].VersionID, views[1].VersionID, views[2].VersionID)
			}

			// 原空档现在归 v2，两个交接点的归属也符合半开区间约定。
			got, err := b.EffectiveVersionAt("seat", gfGapAt)
			if err != nil {
				t.Fatalf("修正后空档查询应成功: %v", err)
			}
			if got.VersionID != "seat-v2" || got.UnitPrice != 180 {
				t.Fatalf("空档应归 v2/180, got %s/%d", got.VersionID, got.UnitPrice)
			}
			left, err := b.EffectiveVersionAt("seat", gfV2Start.Add(-time.Nanosecond))
			if err != nil || left.VersionID != "seat-v1" {
				t.Fatalf("v2 起点前一纳秒应仍归 v1, got %+v err=%v", left, err)
			}
			atV2Start, err := b.EffectiveVersionAt("seat", gfV2Start)
			if err != nil || atV2Start.VersionID != "seat-v2" {
				t.Fatalf("3 月 10 日零点应归 v2, got %+v err=%v", atV2Start, err)
			}
			beforeV3, err := b.EffectiveVersionAt("seat", gfV3Start.Add(-time.Nanosecond))
			if err != nil || beforeV3.VersionID != "seat-v2" {
				t.Fatalf("3 月 20 日零点前一纳秒应仍归 v2, got %+v err=%v", beforeV3, err)
			}
			atV3Start, err := b.EffectiveVersionAt("seat", gfV3Start)
			if err != nil || atV3Start.VersionID != "seat-v3" {
				t.Fatalf("3 月 20 日零点应归 v3, got %+v err=%v", atV3Start, err)
			}
		})
	}
}
