package tariff

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ItemVersionsInRange 场景沿用 EffectiveVersionAt 的固定时间线（UTC，结束时刻均不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 起生效，登记结束 2026-03-31，
//	              实际有效区间被新版截断到 2026-03-10
//	新版 seat-v2：单价 180 分，2026-03-10 起替代旧版，2026-03-20 结束
//
// 范围查询包含开始时刻、不包含结束时刻，仅边界相接不算命中。
func day(dayOfMonth int) time.Time {
	return time.Date(2026, 3, dayOfMonth, 0, 0, 0, 0, time.UTC)
}

func wantIDs(views []VersionView) []string {
	ids := make([]string, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.VersionID)
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
// 查看 3 月 10 日至 3 月 25 日只得到新版（旧版实际区间止于交接点，
// 与范围仅在 3 月 10 日相接，不算命中）；查看 3 月 20 日至 3 月 25 日为空。
func TestItemVersionsInRangeTaskExample(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	got, err := b.ItemVersionsInRange("seat", day(9), day(11))
	if err != nil {
		t.Fatalf("3 月 9 日至 11 日不应报错: %v", err)
	}
	if ids := wantIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
		t.Fatalf("3 月 9 日至 11 日应为旧版+新版, got %v", ids)
	}

	got, err = b.ItemVersionsInRange("seat", day(10), day(25))
	if err != nil {
		t.Fatalf("3 月 10 日至 25 日不应报错: %v", err)
	}
	if ids := wantIDs(got); !equalIDs(ids, "seat-v2") {
		t.Fatalf("3 月 10 日至 25 日应只有新版, got %v", ids)
	}

	got, err = b.ItemVersionsInRange("seat", day(20), day(25))
	if err != nil {
		t.Fatalf("3 月 20 日至 25 日不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("3 月 20 日至 25 日应为空列表, got %v", wantIDs(got))
	}
}

// 边界相接不算命中：范围结束恰好等于版本实际生效起点、范围开始恰好等于
// 版本实际结束，都不列出该版本；范围包含端点内侧才命中。
func TestItemVersionsInRangeBoundaryTouchNotHit(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	cases := []struct {
		name string
		from time.Time
		to   time.Time
		want []string
	}{
		// 范围在新版起点处结束：新版不命中，旧版命中（旧版实际区间覆盖范围内时刻）。
		{"范围止于新版起点", day(5), effV2Start, []string{"seat-v1"}},
		// 范围从旧版实际结束（交接点）开始：旧版不命中，新版命中。
		{"范围起于旧版实际结束", effV2Start, day(12), []string{"seat-v2"}},
		// 范围从新版结束时刻开始：新版不命中。
		{"范围起于新版结束", effV2End, day(28), nil},
		// 范围止于旧版开始：旧版不命中。
		{"范围止于首版开始", day(1).Add(-48 * time.Hour), effV1Start, nil},
		// 范围恰为新版起点前 1 纳秒结束：新版仍不命中。
		{"范围止于新版起点前1纳秒", day(9), effV2Start.Add(-time.Nanosecond), []string{"seat-v1"}},
		// 范围从新版结束前 1 纳秒开始：新版命中。
		{"范围起于新版结束前1纳秒", effV2End.Add(-time.Nanosecond), day(25), []string{"seat-v2"}},
		// 完整覆盖两版。
		{"范围覆盖全部", day(1), day(31), []string{"seat-v1", "seat-v2"}},
		// 完全落在旧版实际区间内。
		{"范围落在旧版内部", day(2), day(5), []string{"seat-v1"}},
		// 完全落在两版之间不存在空档（本场景无空档），落在全部到期之后。
		{"范围在全部到期之后", day(21), day(22), nil},
		// 完全在首版开始之前。
		{"范围在首版开始之前", day(1).Add(-72 * time.Hour), day(1).Add(-24 * time.Hour), nil},
	}
	for _, c := range cases {
		got, err := b.ItemVersionsInRange("seat", c.from, c.to)
		if err != nil {
			t.Fatalf("%s: 不应报错, got %v", c.name, err)
		}
		if ids := wantIDs(got); !equalIDs(ids, c.want...) {
			t.Fatalf("%s: got %v, want %v", c.name, ids, c.want)
		}
	}
}

// 被替代的旧版按截短后的实际结束判断：登记结束 3 月 31 日落在范围内
// 也不能让旧版命中；新版到期后旧版不恢复。
func TestItemVersionsInRangeTruncatedOldVersion(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	// 范围完全在交接点之后、旧版登记结束之前：旧版登记结束仍在范围内，但不命中。
	got, err := b.ItemVersionsInRange("seat", day(15), day(25))
	if err != nil {
		t.Fatal(err)
	}
	if ids := wantIDs(got); !equalIDs(ids, "seat-v2") {
		t.Fatalf("旧版登记结束在范围内也不能命中旧版: got %v", ids)
	}

	// 新版到期之后：旧版不恢复，范围内没有任何版本。
	got, err = b.ItemVersionsInRange("seat", day(21), day(31))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("新版到期后旧版不应恢复: got %v", wantIDs(got))
	}
}

