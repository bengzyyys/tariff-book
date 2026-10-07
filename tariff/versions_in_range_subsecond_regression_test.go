package tariff

import (
	"errors"
	"testing"
	"time"
)

// VersionsInRange 的亚秒级回归场景：生效、替代交接与到期全部落在同一秒内。
// 时间线沿用 subsecond_validity_regression_test.go 中写死的常量
// （全部为 2026-03-01 UTC 零点所在的那一秒，结束时刻不含）：
//
//	旧版 seat-v9：单价 150 分，第 100 毫秒起生效（含），登记结束为第 900 毫秒
//	新版 seat-v10：单价 180 分，第 500 毫秒起替代旧版（含），第 700 毫秒结束（不含）
//
// 查询时旧版的实际有效区间已被截短为 [100ms, 500ms)，登记结束仍是第 900 毫秒。
// 版本标识特意选成字符串顺序与生效先后相反（"seat-v10" < "seat-v9"）：
// 列表必须按实际生效起点排列，不能退化成按标识排序。

// newSubSecondRangeBook 返回已按上述时间线登记旧版与新版的账本。
// 两次登记都必须成功：同一秒内的正长度区间是合法输入，不能被截断到整秒。
func newSubSecondRangeBook(t *testing.T) *Book {
	t.Helper()
	b := NewBook()

	oldEnd := subSecRegEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v9", UnitPrice: 150,
		Start: subSecStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v9: %v", err)
	}
	newEnd := subSecNewEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v10", UnitPrice: 180,
		Start: subSecHand, End: &newEnd, Replaces: "seat-v9",
	}); err != nil {
		t.Fatalf("register seat-v10: %v", err)
	}
	return b
}

