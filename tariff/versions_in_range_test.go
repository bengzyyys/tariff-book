package tariff

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// VersionsInRange 场景的固定时间线（UTC，结束时刻均不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00
//	新版 seat-v2：单价 180 分，2026-03-10 00:00 起替代旧版，2026-03-20 00:00 结束
//
// 查询时旧版的实际有效区间已被截断到 3 月 10 日。
var (
	rngV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rngV1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	rngV2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	rngV2End   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
)

func newRangeBook(t *testing.T) *Book {
	t.Helper()
	b := NewBook()
	v1End := rngV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: rngV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	v2End := rngV2End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: rngV2Start, End: &v2End, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b
}

func rangeIDs(views []VersionView) []string {
	ids := make([]string, len(views))
	for i, v := range views {
		ids[i] = v.VersionID
	}
	return ids
}

func equalIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// 题目示例：查看 3 月 9 日至 3 月 11 日应得到旧版和新版；
// 查看 3 月 10 日至 3 月 25 日只得到新版；查看 3 月 20 日至 3 月 25 日为空。
func TestVersionsInRangeSpecExample(t *testing.T) {
	b := newRangeBook(t)

	got, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("3 月 9 日至 11 日不应报错: %v", err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
		t.Fatalf("3 月 9 日至 11 日应依次得到旧版和新版, got %v", ids)
	}

	got, err = b.VersionsInRange("seat", rngV2Start,
		time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("3 月 10 日至 25 日不应报错: %v", err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v2") {
		t.Fatalf("3 月 10 日至 25 日应只得到新版, got %v", ids)
	}

	got, err = b.VersionsInRange("seat", rngV2End,
		time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("3 月 20 日至 25 日不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("3 月 20 日至 25 日应为空列表, got %v", rangeIDs(got))
	}
}

// 边界：范围含开始时刻、不含结束时刻；版本与范围仅在边界处相接不算命中。
func TestVersionsInRangeBoundaries(t *testing.T) {
	b := newRangeBook(t)

	cases := []struct {
		name string
		from time.Time
		to   time.Time
		want []string
	}{
		// 范围结束即新版开始：只与旧版相交，新版只是相接。
		{"范围结束即新版开始", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), rngV2Start, []string{"seat-v1"}},
		// 范围开始即新版开始：新版命中（开始时刻含）。
		{"范围开始即新版开始", rngV2Start, time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), []string{"seat-v2"}},
		// 范围开始即新版结束：新版只是相接，不命中。
		{"范围开始即新版结束", rngV2End, time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC), []string{}},
		// 范围结束即新版结束：新版命中（结束时刻不含，但区间内仍有生效时间）。
		{"范围结束即新版结束", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), rngV2End, []string{"seat-v2"}},
		// 范围结束即旧版开始：旧版只是相接，不命中。
		{"范围结束即旧版开始", time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC), rngV1Start, []string{}},
		// 范围开始即旧版开始：旧版命中。
		{"范围开始即旧版开始", rngV1Start, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), []string{"seat-v1"}},
		// 交接点本身落在范围内：旧版实际区间止于交接点（不含），新版从交接点起（含），
		// 范围跨过交接点时两版都命中。
		{"范围跨过交接点", rngV2Start.Add(-time.Nanosecond), rngV2Start.Add(time.Nanosecond), []string{"seat-v1", "seat-v2"}},
		// 单纳秒范围恰在交接点：只命中新版。
		{"交接点上的单纳秒范围", rngV2Start, rngV2Start.Add(time.Nanosecond), []string{"seat-v2"}},
		// 单纳秒范围恰在新版结束点：无命中。
		{"新版结束点上的单纳秒范围", rngV2End, rngV2End.Add(time.Nanosecond), []string{}},
	}
	for _, c := range cases {
		got, err := b.VersionsInRange("seat", c.from, c.to)
		if err != nil {
			t.Fatalf("%s: 不应报错, got %v", c.name, err)
		}
		if ids := rangeIDs(got); !equalIDs(ids, c.want...) {
			t.Fatalf("%s: got %v, want %v", c.name, ids, c.want)
		}
	}
}

