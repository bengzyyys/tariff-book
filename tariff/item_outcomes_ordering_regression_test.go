package tariff

import (
	"testing"
	"time"
)

// 本文件为“按费率项核对已受理报价”（ItemOutcomes）补充排序回归：
// 确认与拒绝混合列出时，必须按首次受理的完整实际时刻从早到晚排列，
// 同一瞬间再按请求标识的字符串顺序排列。所有时刻均写死并通过固定时钟注入，
// 结论可重复运行，不依赖执行当天的日期，也不需要等待真实时间流逝。

// itemOutcomesSubSecondBase 是纳秒级排序回归的基准秒：
// 全部受理时刻都落在 2026-03-01 00:00:00 UTC 这一秒内。
var itemOutcomesSubSecondBase = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// newSubSecondAcceptanceBook 登记一条落在同一秒内的交接时间线：
//
//	seat-v1：单价 150 分，基准秒起生效，在第 100 毫秒被替代
//	seat-v2：单价 150 分，第 100 毫秒（含）起替代 seat-v1，持续有效
//
// 于是第 100 毫秒引用 v2 是正常确认，同一毫秒引用 v1 则以 version_expired
// 正常受理拒绝，方便构造“确认与拒绝混合、只相差一纳秒”的受理序列。
// now 是测试注入的固定时钟，登记时不读取时钟，受理时刻全部由测试显式拨表。
func newSubSecondAcceptanceBook(t *testing.T, now func() time.Time) *Book {
	t.Helper()
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: itemOutcomesSubSecondBase,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 150,
		Start:    itemOutcomesSubSecondBase.Add(100 * time.Millisecond),
		Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b
}