// 持续有效的版本按没有结束边界处理：与任何不早于其起点的范围相交。
func TestItemVersionsInRangeOpenEnded(t *testing.T) {
	b := NewBook()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "open", VersionID: "v1", UnitPrice: 7,
		Start: day(1),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := b.ItemVersionsInRange("open", day(1), time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(got) != 1 || got[0].VersionID != "v1" {
		t.Fatalf("持续有效版本应命中远期范围: %+v (%v)", got, err)
	}
	if got[0].End != nil || got[0].EffectiveEnd != nil {
		t.Fatalf("持续有效版本的两种结束都应为 nil: %+v", got[0])
	}

	// 范围完全在其起点之前则不命中。
	got, err = b.ItemVersionsInRange("open", day(1).Add(-48*time.Hour), day(1).Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("起点之前的范围不应命中: %v", wantIDs(got))
	}
}

// 结束不晚于开始时一律返回可明确识别的范围错误，不返回版本列表；
// 即使费率项不存在，范围错误也优先。
func TestItemVersionsInRangeInvalidRange(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	for _, c := range []struct {
		name string
		from time.Time
		to   time.Time
	}{
		{"结束等于开始", day(10), day(10)},
		{"结束早于开始", day(11), day(10)},
	} {
		got, err := b.ItemVersionsInRange("seat", c.from, c.to)
		if !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("%s: want ErrInvalidRange, got %v", c.name, err)
		}
		if got != nil {
			t.Fatalf("%s: 范围错误不应返回版本列表, got %v", c.name, wantIDs(got))
		}
		if errors.Is(err, ErrItemNotFound) {
			t.Fatalf("%s: 范围错误不能被误报成费率项不存在: %v", c.name, err)
		}

		// 费率项不存在时同样先报范围错误。
		if _, err := b.ItemVersionsInRange("never-seen", c.from, c.to); !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("%s（未知费率项）: want ErrInvalidRange, got %v", c.name, err)
		}
	}
}

// 范围合法但费率项从未登记过版本时沿用现有的费率项未找到错误；
// 费率项存在但没有命中版本时返回空列表且不报错，两者必须可区分。
func TestItemVersionsInRangeMissingItemVsEmptyResult(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	got, err := b.ItemVersionsInRange("never-seen", day(9), day(11))
	if !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("从未登记的费率项应返回 ErrItemNotFound, got %v", err)
	}
	if got != nil {
		t.Fatalf("费率项未找到不应返回版本列表, got %v", wantIDs(got))
	}

	got, err = b.ItemVersionsInRange("seat", day(20), day(25))
	if err != nil {
		t.Fatalf("存在的费率项无命中不应报错, got %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("无命中应返回非 nil 空列表, got %#v", got)
	}
}