// 被替代的旧版按截短后的实际结束参与判断：登记结束（3 月 31 日）落在范围内
// 也不能列入；新版到期后旧版不恢复，范围完全落在空档时为空。
func TestVersionsInRangeTruncatedOldVersion(t *testing.T) {
	b := newRangeBook(t)

	// 范围完全在旧版登记结束之前、但晚于交接点：旧版登记结束仍在范围内，
	// 实际结束却是 3 月 10 日，只能列出新版。
	got, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v2") {
		t.Fatalf("旧版登记结束在范围内也不能列入已截短的旧版: %v", ids)
	}

	// 新版到期后：范围落在两版之后的空档，旧版不恢复，列表为空。
	got, err = b.VersionsInRange("seat",
		time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC),
		rngV1End)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("新版到期后旧版不能恢复: %v", rangeIDs(got))
	}
}

// 持续有效（无结束边界）的版本按没有结束边界处理：任意未来范围都命中。
func TestVersionsInRangeOpenEnded(t *testing.T) {
	b := NewBook()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "open", VersionID: "v1", UnitPrice: 7,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := b.VersionsInRange("open",
		time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2099, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "v1") {
		t.Fatalf("持续有效版本应命中任意未来范围: %v", ids)
	}
	if got[0].EffectiveEnd != nil || got[0].End != nil {
		t.Fatalf("持续有效版本视图不应出现结束边界: %+v", got[0])
	}

	// 范围结束即版本开始：只是相接，不命中。
	got, err = b.VersionsInRange("open",
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("范围结束即版本开始不应命中: %v", rangeIDs(got))
	}
}

// 结束不晚于开始时一律返回可明确识别的范围错误，不返回版本列表；
// 即使费率项从未登记过，范围错误也优先。
func TestVersionsInRangeInvalidRange(t *testing.T) {
	b := newRangeBook(t)

	at := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		from time.Time
		to   time.Time
	}{
		{"结束等于开始", at, at},
		{"结束早于开始", at, at.Add(-time.Nanosecond)},
		{"结束远早于开始", rngV2End, rngV1Start},
	} {
		got, err := b.VersionsInRange("seat", c.from, c.to)
		if !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("%s: want ErrInvalidRange, got %v", c.name, err)
		}
		if got != nil {
			t.Fatalf("%s: 范围错误不应返回版本列表, got %v", c.name, rangeIDs(got))
		}
		// 同一瞬间的不同时区表示结论一致。
		got, err = b.VersionsInRange("seat", c.from.In(east8), c.to.In(east8))
		if !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("%s（东八区表示）: want ErrInvalidRange, got %v", c.name, err)
		}
		if got != nil {
			t.Fatalf("%s（东八区表示）: 范围错误不应返回版本列表, got %v", c.name, rangeIDs(got))
		}
	}

	// 费率项不存在时，范围错误同样优先于未找到错误。
	_, err := b.VersionsInRange("never-seen", at, at)
	if !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("项不存在时范围错误仍应优先: got %v", err)
	}
}

// 范围合法但费率项从未登记过版本时，沿用现有的费率项未找到错误；
// 费率项存在但范围内没有命中版本时，返回空列表且不报错。
func TestVersionsInRangeMissingItemVsEmptyResult(t *testing.T) {
	b := newRangeBook(t)

	_, err := b.VersionsInRange("never-seen", rngV1Start, rngV2Start)
	if !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("从未登记的费率项应返回 ErrItemNotFound, got %v", err)
	}
	if errors.Is(err, ErrInvalidRange) {
		t.Fatalf("合法范围不能被误报成范围错误: %v", err)
	}

	got, err := b.VersionsInRange("seat",
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("项存在但无命中不应报错: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("项存在但无命中应返回空列表: %+v", got)
	}
	if errors.Is(err, ErrItemNotFound) {
		t.Fatalf("无命中不能被误报成费率项不存在: %v", err)
	}
}