// TestItemOutcomesSubsecondOrderingOneNanosecond 保护纳秒级先后：
// 同一秒内第 100 毫秒与第 100 毫秒再加一纳秒受理的两笔记录不能被当成同刻。
// 后一笔的请求标识在字符串顺序上更靠前（q-a… < q-z…），较早一笔仍必须先列出；
// 列表中确认与正常受理的拒绝混合排列，不能按确认状态分组，也不能排除拒绝。
func TestItemOutcomesSubsecondOrderingOneNanosecond(t *testing.T) {
	// 固定时钟：报价前把时钟拨到写死的受理时刻，不等待任何真实时间。
	var clock time.Time
	setNow := func(at time.Time) { clock = at }
	b := newSubSecondAcceptanceBook(t, func() time.Time { return clock })

	t100ms := itemOutcomesSubSecondBase.Add(100 * time.Millisecond)
	t100msPlus1 := t100ms.Add(time.Nanosecond)
	t100msPlus2 := t100ms.Add(2 * time.Nanosecond)

	// 测试前提：三个时刻确实落在同一秒内且依次只差一纳秒，否则本回归不成立。
	if t100ms.Unix() != t100msPlus2.Unix() {
		t.Fatalf("三笔受理时刻应落在同一秒内: %v / %v", t100ms, t100msPlus2)
	}
	if t100msPlus1.Sub(t100ms) != time.Nanosecond || t100msPlus2.Sub(t100msPlus1) != time.Nanosecond {
		t.Fatalf("受理时刻间隔应为一纳秒: %v, %v", t100msPlus1.Sub(t100ms), t100msPlus2.Sub(t100msPlus1))
	}

	// ① 最早（100.000000ms）：引用新版确认，故意给字符串顺序最靠后的标识。
	setNow(t100ms)
	first, err := b.Quote(QuoteRequest{
		RequestID: "q-z-first-accepted", ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	})
	if err != nil || !first.Confirmed {
		t.Fatalf("最早一笔应为确认: %v %+v", err, first)
	}

	// ② 晚一纳秒（100.000001ms）：引用已失效旧版，被正常受理拒绝，
	//    故意给字符串顺序最靠前的标识——时刻先后必须压过标识顺序。
	setNow(t100msPlus1)
	rejected, err := b.Quote(QuoteRequest{
		RequestID: "q-a-second-accepted", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("晚一纳秒的拒绝应正常受理: %v", err)
	}
	if rejected.Confirmed || rejected.Reason != ReasonVersionExpired {
		t.Fatalf("晚一纳秒引用旧版应为 version_expired 拒绝: %+v", rejected)
	}

	// ③ 最晚（100.000002ms）：再次确认，标识字符串顺序居中。
	setNow(t100msPlus2)
	third, err := b.Quote(QuoteRequest{
		RequestID: "q-m-third-accepted", ItemID: "seat", VersionID: "seat-v2", Quantity: 1,
	})
	if err != nil || !third.Confirmed {
		t.Fatalf("最晚一笔应为确认: %v %+v", err, third)
	}

	// 正确顺序只能由完整时刻推出：q-z、q-a、q-m；
	// 纯请求标识顺序会是 q-a、q-m、q-z，按确认状态分组会是 q-z、q-m、q-a。
	wantIDs := []string{"q-z-first-accepted", "q-a-second-accepted", "q-m-third-accepted"}

	// map 遍历顺序随机：重复列取多轮，每轮都必须严格一致，保证排序不依赖遍历运气。
	var firstList []Outcome
	for round := 0; round < 30; round++ {
		got, err := b.ItemOutcomes("seat")
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if len(got) != 3 {
			t.Fatalf("round %d: 确认与拒绝都必须列出，应有 3 条, got %d: %+v", round, len(got), got)
		}
		if ids := requestIDs(got); !equalStrings(ids, wantIDs) {
			t.Fatalf("round %d: 纳秒级先后被打乱, want %v, got %v", round, wantIDs, ids)
		}
		if round == 0 {
			firstList = got
		}
	}

	got := firstList

	// 排序后每条记录仍保留首次受理的完整字段：完整小数时刻、请求来源、
	// 确认状态、金额或拒绝原因；两笔记录各自独立存在，不被合并。
	earlier := got[0]
	if earlier.Request.RequestID != "q-z-first-accepted" ||
		earlier.Request.ItemID != "seat" || earlier.Request.VersionID != "seat-v2" ||
		earlier.Request.Quantity != 4 || !earlier.Confirmed ||
		earlier.UnitPrice != 150 || earlier.Total != 600 || earlier.Reason != ReasonNone {
		t.Fatalf("150 分 × 数量 4 的确认必须保留 600 分与完整来源: %+v", earlier)
	}
	if !earlier.AcceptedAt.Equal(t100ms) || earlier.AcceptedAt.UnixNano() != t100ms.UnixNano() {
		t.Fatalf("最早一笔的受理时刻小数部分被改写: got %v, want %v", earlier.AcceptedAt, t100ms)
	}

	later := got[1]
	if later.Request.RequestID != "q-a-second-accepted" ||
		later.Request.ItemID != "seat" || later.Request.VersionID != "seat-v1" ||
		later.Request.Quantity != 4 || later.Confirmed ||
		later.Reason != ReasonVersionExpired || later.UnitPrice != 0 || later.Total != 0 {
		t.Fatalf("version_expired 拒绝必须保留原因与零金额，不能变成免费确认价: %+v", later)
	}
	if !later.AcceptedAt.Equal(t100msPlus1) || later.AcceptedAt.UnixNano() != t100msPlus1.UnixNano() {
		t.Fatalf("拒绝一笔的受理时刻小数部分被改写: got %v, want %v", later.AcceptedAt, t100msPlus1)
	}

	// 相邻两条相差恰好一纳秒，且同在一个整秒内：小数部分没有被舍去后写回结果。
	if gap := got[1].AcceptedAt.Sub(got[0].AcceptedAt); gap != time.Nanosecond {
		t.Fatalf("前两条受理时刻间隔应为一纳秒, got %v", gap)
	}
	if gap := got[2].AcceptedAt.Sub(got[1].AcceptedAt); gap != time.Nanosecond {
		t.Fatalf("后两条受理时刻间隔应为一纳秒, got %v", gap)
	}
	if got[0].AcceptedAt.Unix() != got[1].AcceptedAt.Unix() {
		t.Fatalf("前两条应落在同一整秒内，仅小数部分不同")
	}
	if got[2].UnitPrice != 150 || got[2].Total != 150 {
		t.Fatalf("第三笔确认金额异常: %+v", got[2])
	}

	// 单笔查询与列表一致：排序逻辑不能影响已保存的首次受理内容。
	if lookup, err := b.Lookup("q-z-first-accepted"); err != nil || lookup != first {
		t.Fatalf("Lookup 最早一笔与首次结果不一致: %v %+v vs %+v", err, lookup, first)
	}
	if lookup, err := b.Lookup("q-a-second-accepted"); err != nil || lookup != rejected {
		t.Fatalf("Lookup 拒绝一笔与首次结果不一致: %v %+v vs %+v", err, lookup, rejected)
	}
}

// TestItemOutcomesSameInstantDifferentZoneTieBreak 保护时区同刻判定：
// 2026-03-01 00:00:00.1 UTC 与 08:00:00.1 +08:00 是同一瞬间，
// 表示不同不能让列表失去同刻规则——同刻只按请求标识字符串顺序排列，
// 两笔记录都必须保留，小数部分也不能丢。
func TestItemOutcomesSameInstantDifferentZoneTieBreak(t *testing.T) {
	var clock time.Time
	b := NewBook(WithClock(func() time.Time { return clock }))
	versionStart := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150, Start: versionStart,
	}); err != nil {
		t.Fatal(err)
	}

	east := time.FixedZone("UTC+8", 8*3600)
	tUTC := time.Date(2026, 3, 1, 0, 0, 0, 100000000, time.UTC)
	tEast := time.Date(2026, 3, 1, 8, 0, 0, 100000000, east)

	// 测试前提：两者确为同一瞬间，但时区表示与时间文本不同。
	if !tUTC.Equal(tEast) || tUTC.UnixNano() != tEast.UnixNano() {
		t.Fatalf("测试时间构造错误：%v 与 %v 应为同一瞬间", tUTC, tEast)
	}
	if tUTC.Format(time.RFC3339) == tEast.Format(time.RFC3339) {
		t.Fatalf("测试时间构造错误：两者文本表示应不同")
	}

	// 先提交 +08:00 表示且标识靠后的一笔，再提交 UTC 表示且标识靠前的一笔。
	clock = tEast
	eastOut, err := b.Quote(QuoteRequest{
		RequestID: "q-z-east-repr", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil || !eastOut.Confirmed || eastOut.Total != 600 {
		t.Fatalf("+08:00 表示的受理应正常确认: %v %+v", err, eastOut)
	}
	clock = tUTC
	utcOut, err := b.Quote(QuoteRequest{
		RequestID: "q-a-utc-repr", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil || !utcOut.Confirmed || utcOut.Total != 600 {
		t.Fatalf("UTC 表示的受理应正常确认: %v %+v", err, utcOut)
	}

	wantIDs := []string{"q-a-utc-repr", "q-z-east-repr"}
	for round := 0; round < 30; round++ {
		got, err := b.ItemOutcomes("seat")
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		// 同刻两笔都必须存在，不能因时刻相等被合并。
		if len(got) != 2 {
			t.Fatalf("round %d: 同刻两笔必须各自保留, got %d: %+v", round, len(got), got)
		}
		if ids := requestIDs(got); !equalStrings(ids, wantIDs) {
			t.Fatalf("round %d: 同刻应只按请求标识排序, want %v, got %v", round, wantIDs, ids)
		}
	}

	got, _ := b.ItemOutcomes("seat")
	// 两条记录的完整受理时刻（含一亿纳秒小数）都原样保留，可按任一表示核对。
	for _, o := range got {
		if !o.AcceptedAt.Equal(tUTC) || o.AcceptedAt.UnixNano() != tUTC.UnixNano() {
			t.Fatalf("同刻记录的完整受理时刻被改写: %+v", o)
		}
		if o.AcceptedAt.Nanosecond() != 100000000 {
			t.Fatalf("受理时刻的小数部分被舍去: %v", o.AcceptedAt)
		}
		if o.Request.ItemID != "seat" || !o.Confirmed || o.UnitPrice != 150 || o.Total != 600 {
			t.Fatalf("同刻记录必须保留请求来源与确认金额: %+v", o)
		}
	}
}

// TestItemOutcomesOrdersByInstantAcrossDates 保护跨日期显示时仍按真实时刻排列：
// 日历日期、显示的小时数或时间文本都不能成为排序依据。
func TestItemOutcomesOrdersByInstantAcrossDates(t *testing.T) {
	var clock time.Time
	b := NewBook(WithClock(func() time.Time { return clock }))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	east := time.FixedZone("UTC+8", 8*3600)

	// 真实更早的一笔：+08:00 显示为 3 月 1 日 00:00（UTC 实为 2 月 28 日 16:00）。
	earlier := time.Date(2026, 3, 1, 0, 0, 0, 0, east)
	// 真实更晚的一笔：UTC 显示仍停留在 2 月 28 日 23:00（+08:00 实为 3 月 1 日 07:00）。
	later := time.Date(2026, 2, 28, 23, 0, 0, 0, time.UTC)

	// 测试前提：真实先后与显示的日历日期相反——按日期文本排序会得到错误结论。
	if !later.After(earlier) {
		t.Fatalf("测试时间构造错误：%v 应晚于 %v", later, earlier)
	}
	if earlier.Format("2006-01-02") <= later.Format("2006-01-02") {
		t.Fatalf("测试时间构造错误：较早一笔显示的日历日期应更晚, got %s vs %s",
			earlier.Format("2006-01-02"), later.Format("2006-01-02"))
	}

	// 先提交真实更晚（显示日期更早）的一笔，再提交真实更早（显示日期更晚）的一笔，
	// 标识顺序也与真实先后相反，避免任何次级规则“碰巧”给出正确答案。
	clock = later
	laterOut, err := b.Quote(QuoteRequest{
		RequestID: "q-ccc-later-instant", ItemID: "seat", VersionID: "seat-v1", Quantity: 1,
	})
	if err != nil || !laterOut.Confirmed {
		t.Fatalf("真实更晚一笔受理失败: %v %+v", err, laterOut)
	}
	clock = earlier
	earlierOut, err := b.Quote(QuoteRequest{
		RequestID: "q-bbb-earlier-instant", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil || !earlierOut.Confirmed || earlierOut.Total != 600 {
		t.Fatalf("真实更早一笔受理失败: %v %+v", err, earlierOut)
	}

	wantIDs := []string{"q-bbb-earlier-instant", "q-ccc-later-instant"}
	for round := 0; round < 30; round++ {
		got, err := b.ItemOutcomes("seat")
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if len(got) != 2 {
			t.Fatalf("round %d: 应有 2 条记录, got %d: %+v", round, len(got), got)
		}
		if ids := requestIDs(got); !equalStrings(ids, wantIDs) {
			t.Fatalf("round %d: 跨日期记录必须按真实时刻排列, want %v, got %v", round, wantIDs, ids)
		}
		if !got[0].AcceptedAt.Before(got[1].AcceptedAt) {
			t.Fatalf("round %d: 列表顺序不满足实际时刻从早到晚: %v !< %v",
				round, got[0].AcceptedAt, got[1].AcceptedAt)
		}
	}

	got, _ := b.ItemOutcomes("seat")
	if !got[0].AcceptedAt.Equal(earlier) || got[0].Total != 600 {
		t.Fatalf("较早一笔的完整时刻与金额必须保留: %+v", got[0])
	}
	if !got[1].AcceptedAt.Equal(later) || got[1].Total != 150 {
		t.Fatalf("较晚一笔的完整时刻与金额必须保留: %+v", got[1])
	}
}

// TestItemOutcomesSameInstantAcrossDateBoundary 补一对跨日期的同刻记录：
// UTC 前一天晚上 == +08:00 次日清晨，日历日期不同也只能按请求标识排列。
func TestItemOutcomesSameInstantAcrossDateBoundary(t *testing.T) {
	var clock time.Time
	b := NewBook(WithClock(func() time.Time { return clock }))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	east := time.FixedZone("UTC+8", 8*3600)
	nightUTC := time.Date(2026, 2, 28, 23, 59, 59, 100000000, time.UTC)
	morningEast := time.Date(2026, 3, 1, 7, 59, 59, 100000000, east)
	if !nightUTC.Equal(morningEast) {
		t.Fatalf("测试时间构造错误：%v 与 %v 应为同一瞬间", nightUTC, morningEast)
	}
	if nightUTC.Format("2006-01-02") == morningEast.Format("2006-01-02") {
		t.Fatalf("测试时间构造错误：两者显示的日历日期应不同")
	}

	clock = nightUTC
	if _, err := b.Quote(QuoteRequest{
		RequestID: "q-z-utc-night", ItemID: "seat", VersionID: "seat-v1", Quantity: 1,
	}); err != nil {
		t.Fatal(err)
	}
	clock = morningEast
	if _, err := b.Quote(QuoteRequest{
		RequestID: "q-a-east-morning", ItemID: "seat", VersionID: "seat-v1", Quantity: 1,
	}); err != nil {
		t.Fatal(err)
	}

	wantIDs := []string{"q-a-east-morning", "q-z-utc-night"}
	for round := 0; round < 30; round++ {
		got, err := b.ItemOutcomes("seat")
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if ids := requestIDs(got); !equalStrings(ids, wantIDs) {
			t.Fatalf("round %d: 跨日期同刻只能按请求标识排列, want %v, got %v",
				round, wantIDs, ids)
		}
		if !got[0].AcceptedAt.Equal(got[1].AcceptedAt) {
			t.Fatalf("round %d: 两条记录应判定为同刻", round)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
