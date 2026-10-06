package tariff

import (
	"testing"
	"time"
)

// ItemOutcomes 排序回归测试的固定时间线（受理时刻全部由注入时钟推进到
// 确定值，结论不依赖运行当天的真实日期，也不需要等待真实时间流逝）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 起，登记结束 2026-03-31（不含）
//	新版 seat-v2：单价 180 分，2026-03-10 起替代 v1，持续有效
var (
	ordV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ordV1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	ordHandoff = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
)

// newOrderingBook 返回登记好 v1、v2 两个版本的账本和推进受理时钟的函数。
func newOrderingBook(t *testing.T) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(ordV1Start)
	b := NewBook(WithClock(now))
	v1End := ordV1End
	for _, req := range []RegisterRequest{
		{ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150, Start: ordV1Start, End: &v1End},
		{ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180, Start: ordHandoff, Replaces: "seat-v1"},
	} {
		if err := b.RegisterVersion(req); err != nil {
			t.Fatalf("register %s: %v", req.VersionID, err)
		}
	}
	return b, setNow
}

// 同一费率项的记录即使落在同一秒内，也必须按完整受理时刻排列：
// 相差一纳秒不能被当成同刻。较早受理的一笔即使请求标识在字符串顺序上
// 更靠后，仍应先列出——若实现把受理时刻截断到秒再按标识tie-break，
// 本用例的顺序就会颠倒。确认与正常受理的拒绝混合在同一列表中时，
// 也按同一时刻规则排列，不因确认状态不同而改变先后。
func TestItemOutcomesSubSecondOrdering(t *testing.T) {
	b, setNow := newOrderingBook(t)

	// ① 旧版有效期内按 150 分、数量 4 确认 600 分。
	confirmedAt := time.Date(2026, 3, 2, 10, 0, 0, 100_000_000, time.UTC)
	setNow(confirmedAt)
	confirmed, err := b.Quote(QuoteRequest{
		RequestID: "q-c-confirmed", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirmed quote: %v %+v", err, confirmed)
	}

	// ②③ 交接后的同一秒内，.100000000 与 .100000001（相差一纳秒）各受理一笔：
	// 较早的是引用已失效旧版的拒绝，较晚的是引用新版的确认。
	// 较早一笔故意取字符串顺序更靠后的标识，提交顺序也故意颠倒，
	// 验证排序只看完整受理时刻，不看标识、状态或提交顺序。
	sec := time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	expiredAt := sec.Add(100 * time.Millisecond)     // .100000000
	newConfirmedAt := expiredAt.Add(time.Nanosecond) // .100000001

	setNow(newConfirmedAt)
	newConfirmed, err := b.Quote(QuoteRequest{
		RequestID: "q-x-confirmed", ItemID: "seat", VersionID: "seat-v2", Quantity: 1,
	})
	if err != nil || !newConfirmed.Confirmed {
		t.Fatalf("new confirmed quote: %v %+v", err, newConfirmed)
	}

	setNow(expiredAt)
	expired, err := b.Quote(QuoteRequest{
		RequestID: "q-y-expired", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if expired.Confirmed || expired.Reason != ReasonVersionExpired {
		t.Fatalf("want version_expired rejection: %+v", expired)
	}

	got, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 outcomes, got %d: %+v", len(got), got)
	}

	// 按完整首次受理时刻从早到晚：3 月 2 日的确认最先；
	// 同一秒内的两笔按纳秒级先后，拒绝排在确认之前只是因为受理更早。
	wantOrder := []string{"q-c-confirmed", "q-y-expired", "q-x-confirmed"}
	for i, id := range wantOrder {
		if got[i].Request.RequestID != id {
			t.Fatalf("position %d: want %s, got %s (full: %v)",
				i, id, got[i].Request.RequestID, requestIDs(got))
		}
	}

	// 排序后每笔记录仍保留首次受理的完整时刻（小数部分不被舍去）、
	// 请求来源、确认状态以及金额或拒绝原因。
	old := got[0]
	if !old.Confirmed || old.Request.ItemID != "seat" || old.Request.VersionID != "seat-v1" ||
		old.Request.Quantity != 4 || old.UnitPrice != 150 || old.Total != 600 ||
		old.Reason != ReasonNone {
		t.Fatalf("old confirmation must keep 150x4=600 first-result values: %+v", old)
	}
	if !old.AcceptedAt.Equal(confirmedAt) || old.AcceptedAt.Nanosecond() != 100_000_000 {
		t.Fatalf("confirmed record must keep full acceptance time %v, got %v", confirmedAt, old.AcceptedAt)
	}

	rej := got[1]
	if rej.Confirmed || rej.Reason != ReasonVersionExpired ||
		rej.Request.VersionID != "seat-v1" || rej.UnitPrice != 0 || rej.Total != 0 {
		t.Fatalf("expired rejection must keep version_expired and zero amounts: %+v", rej)
	}
	if !rej.AcceptedAt.Equal(expiredAt) || rej.AcceptedAt.Nanosecond() != 100_000_000 {
		t.Fatalf("rejection must keep full acceptance time %v, got %v", expiredAt, rej.AcceptedAt)
	}

	later := got[2]
	if !later.Confirmed || later.UnitPrice != 180 || later.Total != 180 {
		t.Fatalf("later confirmation must keep its amounts: %+v", later)
	}
	if !later.AcceptedAt.Equal(newConfirmedAt) || later.AcceptedAt.Nanosecond() != 100_000_001 {
		t.Fatalf("later record must keep nanosecond fraction %v, got %v", newConfirmedAt, later.AcceptedAt)
	}
	// 一纳秒之差是真实先后，不是同刻：两笔的受理时刻不能被判为相等。
	if rej.AcceptedAt.Equal(later.AcceptedAt) {
		t.Fatalf("records one nanosecond apart must not be treated as the same instant: %v vs %v",
			rej.AcceptedAt, later.AcceptedAt)
	}
}

// 时区不同不代表时刻不同：同一瞬间的不同时区表示是同刻，两笔之间只按
// 请求标识排列；真实时刻不同的记录按实际先后排列，不能只比较显示的
// 小时数或时间文本，日历日期显示差异也不是额外的排序依据。
func TestItemOutcomesTimezoneOrdering(t *testing.T) {
	cn := time.FixedZone("UTC+08:00", 8*60*60)

	// 版本从 2026-02-01 起持续有效，下列报价全部确认，专注验证排序。
	now, setNow := fixedClock(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	// 跨日期显示：UTC 的 2 月 28 日 23:00 与 +08:00 的 3 月 1 日 07:00
	// 是同一瞬间；而 +08:00 显示为 3 月 1 日 06:00 的时刻实际是
	// UTC 2 月 28 日 22:00，比前者更早——显示的日历日期更晚不代表时刻更晚。
	utcLateEve := time.Date(2026, 2, 28, 23, 0, 0, 0, time.UTC) // 真实时刻较晚
	cnMorning := time.Date(2026, 3, 1, 6, 0, 0, 0, cn)          // = 2 月 28 日 22:00Z，真实时刻较早

	// 同刻的两种时区表示：2026-03-01T00:00:00.100000000Z 与
	// 2026-03-01T08:00:00.100000000+08:00 是同一瞬间，只按请求标识排列。
	sameUTC := time.Date(2026, 3, 1, 0, 0, 0, 100_000_000, time.UTC)
	sameCN := time.Date(2026, 3, 1, 8, 0, 0, 100_000_000, cn)

	// 故意乱序提交，验证结果不依赖提交顺序。
	quotes := []struct {
		id string
		at time.Time
	}{
		{"q-b-same-instant", sameUTC},
		{"q-utc-late-eve", utcLateEve},
		{"q-a-same-instant", sameCN},
		{"q-cn-early-morning", cnMorning},
	}
	for _, q := range quotes {
		setNow(q.at)
		out, err := b.Quote(QuoteRequest{
			RequestID: q.id, ItemID: "seat", VersionID: "seat-v1", Quantity: 1,
		})
		if err != nil || !out.Confirmed {
			t.Fatalf("quote %s: %v %+v", q.id, err, out)
		}
	}

	got, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	// 同刻的两笔都必须在列表中，不能被合并。
	if len(got) != 4 {
		t.Fatalf("want 4 outcomes (same-instant records must not be merged), got %d: %+v",
			len(got), got)
	}

	// 真实先后：cn-early-morning（22:00Z）< utc-late-eve（23:00Z）< 同刻两笔；
	// 同刻两笔按请求标识：q-a-same-instant 在 q-b-same-instant 之前。
	wantOrder := []string{
		"q-cn-early-morning", "q-utc-late-eve", "q-a-same-instant", "q-b-same-instant",
	}
	for i, id := range wantOrder {
		if got[i].Request.RequestID != id {
			t.Fatalf("position %d: want %s, got %s (full: %v)",
				i, id, got[i].Request.RequestID, requestIDs(got))
		}
	}

	// 跨日期显示的两笔按真实时刻排列：+08:00 显示为 3 月 1 日清晨的一笔，
	// 实际早于 UTC 显示为 2 月 28 日深夜的一笔。
	if !got[0].AcceptedAt.Before(got[1].AcceptedAt) {
		t.Fatalf("cross-date records must order by real instant: %v should be before %v",
			got[0].AcceptedAt, got[1].AcceptedAt)
	}
	if !got[0].AcceptedAt.Equal(cnMorning) || !got[1].AcceptedAt.Equal(utcLateEve) {
		t.Fatalf("cross-date records must keep their acceptance times: %v, %v",
			got[0].AcceptedAt, got[1].AcceptedAt)
	}

	// 同刻两笔：受理时刻按真实瞬间相等，小数部分不被舍去，
	// 先后只由请求标识决定。
	first, second := got[2], got[3]
	if !first.AcceptedAt.Equal(second.AcceptedAt) {
		t.Fatalf("different timezone representations of the same instant must compare equal: %v vs %v",
			first.AcceptedAt, second.AcceptedAt)
	}
	if !first.AcceptedAt.Equal(sameUTC) || !second.AcceptedAt.Equal(sameCN) {
		t.Fatalf("same-instant records must keep their acceptance times: %v, %v",
			first.AcceptedAt, second.AcceptedAt)
	}
	if first.AcceptedAt.Nanosecond() != 100_000_000 || second.AcceptedAt.Nanosecond() != 100_000_000 {
		t.Fatalf("sub-second fraction must be preserved: %d, %d",
			first.AcceptedAt.Nanosecond(), second.AcceptedAt.Nanosecond())
	}
}