// 空档不补入相邻版本：范围覆盖两段版本之间的空档时，只列出实际相交的版本。
func TestVersionsInRangeGapNotFilled(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	// 两段不涉及替代的区间，中间留 3 月 10 日至 3 月 20 日的空档。
	must(RegisterRequest{
		ItemID: "gap", VersionID: "g1", UnitPrice: 100,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		End:   ptrTime(time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)),
	})
	must(RegisterRequest{
		ItemID: "gap", VersionID: "g2", UnitPrice: 200,
		Start: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		End:   ptrTime(time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)),
	})

	// 范围跨过整个空档：两段都命中，空档本身不产生任何版本。
	got, err := b.VersionsInRange("gap",
		time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "g1", "g2") {
		t.Fatalf("跨过空档的范围应只列出实际相交的两版: %v", ids)
	}

	// 范围完全落在空档内：空列表，不拿相邻版本补位。
	got, err = b.VersionsInRange("gap",
		time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("空档内不应补入相邻版本: %v", rangeIDs(got))
	}
}

// 返回列表沿用现有版本视图：保留登记起止、实际有效区间和替代关系，
// 不把版本边界改成查询范围的边界；按实际生效起点从早到晚排列，每版只出现一次。
func TestVersionsInRangeViewFieldsAndOrdering(t *testing.T) {
	b := newRangeBook(t)

	got, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应命中两版, got %v", rangeIDs(got))
	}

	old, nv := got[0], got[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("应按实际生效起点从早到晚排列: %v", rangeIDs(got))
	}

	// 旧版保留登记边界与截短后的实际边界，不被查询范围 [3-09, 3-11) 改写。
	if old.ItemID != "seat" || old.UnitPrice != 150 {
		t.Fatalf("旧版标识/单价异常: %+v", old)
	}
	if !old.Start.Equal(rngV1Start) || !old.EffectiveStart.Equal(rngV1Start) {
		t.Fatalf("旧版开始边界不应被改成范围开始: %+v", old)
	}
	if old.End == nil || !old.End.Equal(rngV1End) {
		t.Fatalf("旧版应保留登记结束 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(rngV2Start) {
		t.Fatalf("旧版实际结束应显示交接点 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}

	if nv.UnitPrice != 180 || !nv.Start.Equal(rngV2Start) || !nv.EffectiveStart.Equal(rngV2Start) {
		t.Fatalf("新版视图异常: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(rngV2End) || nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(rngV2End) {
		t.Fatalf("新版结束边界不应被改成范围结束: %+v", nv)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q", nv.Replaces, nv.SupersededBy)
	}

	// 无论范围与版本相交多少次，每个版本只出现一次：大范围包含两版完整区间。
	got, err = b.VersionsInRange("seat",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
		t.Fatalf("大范围应各列一次、按起点排序: %v", ids)
	}
}

// 同一瞬间用不同时区表示，范围查询结论一致。
func TestVersionsInRangeSameInstantAnyTimezone(t *testing.T) {
	b := newRangeBook(t)

	from := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)
	reprs := [][2]time.Time{
		{from, to},
		{from.In(east8), to.In(east8)},
		{from.In(time.FixedZone("UTC-5", -5*3600)), to.In(time.FixedZone("UTC-5", -5*3600))},
	}
	for i, r := range reprs {
		got, err := b.VersionsInRange("seat", r[0], r[1])
		if err != nil {
			t.Fatalf("表示%d: %v", i, err)
		}
		if ids := rangeIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
			t.Fatalf("表示%d: got %v, want [seat-v1 seat-v2]", i, ids)
		}
	}
}

// 选择只在指定费率项内进行：其他费率项即使有同名版本也不参与。
func TestVersionsInRangeScopedToItem(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		End:   ptrTime(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)),
	})
	must(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 300,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	})

	got, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ItemID != "seat" || got[0].UnitPrice != 150 {
		t.Fatalf("seat 的范围查询不能混入 room 的同名版本: %+v", got)
	}

	got, err = b.VersionsInRange("room",
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ItemID != "room" || got[0].UnitPrice != 300 {
		t.Fatalf("room 的范围查询不能混入 seat 的同名版本: %+v", got)
	}
}

// 范围可以在过去或未来，与账本受理时钟无关。
func TestVersionsInRangeIgnoresLedgerClock(t *testing.T) {
	now, setNow := fixedClock(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	b := NewBook(WithClock(now))
	v1End := rngV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: rngV1Start, End: &v1End,
	}); err != nil {
		t.Fatal(err)
	}

	// 时钟停在 6 月，查过去的范围仍按登记区间回答。
	got, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1") {
		t.Fatalf("时钟在未来不影响查过去: %v", ids)
	}

	// 时钟拨回登记之前，查未来的范围结论不变。
	setNow(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	got, err = b.VersionsInRange("seat",
		time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1") {
		t.Fatalf("时钟在过去不影响查未来: %v", ids)
	}
}

