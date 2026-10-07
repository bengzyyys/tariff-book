package tariff

import (
	"errors"
	"testing"
	"time"
)

// VersionsInRange 的二次截短回归场景沿用 second_truncation_regression_test.go
// 的固定时间线（全部为 2026 年 UTC 零点，结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 起生效，登记结束 2026-03-31
//	较晚版本 seat-v3：单价 200 分，2026-03-20 起替代 v1，持续有效
//	补登的更早版本 seat-v2：单价 180 分，2026-03-10 起替代 v1，2026-03-20 结束
//
// 登记顺序是 v1、v3、v2。范围查询必须按查询当时账本里已登记的实际有效
// 区间回答：不能停留在 v3 第一次替代后的安排，也不能把登记先后当成生效
// 先后。所有时刻写死，结论不依赖运行当天的真实日期。

// newTwiceTruncatedRangeBook 在 v1、v3 已登记的基础上成功补登 v2，
// 返回三个版本齐备的账本：v1 的实际结束被 v2 进一步截短到 3 月 10 日。
func newTwiceTruncatedRangeBook(t *testing.T) *Book {
	t.Helper()
	b := newTwiceTruncatedBaseBook(t)
	earlyEnd := dtLateHandoff
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dtEarlyHandoff, End: &earlyEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b
}

// 同一段 3 月 12 日至 3 月 18 日，补登 v2 前后各查一次：
// 补登前落在 v1 被 v3 截短后的实际区间 [03-01, 03-20) 内，只列出 v1；
// 补登后 v1 的实际结束被进一步截短到 3 月 10 日，同一段只列出 v2——
// v1 不能因为登记结束仍是 3 月 31 日、或第一次替代后曾截短到 3 月 20 日
// 而继续命中。
func TestVersionsInRangeReflectsSecondTruncation(t *testing.T) {
	b := newTwiceTruncatedBaseBook(t)

	midFrom := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	midTo := time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)

	got, err := b.VersionsInRange("seat", midFrom, midTo)
	if err != nil {
		t.Fatalf("补登前查询不应报错: %v", err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1") {
		t.Fatalf("补登 v2 前 3 月 12 日至 18 日应只列出 v1, got %v", ids)
	}

	// 成功补登 v2：3 月 10 日起至 3 月 20 日、单价 180 分，同样替代 v1。
	earlyEnd := dtLateHandoff
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dtEarlyHandoff, End: &earlyEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("补登 v2 应成功: %v", err)
	}

	got, err = b.VersionsInRange("seat", midFrom, midTo)
	if err != nil {
		t.Fatalf("补登后查询不应报错: %v", err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v2") {
		t.Fatalf("补登 v2 后同一范围应只列出 v2（v1 不得继续命中）, got %v", ids)
	}
}

