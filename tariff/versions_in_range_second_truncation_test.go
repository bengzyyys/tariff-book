package tariff

import (
	"errors"
	"testing"
	"time"
)

// VersionsInRange 的“二次截短”回归：同一旧版 v1 先被较晚生效的 v3
// （2026-03-20 起、持续有效）替代，实际结束第一次被截短到 3 月 20 日；
// 随后又补登更早生效的 v2（2026-03-10 起至 2026-03-20，不含），
// 把 v1 的实际结束进一步提前到 3 月 10 日。登记顺序是 v1、v3、v2，
// 生效顺序却是 v1、v2、v3。范围查询必须反映查询当时账本里已经登记的
// 实际有效区间：不能停留在第一次替代后的安排，也不能把登记先后当成生效先后。
//
// 时间线常量（dtOldStart / dtOldEnd / dtEarlyHandoff / dtLateHandoff）与
// “v1、v3 已按序登记”的账本构造器 newTwiceTruncatedBaseBook 见
// second_truncation_regression_test.go，本文件直接复用，不再另起一份时间线。

// 补登 v2：3 月 10 日起至 3 月 20 日（不含）、单价 180 分、替代 v1。
// 它与持续有效的 v3 只在 3 月 20 日端点相接（结束时刻不含），登记必须成功。
func registerInterposedV2(t *testing.T, b *Book) {
	t.Helper()
	v2End := dtLateHandoff
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dtEarlyHandoff, End: &v2End, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("补登 seat-v2 应成功: %v", err)
	}
}