// 返回列表沿用现有版本视图的全部信息，版本边界保持登记值，
// 不被改写成查询范围的边界。
func TestItemVersionsInRangeViewFieldsUntouched(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	got, err := b.ItemVersionsInRange("seat", day(9), day(11))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应命中两版, got %v", wantIDs(got))
	}

	old, nv := got[0], got[1]
	if old.ItemID != "seat" || old.VersionID != "seat-v1" || old.UnitPrice != 150 {
		t.Fatalf("旧版标识/单价异常: %+v", old)
	}
	if !old.Start.Equal(effV1Start) || !old.EffectiveStart.Equal(effV1Start) {
		t.Fatalf("旧版开始时刻应保持登记值, 不是范围起点: %+v", old)
	}
	if old.End == nil || !old.End.Equal(effV1End) {
		t.Fatalf("旧版登记结束应保持 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(effV2Start) {
		t.Fatalf("旧版实际结束应保持交接点 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}

	if nv.ItemID != "seat" || nv.VersionID != "seat-v2" || nv.UnitPrice != 180 {
		t.Fatalf("新版标识/单价异常: %+v", nv)
	}
	if !nv.Start.Equal(effV2Start) || !nv.EffectiveStart.Equal(effV2Start) {
		t.Fatalf("新版开始时刻应保持登记值: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(effV2End) || nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(effV2End) {
		t.Fatalf("新版结束时刻应保持登记值, 不是范围终点: %+v", nv)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q", nv.Replaces, nv.SupersededBy)
	}
}

// 每个命中的版本只出现一次，按实际生效起点从早到晚排列。
func TestItemVersionsInRangeSortedNoDuplicates(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	// 三段连续区间：3 月 1 日至 10 日、10 日至 20 日、20 日起持续有效。
	must(RegisterRequest{
		ItemID: "multi", VersionID: "m1", UnitPrice: 100,
		Start: day(1), End: ptrTime(day(10)),
	})
	must(RegisterRequest{
		ItemID: "multi", VersionID: "m2", UnitPrice: 200,
		Start: day(10), End: ptrTime(day(20)),
	})
	must(RegisterRequest{
		ItemID: "multi", VersionID: "m3", UnitPrice: 300,
		Start: day(20),
	})

	got, err := b.ItemVersionsInRange("multi", day(1), day(31))
	if err != nil {
		t.Fatal(err)
	}
	if ids := wantIDs(got); !equalIDs(ids, "m1", "m2", "m3") {
		t.Fatalf("应按实际生效起点从早到晚各出现一次, got %v", ids)
	}

	// 大范围重复查询结果稳定，且与 ItemVersions 的全量顺序一致。
	again, err := b.ItemVersionsInRange("multi", day(1), day(31))
	if err != nil {
		t.Fatal(err)
	}
	if ids := wantIDs(again); !equalIDs(ids, "m1", "m2", "m3") {
		t.Fatalf("重复查询顺序应稳定, got %v", ids)
	}
	all, err := b.ItemVersions("multi")
	if err != nil {
		t.Fatal(err)
	}
	if ids := wantIDs(all); !equalIDs(ids, "m1", "m2", "m3") {
		t.Fatalf("ItemVersions 顺序基准异常, got %v", ids)
	}
}

// 其他费率项即使有同名版本也不参与本项的范围查询。
func TestItemVersionsInRangeScopedToItem(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	// seat/v1 与 room/v1 同名、单价不同、区间不同。
	must(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: day(1), End: ptrTime(day(10)),
	})
	must(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 300,
		Start: day(5), End: ptrTime(day(28)),
	})

	got, err := b.ItemVersionsInRange("seat", day(12), day(25))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("seat 在 3 月 12 日后无版本, 不能借 room 的同名版本: %v", wantIDs(got))
	}

	got, err = b.ItemVersionsInRange("room", day(12), day(25))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ItemID != "room" || got[0].UnitPrice != 300 {
		t.Fatalf("room 应只列出本项版本: %+v", got)
	}
}

// 同一瞬间用不同时区表示范围边界，查询结论一致。
func TestItemVersionsInRangeSameInstantAnyTimezone(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	from := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)
	reprs := [][2]time.Time{
		{from, to},
		{from.In(east8), to.In(east8)},
		{from.In(time.FixedZone("UTC-5", -5*3600)), to.In(time.FixedZone("UTC-5", -5*3600))},
	}
	for i, r := range reprs {
		got, err := b.ItemVersionsInRange("seat", r[0], r[1])
		if err != nil {
			t.Fatalf("表示%d: %v", i, err)
		}
		if ids := wantIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
			t.Fatalf("表示%d: got %v, want [seat-v1 seat-v2]", i, ids)
		}
	}
}

// 范围可以在过去或未来，与账本时钟无关：时钟停在过去或未来都不改变结论。
func TestItemVersionsInRangeIgnoresLedgerClock(t *testing.T) {
	b, setNow := newEffectiveVersionBook(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	got, err := b.ItemVersionsInRange("seat", day(9), day(11))
	if err != nil {
		t.Fatal(err)
	}
	if ids := wantIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
		t.Fatalf("时钟在未来不影响查过去: %v", ids)
	}

	setNow(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	got, err = b.ItemVersionsInRange("seat", day(9), day(11))
	if err != nil {
		t.Fatal(err)
	}
	if ids := wantIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
		t.Fatalf("时钟在过去不影响查未来: %v", ids)
	}
}