// 补登后查看 3 月 9 日至 3 月 21 日：按生效顺序得到 v1、v2、v3，各出现一次。
// 列表沿用账本原值：v1 保留登记结束 3 月 31 日、实际结束 3 月 10 日并显示
// 被 v2 替代；v2 与 v3 的替代来源都仍是 v1，不能把 v3 改成替代 v2；三版单价
// 保留各自登记值；版本起止时间不能被裁成查询范围的边界。
func TestVersionsInRangeSecondTruncationFullRangeViews(t *testing.T) {
	b := newTwiceTruncatedRangeBook(t)

	got, err := b.VersionsInRange("seat",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(got); !equalIDs(ids, "seat-v1", "seat-v2", "seat-v3") {
		t.Fatalf("3 月 9 日至 21 日应按生效顺序各列一次 v1/v2/v3, got %v", ids)
	}
	old, early, late := got[0], got[1], got[2]

	// v1：登记结束仍是 3 月 31 日，实际结束被 v2 截短到 3 月 10 日，
	// 显示被 v2 替代；开始时刻保留 3 月 1 日，不被裁成范围开始 3 月 9 日。
	if old.ItemID != "seat" || old.UnitPrice != 150 {
		t.Fatalf("v1 标识/单价异常: %+v", old)
	}
	if !old.Start.Equal(dtOldStart) || !old.EffectiveStart.Equal(dtOldStart) {
		t.Fatalf("v1 开始边界不应被改成范围开始: %+v", old)
	}
	if old.End == nil || !old.End.Equal(dtOldEnd) {
		t.Fatalf("v1 应保留登记结束 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(dtEarlyHandoff) {
		t.Fatalf("v1 实际结束应被 v2 截短到 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("v1 替代关系异常: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}

	// v2：实际有效区间 [03-10, 03-20)，替代来源是 v1。
	if early.UnitPrice != 180 {
		t.Fatalf("v2 单价应保留登记值 180, got %d", early.UnitPrice)
	}
	if !early.Start.Equal(dtEarlyHandoff) || !early.EffectiveStart.Equal(dtEarlyHandoff) {
		t.Fatalf("v2 开始边界异常: %+v", early)
	}
	if early.End == nil || !early.End.Equal(dtLateHandoff) ||
		early.EffectiveEnd == nil || !early.EffectiveEnd.Equal(dtLateHandoff) {
		t.Fatalf("v2 有效区间应为 [03-10, 03-20): 登记结束=%v 实际结束=%v",
			early.End, early.EffectiveEnd)
	}
	if early.Replaces != "seat-v1" || early.SupersededBy != "" {
		t.Fatalf("v2 替代关系异常: Replaces=%q SupersededBy=%q", early.Replaces, early.SupersededBy)
	}

	// v3：从 3 月 20 日起持续有效，替代来源仍是 v1，不能改成替代 v2；
	// 持续有效不因查询范围结束于 3 月 21 日而多出结束边界。
	if late.UnitPrice != 200 {
		t.Fatalf("v3 单价应保留登记值 200, got %d", late.UnitPrice)
	}
	if !late.Start.Equal(dtLateHandoff) || !late.EffectiveStart.Equal(dtLateHandoff) {
		t.Fatalf("v3 开始边界异常: %+v", late)
	}
	if late.End != nil || late.EffectiveEnd != nil {
		t.Fatalf("v3 应持续有效，不能出现结束边界: 登记结束=%v 实际结束=%v",
			late.End, late.EffectiveEnd)
	}
	if late.Replaces != "seat-v1" || late.SupersededBy != "" {
		t.Fatalf("v3 替代来源应保持 v1 不变: Replaces=%q SupersededBy=%q",
			late.Replaces, late.SupersededBy)
	}
}

// 两个交接点都沿用范围含开始、不含结束的规则：
// 3 月 10 日至 3 月 20 日只得到 v2（v1、v3 都只是与范围端点相接）；
// 3 月 20 日至 3 月 25 日只得到 v3。范围只覆盖任一交接点前后一纳秒时，
// 应列出在该范围内先后生效的相邻两版，边界附近的时间精度不能丢失。
func TestVersionsInRangeSecondTruncationHandoffBoundaries(t *testing.T) {
	b := newTwiceTruncatedRangeBook(t)

	cases := []struct {
		name string
		from time.Time
		to   time.Time
		want []string
	}{
		{"v1/v2 与 v2/v3 交接点恰为范围端点", dtEarlyHandoff, dtLateHandoff, []string{"seat-v2"}},
		{"v3 交接点至 3 月 25 日", dtLateHandoff,
			time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC), []string{"seat-v3"}},
		{"范围覆盖 v1/v2 交接点前后一纳秒",
			dtEarlyHandoff.Add(-time.Nanosecond), dtEarlyHandoff.Add(time.Nanosecond),
			[]string{"seat-v1", "seat-v2"}},
		{"范围覆盖 v2/v3 交接点前后一纳秒",
			dtLateHandoff.Add(-time.Nanosecond), dtLateHandoff.Add(time.Nanosecond),
			[]string{"seat-v2", "seat-v3"}},
		{"v1/v2 交接点上的单纳秒范围", dtEarlyHandoff, dtEarlyHandoff.Add(time.Nanosecond),
			[]string{"seat-v2"}},
		{"v2/v3 交接点上的单纳秒范围", dtLateHandoff, dtLateHandoff.Add(time.Nanosecond),
			[]string{"seat-v3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := b.VersionsInRange("seat", c.from, c.to)
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if ids := rangeIDs(got); !equalIDs(ids, c.want...) {
				t.Fatalf("got %v, want %v", ids, c.want)
			}
		})
	}
}

// 这些范围查询只读取费率安排：既有登记规则与已保存的报价结果都保持原样。
func TestVersionsInRangeSecondTruncationReadOnly(t *testing.T) {
	now, setNow := fixedClock(dtOldStart)
	b := NewBook(WithClock(now))
	oldEnd := dtOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: dtOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}

	// v1 尚未被替代时确认一笔 600 分报价，作为后续比对的已保存结果。
	setNow(time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC))
	confirmed, err := b.Quote(QuoteRequest{
		RequestID: "dt-range-600", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil || !confirmed.Confirmed || confirmed.Total != 600 {
		t.Fatalf("前提：v1 有效期内应确认 600 分: %+v (%v)", confirmed, err)
	}

	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: dtLateHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}
	earlyEnd := dtLateHandoff
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dtEarlyHandoff, End: &earlyEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}

	// 依次执行场景中的各次范围查询。
	queries := [][2]time.Time{
		{time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC)},
		{dtEarlyHandoff, dtLateHandoff},
		{dtLateHandoff, time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)},
		{dtEarlyHandoff.Add(-time.Nanosecond), dtEarlyHandoff.Add(time.Nanosecond)},
		{dtLateHandoff.Add(-time.Nanosecond), dtLateHandoff.Add(time.Nanosecond)},
	}
	for i, q := range queries {
		if _, err := b.VersionsInRange("seat", q[0], q[1]); err != nil {
			t.Fatalf("第 %d 次范围查询不应报错: %v", i, err)
		}
	}

	// 已保存的报价结果原样保留。
	got, err := b.Lookup("dt-range-600")
	if err != nil || got != confirmed {
		t.Fatalf("范围查询不能改写已保存的报价结果: %+v (%v)", got, err)
	}

	// 登记规则不变：试图登记与 v2 实际有效区间重叠的版本仍被拒绝，
	// 且失败登记不留痕迹。
	err = b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v4", UnitPrice: 220,
		Start: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("范围查询后登记规则应保持原样，重叠登记应返回 ErrOverlap, got %v", err)
	}
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if ids := rangeIDs(views); !equalIDs(ids, "seat-v1", "seat-v2", "seat-v3") {
		t.Fatalf("范围查询与失败登记都不能改变版本集合: %v", ids)
	}

	// 报价路径不受影响：v2 有效区间内引用 v2 仍按 180 分确认。
	setNow(time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC))
	fresh, err := b.Quote(QuoteRequest{
		RequestID: "dt-range-v2-360", ItemID: "seat", VersionID: "seat-v2", Quantity: 2,
	})
	if err != nil || !fresh.Confirmed || fresh.UnitPrice != 180 || fresh.Total != 360 {
		t.Fatalf("范围查询后 v2 报价应按 180 分确认 360 分: %+v (%v)", fresh, err)
	}
}