// 选择依据是查询时账本已登记的版本：补登过去生效的替代版本后，
// 同一历史范围的回答按当前登记的实际区间变化。
func TestVersionsInRangeReflectsRetroactiveRegistration(t *testing.T) {
	b := NewBook()
	v1End := rngV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: rngV1Start, End: &v1End,
	}); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	got, err := b.VersionsInRange("seat", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1") {
		t.Fatalf("补登前该范围只应命中旧版: %v", ids)
	}

	// 补登 3 月 10 日起生效的替代版本后，旧版实际区间被截断，同一范围命中两版。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: rngV2Start, Replaces: "seat-v1",
	}); err != nil {
		t.Fatal(err)
	}
	got, err = b.VersionsInRange("seat", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
		t.Fatalf("补登后同一范围应按当前登记区间命中两版: %v", ids)
	}
}

// 查询是只读操作：不受理报价、不占用请求标识；
// 返回视图是独立副本，改写其中的结束时间不影响后续查询和报价。
func TestVersionsInRangeReadOnlyAndIsolated(t *testing.T) {
	b, setNow := newEffectiveVersionBook(t, effV1Start)

	got, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("前提：应命中两版, got %v", rangeIDs(got))
	}

	// 调用方改写返回视图中的结束时间。
	*got[0].End = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	*got[0].EffectiveEnd = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	*got[1].End = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	*got[1].EffectiveEnd = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	// 再次范围查询：边界仍是账本原值，且与上次返回不共享指针。
	again, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if again[0].End == nil || !again[0].End.Equal(effV1End) {
		t.Fatalf("登记结束被查询结果的修改改写: %v", again[0].End)
	}
	if again[0].EffectiveEnd == nil || !again[0].EffectiveEnd.Equal(effV2Start) {
		t.Fatalf("实际结束被查询结果的修改改写: %v", again[0].EffectiveEnd)
	}
	if got[0].End == again[0].End || got[0].EffectiveEnd == again[0].EffectiveEnd {
		t.Fatal("两次范围查询共享了结束时间指针")
	}

	// 其他查询入口同样未被污染。
	view, err := b.EffectiveVersionAt("seat", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || view.EffectiveEnd == nil || !view.EffectiveEnd.Equal(effV2Start) {
		t.Fatalf("EffectiveVersionAt 被范围查询结果的修改污染: %+v (%v)", view, err)
	}

	// 报价仍按账本真实区间处理：交接点起旧版失效、新版有效。
	setNow(effV2Start)
	out, _ := b.Quote(QuoteRequest{RequestID: "q-old", ItemID: "seat", VersionID: "seat-v1", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("修改范围查询结果不能影响后续报价依据: %+v", out)
	}
	out, _ = b.Quote(QuoteRequest{RequestID: "q-new", ItemID: "seat", VersionID: "seat-v2", Quantity: 1})
	if !out.Confirmed {
		t.Fatalf("新版在交接点应可报价: %+v", out)
	}

	// 范围查询本身不产生任何报价记录、不占用请求标识。
	if _, err := b.Lookup("range-query-check"); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("只读查询不应产生报价记录: %v", err)
	}
	fresh, err := b.Quote(QuoteRequest{RequestID: "range-query-check", ItemID: "seat", VersionID: "seat-v2", Quantity: 2})
	if err != nil || !fresh.Confirmed || fresh.Total != 360 {
		t.Fatalf("范围查询不应占用请求标识或影响首次受理: %+v (%v)", fresh, err)
	}
}

// 并发只读范围查询应始终给出一致结论。
func TestVersionsInRangeConcurrentReads(t *testing.T) {
	b := newRangeBook(t)

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := b.VersionsInRange("seat",
				time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC))
			if err != nil {
				errs <- fmt.Errorf("查询 %d: %w", i, err)
				return
			}
			if ids := rangeIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
				errs <- fmt.Errorf("查询 %d 结果异常: %v", i, ids)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