// 核心回归：v2 补登前后分别查看同一段 3 月 12 日至 3 月 18 日。
// 补登前账本里只有 v1 与 3 月 20 日起才生效的 v3，该窗口只列出 v1；
// 补登后 v1 的实际结束已提前到 3 月 10 日，同一窗口必须只列出 v2——
// v1 不能因为登记结束仍是 3 月 31 日、或第一次替代后曾被截短到 3 月 20 日
// 而继续命中。
func TestVersionsInRangeWindowBeforeAndAfterRetroactiveV2(t *testing.T) {
	b := newTwiceTruncatedBaseBook(t)

	from := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)

	got, err := b.VersionsInRange("seat", from, to)
	if err != nil {
		t.Fatalf("补登前范围查询不应报错: %v", err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1") {
		t.Fatalf("补登前 3 月 12 日至 18 日应只列出 v1（v3 尚未生效）, got %v", ids)
	}

	registerInterposedV2(t, b)

	got, err = b.VersionsInRange("seat", from, to)
	if err != nil {
		t.Fatalf("补登后范围查询不应报错: %v", err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v2") {
		t.Fatalf("补登后 3 月 12 日至 18 日应只列出 v2，v1 的登记结束与第一次截短都不能让它继续命中, got %v", ids)
	}
	if got[0].UnitPrice != 180 {
		t.Fatalf("命中的 v2 应保留登记单价 180 分, got %d", got[0].UnitPrice)
	}
}

// 补登后查看 3 月 9 日至 3 月 21 日，应按生效顺序得到 v1、v2、v3，各一次。
// 返回视图保留各版本登记时的原值：v1 的登记结束仍是 3 月 31 日、实际结束
// 显示为 3 月 10 日且被 v2 替代；v2 与 v3 的替代来源都仍是 v1，不能把 v3
// 改写成替代 v2；版本起止时间不能被裁成查询范围；三版单价各自保留，
// 不能统一套用最后登记的费率。
func TestVersionsInRangeFullTimelineAfterSecondTruncation(t *testing.T) {
	b := newTwiceTruncatedBaseBook(t)
	registerInterposedV2(t, b)

	got, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("补登后范围查询不应报错: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("3 月 9 日至 21 日应命中三版, got %v", rangeIDs(got))
	}
	old, early, late := got[0], got[1], got[2]
	if old.VersionID != "seat-v1" || early.VersionID != "seat-v2" ||
		late.VersionID != "seat-v3" {
		t.Fatalf("应按生效顺序（而非登记顺序）排列为 v1/v2/v3: %q, %q, %q",
			old.VersionID, early.VersionID, late.VersionID)
	}

	// 三版单价保留各自登记值（150/180/200），不能统一成最后登记的费率。
	if old.UnitPrice != 150 || early.UnitPrice != 180 || late.UnitPrice != 200 {
		t.Fatalf("三版单价应各自保留登记值: v1=%d v2=%d v3=%d",
			old.UnitPrice, early.UnitPrice, late.UnitPrice)
	}
	for _, v := range got {
		if v.ItemID != "seat" {
			t.Fatalf("返回视图不应混入其他费率项: %+v", v)
		}
	}

	// v1：登记结束仍是 3 月 31 日，实际结束被 v2 进一步提前到 3 月 10 日，
	// 改显示由 v2 替代；起点不能被裁成范围开始 3 月 9 日。
	if !old.Start.Equal(dtOldStart) || !old.EffectiveStart.Equal(dtOldStart) {
		t.Fatalf("v1 起点不应被查询范围改写: %+v", old)
	}
	if old.End == nil || !old.End.Equal(dtOldEnd) {
		t.Fatalf("v1 应保留登记结束 3 月 31 日, got %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(dtEarlyHandoff) {
		t.Fatalf("v1 实际结束应为 3 月 10 日, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("v1 替代关系异常: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// v2：实际有效区间 [3 月 10 日, 3 月 20 日)，替代 v1，未被任何版本替代。
	if !early.Start.Equal(dtEarlyHandoff) || !early.EffectiveStart.Equal(dtEarlyHandoff) {
		t.Fatalf("v2 起点异常: %+v", early)
	}
	if early.End == nil || !early.End.Equal(dtLateHandoff) ||
		early.EffectiveEnd == nil || !early.EffectiveEnd.Equal(dtLateHandoff) {
		t.Fatalf("v2 有效区间应为 [03-10, 03-20): 登记结束=%v 实际结束=%v",
			early.End, early.EffectiveEnd)
	}
	if early.Replaces != "seat-v1" || early.SupersededBy != "" {
		t.Fatalf("v2 替代关系异常: Replaces=%q SupersededBy=%q",
			early.Replaces, early.SupersededBy)
	}

	// v3：3 月 20 日起持续有效，替代来源仍是登记时填写的 v1，
	// 不能为了排成 v1→v2→v3 连续链而改写成 v2；持续有效也不能被范围结束
	// 3 月 21 日裁出一个结束边界。
	if !late.Start.Equal(dtLateHandoff) || !late.EffectiveStart.Equal(dtLateHandoff) {
		t.Fatalf("v3 起点异常: %+v", late)
	}
	if late.End != nil || late.EffectiveEnd != nil {
		t.Fatalf("v3 应仍持续有效，不能被范围结束裁短: 登记结束=%v 实际结束=%v",
			late.End, late.EffectiveEnd)
	}
	if late.Replaces != "seat-v1" || late.SupersededBy != "" {
		t.Fatalf("v3 的替代来源应保持 v1 不变: Replaces=%q SupersededBy=%q",
			late.Replaces, late.SupersededBy)
	}

	// 大范围只覆盖三版各一次，不因查询范围与各版区间如何相交而重复列出。
	again, err := b.VersionsInRange("seat",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(again); !equalIDs(ids, "seat-v1", "seat-v2", "seat-v3") {
		t.Fatalf("三版在大范围中仍应各出现一次、按生效顺序排列: %v", ids)
	}
}

// 两个交接点都沿用范围含开始、不含结束的规则，包括交接点前后一纳秒的精度：
// [03-10, 03-20) 只能得到 v2（v1 结束即范围开始、v3 开始即范围结束，都只是相接）；
// [03-20, 03-25) 只能得到 v3（v2 结束即范围开始）；范围只覆盖交接点前后一纳秒时，
// 要列出在这个范围内先后生效的相邻两版，不能丢失边界附近的时间精度。
func TestVersionsInRangeHandoffBoundariesAfterSecondTruncation(t *testing.T) {
	b := newTwiceTruncatedBaseBook(t)
	registerInterposedV2(t, b)

	cases := []struct {
		name string
		from time.Time
		to   time.Time
		want []string
	}{
		{
			"v2 完整实际区间只列 v2，v1 与 v3 都只是端点相接",
			dtEarlyHandoff, dtLateHandoff, []string{"seat-v2"},
		},
		{
			"v3 交接点起的范围只列 v3，v2 结束即范围开始不算命中",
			dtLateHandoff, time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
			[]string{"seat-v3"},
		},
		{
			"覆盖 3 月 10 日交接点前后一纳秒应列出相邻的 v1、v2",
			dtEarlyHandoff.Add(-time.Nanosecond), dtEarlyHandoff.Add(time.Nanosecond),
			[]string{"seat-v1", "seat-v2"},
		},
		{
			"覆盖 3 月 20 日交接点前后一纳秒应列出相邻的 v2、v3",
			dtLateHandoff.Add(-time.Nanosecond), dtLateHandoff.Add(time.Nanosecond),
			[]string{"seat-v2", "seat-v3"},
		},
		{
			"3 月 10 日交接点上的单纳秒范围（开始含）只列 v2",
			dtEarlyHandoff, dtEarlyHandoff.Add(time.Nanosecond),
			[]string{"seat-v2"},
		},
		{
			"3 月 20 日交接点上的单纳秒范围（开始含）只列 v3",
			dtLateHandoff, dtLateHandoff.Add(time.Nanosecond),
			[]string{"seat-v3"},
		},
		{
			"恰在 3 月 10 日交接点前一纳秒结束的范围只列 v1",
			dtEarlyHandoff.Add(-time.Nanosecond), dtEarlyHandoff,
			[]string{"seat-v1"},
		},
		{
			"恰在 3 月 20 日交接点前一纳秒结束的范围只列 v2",
			dtLateHandoff.Add(-time.Nanosecond), dtLateHandoff,
			[]string{"seat-v2"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := b.VersionsInRange("seat", c.from, c.to)
			if err != nil {
				t.Fatalf("范围查询不应报错: %v", err)
			}
			if ids := rangeIDs(got); !equalIDs(ids, c.want...) {
				t.Fatalf("got %v, want %v", ids, c.want)
			}
		})
	}
}

// 范围查询只读取费率安排：前后多次查询既不受理报价、不占用请求标识，
// 也不改写已保存的报价结果；补登 v2 的登记规则（与 v3 重叠即拒、
// 端点相接可成功）同样不受查询影响。
func TestVersionsInRangeSecondTruncationReadOnly(t *testing.T) {
	b := newTwiceTruncatedBaseBook(t)

	// 交接发生前已确认的报价：v1、150 分、数量 2、总价 300 分。
	first, err := b.Quote(QuoteRequest{
		RequestID: "dt-range-kept", ItemID: "seat", VersionID: "seat-v1", Quantity: 2,
	})
	if err != nil {
		t.Fatalf("登记阶段引用 v1 是正常受理，err 应为空: %v", err)
	}
	if !first.Confirmed || first.UnitPrice != 150 || first.Total != 300 {
		t.Fatalf("前置确认结果异常: %+v", first)
	}

	from := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)

	// 补登前查询：只列 v1。
	if got, err := b.VersionsInRange("seat", from, to); err != nil {
		t.Fatalf("补登前查询报错: %v", err)
	} else if ids := rangeIDs(got); !equalIDs(ids, "seat-v1") {
		t.Fatalf("补登前应只列 v1, got %v", ids)
	}

	// 查询不改变登记规则：不填结束时间的 v2 会延伸进持续有效的 v3，仍被拒绝。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dtEarlyHandoff, Replaces: "seat-v1",
	}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("挤占 v3 有效时间的登记仍应返回 ErrOverlap, got %v", err)
	}
	registerInterposedV2(t, b)

	// 补登后的多次范围查询：同段窗口只列 v2，完整时间线列三版。
	if got, err := b.VersionsInRange("seat", from, to); err != nil {
		t.Fatalf("补登后查询报错: %v", err)
	} else if ids := rangeIDs(got); !equalIDs(ids, "seat-v2") {
		t.Fatalf("补登后应只列 v2, got %v", ids)
	}
	full, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("完整时间线查询报错: %v", err)
	}
	if ids := rangeIDs(full); !equalIDs(ids, "seat-v1", "seat-v2", "seat-v3") {
		t.Fatalf("完整时间线应按 v1/v2/v3 返回, got %v", ids)
	}

	// 调用方改写返回视图中 v1 的两个结束时间，再次查询边界仍是账本原值，
	// 且两次查询不共享结束时间指针。
	if full[0].End == nil || full[0].EffectiveEnd == nil {
		t.Fatalf("前提：v1 视图应带登记结束与实际结束指针: %+v", full[0])
	}
	*full[0].End = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	*full[0].EffectiveEnd = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	again, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("改写视图后的查询报错: %v", err)
	}
	if again[0].VersionID != "seat-v1" ||
		again[0].End == nil || !again[0].End.Equal(dtOldEnd) ||
		again[0].EffectiveEnd == nil || !again[0].EffectiveEnd.Equal(dtEarlyHandoff) {
		t.Fatalf("v1 边界被范围查询结果的外部修改改写: %+v", again[0])
	}
	if again[0].End == full[0].End || again[0].EffectiveEnd == full[0].EffectiveEnd {
		t.Fatal("两次范围查询共享了结束时间指针")
	}

	// 已保存的报价结果原样保留：仍是 v1、150 分、300 分和首次受理时刻，
	// 不按补登后的费率重算。
	got, err := b.Lookup("dt-range-kept")
	if err != nil {
		t.Fatalf("已保存的报价结果不应丢失: %v", err)
	}
	if got != first {
		t.Fatalf("范围查询与补登不能改写已保存的首次结果: %+v -> %+v", first, got)
	}
	outcomes, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatalf("按费率项查看报价结果不应报错: %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("只读范围查询不应产生报价记录，应仍只有一笔, got %d 笔", len(outcomes))
	}
	if outcomes[0] != first {
		t.Fatalf("按费率项取回的记录应与首次结果一致: %+v", outcomes[0])
	}

	// 范围查询不占用请求标识：从未用于报价的新标识仍查无记录，
	// 用它发起报价会被当成首次受理，而不是请求冲突。
	if _, err := b.Lookup("dt-range-unused"); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("范围查询不应占用请求标识, got %v", err)
	}
}