// 查看第 300 毫秒到第 600 毫秒：应依次得到旧版、新版，各出现一次。
// 旧版生效更早，即使其标识 "seat-v9" 在字符串顺序中靠后，也必须排在前面。
// 返回的视图保留账本中的完整时间精度与来源，不把边界裁成查询范围的边界。
func TestVersionsInRangeSubSecondSpanningHandoff(t *testing.T) {
	assertSubSecondTimesInSameSecond(t)
	b := newSubSecondRangeBook(t)

	from := subSecBase.Add(300 * time.Millisecond)
	to := subSecBase.Add(600 * time.Millisecond)
	got, err := b.VersionsInRange("seat", from, to)
	if err != nil {
		t.Fatalf("同一秒内的合法范围不应报错: %v", err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v9", "seat-v10") {
		t.Fatalf("应按实际生效起点依次列出旧版、新版（而非按标识排序）, got %v", ids)
	}

	old, nv := got[0], got[1]

	// 旧版：保留第 100 毫秒起点、第 900 毫秒登记结束和第 500 毫秒实际结束，
	// 不被查询范围 [300ms, 600ms) 裁剪；替代关系指向新版。
	if old.ItemID != "seat" || old.UnitPrice != 150 {
		t.Fatalf("旧版标识/单价异常: %+v", old)
	}
	if !old.Start.Equal(subSecStart) || old.Start.Nanosecond() != 100_000_000 ||
		!old.EffectiveStart.Equal(subSecStart) {
		t.Fatalf("旧版开始应为第 100 毫秒且毫秒精度保留，不被裁成范围开始: %+v", old)
	}
	if old.End == nil || !old.End.Equal(subSecRegEnd) || old.End.Nanosecond() != 900_000_000 {
		t.Fatalf("旧版应保留第 900 毫秒登记结束: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(subSecHand) ||
		old.EffectiveEnd.Nanosecond() != 500_000_000 {
		t.Fatalf("旧版实际结束应显示第 500 毫秒交接点: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v10" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}

	// 新版：保留自己的第 500 毫秒起点、第 700 毫秒结束、180 分单价与替代来源。
	if nv.UnitPrice != 180 {
		t.Fatalf("新版单价应保留 180 分, got %d", nv.UnitPrice)
	}
	if !nv.Start.Equal(subSecHand) || nv.Start.Nanosecond() != 500_000_000 ||
		!nv.EffectiveStart.Equal(subSecHand) {
		t.Fatalf("新版开始应为第 500 毫秒且毫秒精度保留: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(subSecNewEnd) || nv.End.Nanosecond() != 700_000_000 ||
		nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(subSecNewEnd) {
		t.Fatalf("新版结束应为第 700 毫秒且毫秒精度保留: 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v9" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q", nv.Replaces, nv.SupersededBy)
	}
}

// 交接点前后的极短范围：范围开始包含在内、结束不包含。
// 从交接点前一纳秒开始、恰好在交接点结束时只列旧版；从交接点开始、
// 到后一纳秒结束时只列新版；跨过交接点的范围列出两版。这些只有几毫秒
// 甚至一纳秒的正长度范围都是合法输入，不能误报范围错误，也不能漏掉
// 只在其中生效了一纳秒的版本。
func TestVersionsInRangeSubSecondHandoffNanosecondRanges(t *testing.T) {
	b := newSubSecondRangeBook(t)

	cases := []struct {
		name string
		from time.Time
		to   time.Time
		want []string
	}{
		{"交接点前一纳秒至交接点-只列旧版",
			subSecHand.Add(-time.Nanosecond), subSecHand, []string{"seat-v9"}},
		{"交接点至后一纳秒-只列新版",
			subSecHand, subSecHand.Add(time.Nanosecond), []string{"seat-v10"}},
		{"跨过交接点的两纳秒范围-两版都列",
			subSecHand.Add(-time.Nanosecond), subSecHand.Add(time.Nanosecond),
			[]string{"seat-v9", "seat-v10"}},
		{"旧版区间内的单纳秒范围",
			subSecBase.Add(300 * time.Millisecond), subSecBase.Add(300*time.Millisecond).Add(time.Nanosecond),
			[]string{"seat-v9"}},
		{"旧版起点上的单纳秒范围",
			subSecStart, subSecStart.Add(time.Nanosecond), []string{"seat-v9"}},
		{"与新版只相交一纳秒的范围",
			subSecNewEnd.Add(-time.Nanosecond), subSecNewEnd, []string{"seat-v10"}},
		{"新版结束点上的单纳秒范围-无命中",
			subSecNewEnd, subSecNewEnd.Add(time.Nanosecond), []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := b.VersionsInRange("seat", c.from, c.to)
			if err != nil {
				t.Fatalf("正长度的极短范围是合法输入，不应报错（尤其不能误报范围错误）: %v", err)
			}
			if ids := rangeIDs(got); !equalIDs(ids, c.want...) {
				t.Fatalf("got %v, want %v", ids, c.want)
			}
		})
	}
}

// 查看第 700 毫秒到第 800 毫秒：新版已到期，旧版登记结束（第 900 毫秒）
// 尚未到来也不能把已截短的旧版重新列入——空列表且没有错误。
func TestVersionsInRangeSubSecondEmptyBetweenNewEndAndOldRegisteredEnd(t *testing.T) {
	b := newSubSecondRangeBook(t)

	got, err := b.VersionsInRange("seat",
		subSecBase.Add(700*time.Millisecond), subSecBase.Add(800*time.Millisecond))
	if err != nil {
		t.Fatalf("合法但无命中的范围不应报错: %v", err)
	}
	if errors.Is(err, ErrInvalidRange) || errors.Is(err, ErrItemNotFound) {
		t.Fatalf("空结果不能与范围错误/项不存在混淆: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("旧版登记结束未到期也不能重新列入已截短的旧版，应为空列表: %+v", got)
	}
}

// 同一秒内结束等于开始、或只比开始早一纳秒时，返回 ErrInvalidRange 且
// 不给出版本列表，与合法但没有命中版本的空结果明确区分。
func TestVersionsInRangeSubSecondInvalidRange(t *testing.T) {
	b := newSubSecondRangeBook(t)

	at := subSecBase.Add(300 * time.Millisecond)
	for _, c := range []struct {
		name string
		from time.Time
		to   time.Time
	}{
		{"结束等于开始-同一毫秒内", at, at},
		{"结束只比开始早一纳秒", at, at.Add(-time.Nanosecond)},
		{"结束早于开始-同在交接点前一纳秒", subSecHand, subSecHand.Add(-time.Nanosecond)},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := b.VersionsInRange("seat", c.from, c.to)
			if !errors.Is(err, ErrInvalidRange) {
				t.Fatalf("want ErrInvalidRange, got %v", err)
			}
			if got != nil {
				t.Fatalf("范围错误不应返回版本列表, got %v", rangeIDs(got))
			}
		})
	}

	// 对照：同一位置长度仅一纳秒的合法范围不报错，与范围错误明确区分。
	got, err := b.VersionsInRange("seat", at, at.Add(time.Nanosecond))
	if err != nil {
		t.Fatalf("一纳秒长的合法范围不应误报范围错误: %v", err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v9") {
		t.Fatalf("一纳秒范围应命中当时生效的旧版: %v", ids)
	}
}