// 查询结果是独立副本：改写返回视图中的结束时间不能影响账本、
// 后续范围查询、其他查询入口和报价。
func TestItemVersionsInRangeViewMutationIsolated(t *testing.T) {
	b, setNow := newEffectiveVersionBook(t, effV1Start)

	got, err := b.ItemVersionsInRange("seat", day(9), day(11))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("前提：应命中两版, got %v", wantIDs(got))
	}
	// 调用方为展示目的改写两种结束时间。
	*got[0].End = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	*got[0].EffectiveEnd = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	*got[1].End = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	*got[1].EffectiveEnd = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	again, err := b.ItemVersionsInRange("seat", day(9), day(11))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 {
		t.Fatalf("再次查询应仍命中两版, got %v", wantIDs(again))
	}
	if again[0].End == nil || !again[0].End.Equal(effV1End) {
		t.Fatalf("登记结束被查询结果的修改改写: %v", again[0].End)
	}
	if again[0].EffectiveEnd == nil || !again[0].EffectiveEnd.Equal(effV2Start) {
		t.Fatalf("实际结束被查询结果的修改改写: %v", again[0].EffectiveEnd)
	}
	// 两次查询的同名指针字段必须互相独立。
	if got[0].End == again[0].End || got[0].EffectiveEnd == again[0].EffectiveEnd {
		t.Fatal("两次范围查询共享了结束时间指针")
	}

	// 被改写的结果不能让旧版在交接点后“复活”：范围 3 月 15 日至 25 日仍只命中新版。
	got2, err := b.ItemVersionsInRange("seat", day(15), day(25))
	if err != nil {
		t.Fatal(err)
	}
	if ids := wantIDs(got2); !equalIDs(ids, "seat-v2") {
		t.Fatalf("改写查询结果不能改变账本区间: got %v", ids)
	}

	// 报价仍按账本真实区间处理：交接点起旧版失效、新版有效。
	setNow(effV2Start)
	out, _ := b.Quote(QuoteRequest{RequestID: "rq-old", ItemID: "seat", VersionID: "seat-v1", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("修改范围查询结果不能影响后续报价: %+v", out)
	}
	out, _ = b.Quote(QuoteRequest{RequestID: "rq-new", ItemID: "seat", VersionID: "seat-v2", Quantity: 1})
	if !out.Confirmed {
		t.Fatalf("新版在交接点应可报价: %+v", out)
	}
}

// 范围查询是只读操作：不受理报价、不占用请求标识，也不产生可被 Lookup 的记录。
func TestItemVersionsInRangeReadOnly(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	for i := 0; i < 8; i++ {
		if _, err := b.ItemVersionsInRange("seat", day(1+i), day(2+i)); err != nil {
			t.Fatalf("查询 %d 意外失败: %v", i, err)
		}
	}
	if _, err := b.Lookup("quote-after-range-queries"); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("只读查询不应产生报价记录: %v", err)
	}

	// 该标识仍是全新的：首次报价正常受理并被保存。
	out, err := b.Quote(QuoteRequest{
		RequestID: "quote-after-range-queries", ItemID: "seat", VersionID: "seat-v1", Quantity: 2,
	})
	if err != nil || !out.Confirmed || out.Total != 300 {
		t.Fatalf("范围查询不应占用请求标识或影响首次受理: %+v (%v)", out, err)
	}

	// 版本集合也不因查询而改变。
	views, err := b.ItemVersions("seat")
	if err != nil || len(views) != 2 {
		t.Fatalf("范围查询不应改动版本集合: %d 个版本 (%v)", len(views), err)
	}
}

// 并发范围查询应始终给出一致结论。
func TestItemVersionsInRangeConcurrentReads(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := b.ItemVersionsInRange("seat", day(9), day(11))
			if err != nil {
				errs <- fmt.Errorf("查询 %d: %w", i, err)
				return
			}
			if ids := wantIDs(got); !equalIDs(ids, "seat-v1", "seat-v2") {
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
