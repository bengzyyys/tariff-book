package tariff

import (
	"errors"
	"testing"
	"time"
)

// VersionsInRange 的亚秒级回归沿用 subsecond_validity_regression_test.go
// 的固定时间线（2026-03-01 UTC 零点所在的一秒内，结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，第 100 毫秒起生效，登记结束第 900 毫秒
//	新版 seat-v2：单价 180 分，第 500 毫秒起替代旧版，第 700 毫秒结束
//
// 因而旧版实际有效区间为 [100ms, 500ms)（登记结束 900ms 不变），
// 新版实际有效区间为 [500ms, 700ms)。生效、替代、到期全部落在同一秒内，
// 范围查询不能把任何边界提前或延后到整秒。

// 题目示例：查看第 300 毫秒到第 600 毫秒，应依次得到旧版、新版，各一次；
// 范围按实际时刻比较，同一瞬间的东八区表示结论一致。
func TestVersionsInRangeSubSecondSpecExample(t *testing.T) {
	assertSubSecondTimesInSameSecond(t)
	b, _ := newSubSecondBook(t)

	from := subSecBase.Add(300 * time.Millisecond)
	to := subSecBase.Add(600 * time.Millisecond)
	got, err := b.VersionsInRange("seat", from, to)
	if err != nil {
		t.Fatalf("300ms 至 600ms 不应报错: %v", err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
		t.Fatalf("300ms 至 600ms 应依次得到旧版、新版各一次, got %v", ids)
	}

	// 同一瞬间换时区表示，命中结果与顺序一致。
	gotTZ, err := b.VersionsInRange("seat", from.In(east8), to.In(east8))
	if err != nil {
		t.Fatalf("东八区表示不应报错: %v", err)
	}
	if ids := rangeIDs(gotTZ); !equalIDs(ids, "seat-v1", "seat-v2") {
		t.Fatalf("东八区表示应得到相同结果, got %v", ids)
	}
}

// 列表按实际生效起点排列，而不是按版本标识的字符串顺序：
// 较早生效的版本即使标识在字符串顺序中更靠后，也必须排在前面。
func TestVersionsInRangeSubSecondOrdersByEffectiveStartNotID(t *testing.T) {
	b := NewBook()
	oldEnd := subSecRegEnd
	if err := b.RegisterVersion(RegisterRequest{
		// 标识刻意取字符串顺序更靠后的 "z..."。
		ItemID: "fare", VersionID: "z-fare-old", UnitPrice: 150,
		Start: subSecStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register z-fare-old: %v", err)
	}
	newEnd := subSecNewEnd
	if err := b.RegisterVersion(RegisterRequest{
		// 标识刻意取字符串顺序更靠前的 "a..."，但生效更晚。
		ItemID: "fare", VersionID: "a-fare-new", UnitPrice: 180,
		Start: subSecHand, End: &newEnd, Replaces: "z-fare-old",
	}); err != nil {
		t.Fatalf("register a-fare-new: %v", err)
	}

	got, err := b.VersionsInRange("fare",
		subSecBase.Add(300*time.Millisecond), subSecBase.Add(600*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "z-fare-old", "a-fare-new") {
		t.Fatalf("应按实际生效起点排序（旧版在前），不能按标识字符串排序, got %v", ids)
	}
	if !got[0].EffectiveStart.Before(got[1].EffectiveStart) {
		t.Fatalf("排序依据应为实际生效起点: %v, %v", got[0].EffectiveStart, got[1].EffectiveStart)
	}
}

// 范围含开始、不含结束：纳秒级的正长度范围是合法输入，不能误报范围错误，
// 也不能漏掉只在其中生效了一纳秒的版本。
func TestVersionsInRangeSubSecondNanosecondWindows(t *testing.T) {
	b, _ := newSubSecondBook(t)

	cases := []struct {
		name string
		from time.Time
		to   time.Time
		want []string
	}{
		// 交接点（第 500 毫秒）边界：
		// 范围从交接点前一纳秒开始、恰好在交接点结束，只列旧版（新版只是相接）。
		{"交接点前一纳秒到交接点-只列旧版",
			subSecHand.Add(-time.Nanosecond), subSecHand, []string{"seat-v1"}},
		// 范围从交接点开始、到后一纳秒结束，只列新版（开始包含；旧版已结束）。
		{"交接点到后一纳秒-只列新版",
			subSecHand, subSecHand.Add(time.Nanosecond), []string{"seat-v2"}},
		// 跨过交接点的两纳秒范围：两版各在其中生效一纳秒，都要列出。
		{"跨过交接点的两纳秒范围-两版",
			subSecHand.Add(-time.Nanosecond), subSecHand.Add(time.Nanosecond),
			[]string{"seat-v1", "seat-v2"}},

		// 几毫秒的极短范围同样合法：整段落在某一版的实际区间内只命中该版。
		{"交接点前一毫秒-只列旧版",
			subSecHand.Add(-time.Millisecond), subSecHand, []string{"seat-v1"}},
		{"交接点后三毫秒-只列新版",
			subSecHand, subSecHand.Add(3 * time.Millisecond), []string{"seat-v2"}},

		// 单纳秒范围完全落在各版区间内部：只生效一纳秒也不能漏。
		{"旧版区间内的单纳秒范围",
			subSecBase.Add(300 * time.Millisecond),
			subSecBase.Add(300*time.Millisecond + time.Nanosecond),
			[]string{"seat-v1"}},
		{"新版区间内的单纳秒范围",
			subSecBase.Add(600 * time.Millisecond),
			subSecBase.Add(600*time.Millisecond + time.Nanosecond),
			[]string{"seat-v2"}},

		// 新版结束点（第 700 毫秒，不含）边界：
		// 结束前一纳秒到结束点，新版只在其中生效一纳秒，仍应列出。
		{"新版结束前一纳秒-只列新版",
			subSecNewEnd.Add(-time.Nanosecond), subSecNewEnd, []string{"seat-v2"}},
		// 结束点本身起到后一纳秒：两版都不再生效，空列表。
		{"新版结束点上的单纳秒范围-空",
			subSecNewEnd, subSecNewEnd.Add(time.Nanosecond), []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !c.to.After(c.from) {
				t.Fatalf("用例前提错误：范围长度必须为正, from=%v to=%v", c.from, c.to)
			}
			got, err := b.VersionsInRange("seat", c.from, c.to)
			if err != nil {
				t.Fatalf("正长度的极短范围不能报错: %v", err)
			}
			if ids := rangeIDs(got); !equalIDs(ids, c.want...) {
				t.Fatalf("got %v, want %v", ids, c.want)
			}
		})
	}
}

// 查看第 700 毫秒到第 800 毫秒应得到空列表且没有错误：新版已到期，
// 旧版的登记结束（第 900 毫秒）尚未到来也不能把旧版重新列入。
func TestVersionsInRangeSubSecondAfterExpiryEmpty(t *testing.T) {
	b, _ := newSubSecondBook(t)

	got, err := b.VersionsInRange("seat",
		subSecBase.Add(700*time.Millisecond), subSecBase.Add(800*time.Millisecond))
	if err != nil {
		t.Fatalf("合法但无命中的范围不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("第 700 至 800 毫秒应为空列表，不能因旧版登记结束未到而重新列入旧版: %v",
			rangeIDs(got))
	}

	// 旧版登记结束前一纳秒的单纳秒范围同样为空：登记结束不复活已被替代的旧版。
	got, err = b.VersionsInRange("seat",
		subSecRegEnd.Add(-time.Nanosecond), subSecRegEnd)
	if err != nil {
		t.Fatalf("合法但无命中的范围不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("旧版登记结束前不应再命中旧版: %v", rangeIDs(got))
	}
}

// 返回的版本信息保留账本中的完整时间精度和来源，不把边界裁成查询范围：
// 旧版仍显示第 100 毫秒起点、第 900 毫秒登记结束和第 500 毫秒实际结束；
// 新版保留自己的起止、单价和替代来源。
func TestVersionsInRangeSubSecondViewPreservesLedgerPrecision(t *testing.T) {
	b, _ := newSubSecondBook(t)

	// 刻意用亚秒级的窄范围（300ms–600ms）命中两版，
	// 验证返回边界没有被裁成这两个查询端点。
	got, err := b.VersionsInRange("seat",
		subSecBase.Add(300*time.Millisecond), subSecBase.Add(600*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应命中两版, got %v", rangeIDs(got))
	}
	old, nv := got[0], got[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("顺序异常: %q, %q", old.VersionID, nv.VersionID)
	}

	// 旧版：登记 [100ms, 900ms)，实际被截短为 [100ms, 500ms)，小数精度完整保留。
	if old.ItemID != "seat" || old.UnitPrice != 150 {
		t.Fatalf("旧版标识/单价异常: %+v", old)
	}
	if !old.Start.Equal(subSecStart) || old.Start.Nanosecond() != 100_000_000 ||
		!old.EffectiveStart.Equal(subSecStart) || old.EffectiveStart.Nanosecond() != 100_000_000 {
		t.Fatalf("旧版起点应为第 100 毫秒且保留小数部分，不能裁成范围起点 300ms: %+v", old)
	}
	if old.End == nil || !old.End.Equal(subSecRegEnd) || old.End.Nanosecond() != 900_000_000 {
		t.Fatalf("旧版应保留登记结束第 900 毫秒: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(subSecHand) ||
		old.EffectiveEnd.Nanosecond() != 500_000_000 {
		t.Fatalf("旧版实际结束应显示交接点第 500 毫秒: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 新版：[500ms, 700ms)，登记结束与实际结束一致，替代来源为旧版。
	if nv.ItemID != "seat" || nv.UnitPrice != 180 {
		t.Fatalf("新版标识/单价异常: %+v", nv)
	}
	if !nv.Start.Equal(subSecHand) || nv.Start.Nanosecond() != 500_000_000 ||
		!nv.EffectiveStart.Equal(subSecHand) {
		t.Fatalf("新版起点应为第 500 毫秒且保留小数部分: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(subSecNewEnd) || nv.End.Nanosecond() != 700_000_000 ||
		nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(subSecNewEnd) ||
		nv.EffectiveEnd.Nanosecond() != 700_000_000 {
		t.Fatalf("新版应保留第 700 毫秒结束，不能裁成范围结束 600ms: 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}

	// 同一窄范围再查一次：两次返回的结束指针互不共享，修改前者不影响后者。
	again, err := b.VersionsInRange("seat",
		subSecBase.Add(300*time.Millisecond), subSecBase.Add(600*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	*got[0].End = subSecBase
	*got[0].EffectiveEnd = subSecBase
	*got[1].End = subSecBase
	*got[1].EffectiveEnd = subSecBase
	if again[0].End == nil || !again[0].End.Equal(subSecRegEnd) ||
		again[0].EffectiveEnd == nil || !again[0].EffectiveEnd.Equal(subSecHand) ||
		again[1].End == nil || !again[1].End.Equal(subSecNewEnd) {
		t.Fatalf("返回视图应为独立副本，修改上一次结果不能改写后续查询: %+v", again)
	}
}

// 结束等于开始，或只比开始早一纳秒时，返回已有的 ErrInvalidRange 且不给出
// 版本列表；与“合法但没有命中版本”的空结果明确区分。全部边界仍在同一秒内。
func TestVersionsInRangeSubSecondInvalidRangeVsEmpty(t *testing.T) {
	b, _ := newSubSecondBook(t)

	invalid := []struct {
		name string
		from time.Time
		to   time.Time
	}{
		{"结束等于开始-亚秒时刻", subSecHand, subSecHand},
		{"结束只比开始早一纳秒", subSecHand, subSecHand.Add(-time.Nanosecond)},
		{"结束等于开始-旧版区间内", subSecBase.Add(300 * time.Millisecond), subSecBase.Add(300 * time.Millisecond)},
	}
	for _, c := range invalid {
		t.Run(c.name, func(t *testing.T) {
			got, err := b.VersionsInRange("seat", c.from, c.to)
			if !errors.Is(err, ErrInvalidRange) {
				t.Fatalf("应返回 ErrInvalidRange, got %v", err)
			}
			if got != nil {
				t.Fatalf("范围错误不应返回版本列表, got %v", rangeIDs(got))
			}
		})
	}

	// 对照：同样只有一纳秒但方向为正、且无任何版本生效时，
	// 必须是空列表加 nil 错误，不能误报范围错误。
	got, err := b.VersionsInRange("seat",
		subSecBase.Add(750*time.Millisecond), subSecBase.Add(750*time.Millisecond+time.Nanosecond))
	if err != nil {
		t.Fatalf("合法的正长度单纳秒范围不能报错: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("合法但无命中应返回空列表: %+v", got)
	}
}
