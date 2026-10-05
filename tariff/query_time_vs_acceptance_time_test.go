package tariff

import (
	"errors"
	"testing"
	"time"
)

// 本组回归保护“按时刻选版后再发起报价”这一使用过程中已经存在的约定：
//
//	EffectiveVersionAt 回答的是调用方“所查时刻”生效的版本；
//	Quote 则按新请求“首次受理的时刻”（账本时钟）判断指定版本是否可用。
//
// 两者可以、而且在本场景中必然使用不同的时刻：查询结果只是只读的版本视图，
// 不能成为随后报价的时间依据。因此——
//
//   - 在过去时刻查到旧版仍有效，不代表稍后（旧版已被替代后）受理的报价
//     还能按旧版确认；报价只认首次受理时刻。
//   - 对未来时刻的查询失败（该时刻无生效版本）不改变账本时钟，也不留下
//     任何状态；回到原来的受理时刻引用当时有效的新版，仍应正常确认。
//
// 本组测试只走现有的公开入口（EffectiveVersionAt / Quote / Lookup），
// 不新增入口，也不改变拒绝的含义（version_expired、ErrNoEffectiveVersion 等）。
//
// 固定时间线（均为 UTC，结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00 起，登记结束 2026-03-31 00:00
//	新版 seat-v2：单价 180 分，2026-03-10 00:00 起替代旧版，2026-03-20 00:00 结束
//	查询时刻一：2026-03-09 00:00（旧版实际有效，登记结束尚未到）
//	报价首次受理时刻：2026-03-15 10:00（旧版实际有效期已止于 3 月 10 日）
//	查询时刻二：2026-03-20 00:00（新版结束时刻，不含；两版都不生效）
var (
	qqV1Start     = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	qqV1RegEnd    = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)  // 旧版登记结束（不含）
	qqV2Start     = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)  // 新版替代旧版的交接点
	qqV2End       = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)  // 新版结束（不含）
	qqPastQuery   = time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)   // 查询时刻一：过去
	qqAcceptAt    = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC) // 报价首次受理时刻
	qqFutureQuery = qqV2End                                       // 查询时刻二：未来的结束点
)

// newQueryQuoteBook 返回一本已登记上述旧版/新版交接关系的账本，
// 账本时钟固定在报价首次受理时刻 2026-03-15 10:00。
func newQueryQuoteBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	oldEnd := qqV1RegEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: qqV1Start, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	newEnd := qqV2End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: qqV2Start, End: &newEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// TestEffectiveQueryDoesNotSetQuoteAcceptanceTime 围绕同一费率项走一遍完整
// 使用过程：先在过去时刻查到旧版，再在更晚的受理时刻引用旧版（应拒绝）；
// 再对未来的“无生效版本”时刻查询失败后，回到原受理时刻引用新版（应确认）。
// 两次报价的首次受理时刻都必须是 3 月 15 日 10:00，与各自之前的查询时刻无关。
func TestEffectiveQueryDoesNotSetQuoteAcceptanceTime(t *testing.T) {
	// 账本时钟自始至终固定在报价受理时刻 3 月 15 日 10:00，全程不推进。
	b, setNow := newQueryQuoteBook(t, qqAcceptAt)

	// ① 查询 3 月 9 日：当时生效的是旧版 seat-v1。
	old, err := b.EffectiveVersionAt("seat", qqPastQuery)
	if err != nil {
		t.Fatalf("3 月 9 日应查到生效的旧版: %v", err)
	}
	if old.ItemID != "seat" || old.VersionID != "seat-v1" || old.UnitPrice != 150 {
		t.Fatalf("3 月 9 日选错版本: %+v", old)
	}
	// 同一份视图必须能区分“登记结束”和“实际结束”：
	// 登记结束仍是 3 月 31 日（尚未到），但旧版已被新版替代，
	// 实际有效区间止于交接点 3 月 10 日。
	if old.End == nil || !old.End.Equal(qqV1RegEnd) {
		t.Fatalf("旧版登记结束应为 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(qqV2Start) {
		t.Fatalf("旧版实际结束应为交接点 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.End.Equal(*old.EffectiveEnd) {
		t.Fatal("登记结束与实际结束必须是两个不同的边界，不能混为同一个值")
	}
	if old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版应显示由 seat-v2 替代: %q", old.SupersededBy)
	}

	// ② 用从未使用过的请求标识引用旧版报价，数量 4。
	//    受理时刻仍是账本时钟 3 月 15 日 10:00，而不是上面查询所用的 3 月 9 日：
	//    查询结果不能成为报价的时间依据。旧版实际止于 3 月 10 日，受理时已到期。
	setNow(qqAcceptAt) // 显式确认时钟仍在原受理时刻，未被查询带动。
	rejectedReq := QuoteRequest{
		RequestID: "qq-old-version-after-query",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	}
	rejected, err := b.Quote(rejectedReq)
	if err != nil {
		t.Fatalf("引用已失效版本是正常受理后拒绝，调用错误应为空, got %v", err)
	}
	if rejected.Confirmed {
		t.Fatalf("旧版在 3 月 15 日已失效，不能确认，更不能按 3 月 9 日的查询结果计价: %+v", rejected)
	}
	if rejected.Reason != ReasonVersionExpired {
		t.Fatalf("拒绝原因应为 version_expired, got %q", rejected.Reason)
	}
	// 拒绝结果单价和总价均为零——零金额不代表登记单价为零。
	if rejected.UnitPrice != 0 || rejected.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: 单价=%d 总价=%d", rejected.UnitPrice, rejected.Total)
	}
	// 结果保留提交的费率项、旧版标识和数量，不能自动改用新版确认。
	if rejected.Request != rejectedReq {
		t.Fatalf("拒绝结果必须原样保留请求（费率项/旧版标识/数量）: %+v vs %+v",
			rejected.Request, rejectedReq)
	}
	if rejected.Request.VersionID == "seat-v2" {
		t.Fatalf("引用旧版不能被自动改成新版: %+v", rejected.Request)
	}
	// 首次受理时刻必须是 3 月 15 日 10:00，绝不能写成查询时刻 3 月 9 日。
	if !rejected.AcceptedAt.Equal(qqAcceptAt) {
		t.Fatalf("首次受理时刻应为 3 月 15 日 10:00, got %v", rejected.AcceptedAt)
	}
	if rejected.AcceptedAt.Equal(qqPastQuery) {
		t.Fatalf("受理时刻被错误地写成了查询时刻 3 月 9 日: %v", rejected.AcceptedAt)
	}

	// ③ 再查询未来的 3 月 20 日零点（新版结束时刻，不含）：
	//    此时没有任何生效版本，旧版不会因为新版到期而重新生效。
	if _, err := b.EffectiveVersionAt("seat", qqFutureQuery); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("3 月 20 日零点应返回 ErrNoEffectiveVersion, got %v", err)
	}

	// ④ 未来时刻的查询失败之后，受理时刻仍是原来的 3 月 15 日 10:00。
	//    换用另一个从未使用过的标识引用新版：新版在受理时刻有效，确认 180×4=720。
	setNow(qqAcceptAt)
	confirmedReq := QuoteRequest{
		RequestID: "qq-new-version-after-future-query",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	}
	confirmed, err := b.Quote(confirmedReq)
	if err != nil {
		t.Fatalf("引用受理时刻有效的新版应正常受理，调用错误应为空, got %v", err)
	}
	if !confirmed.Confirmed {
		t.Fatalf("新版在 3 月 15 日有效，应确认，未来查询失败不能污染本次报价: %+v", confirmed)
	}
	if confirmed.UnitPrice != 180 || confirmed.Total != 720 {
		t.Fatalf("应按新版 180 分确认总价 720 分: 单价=%d 总价=%d",
			confirmed.UnitPrice, confirmed.Total)
	}
	if confirmed.Reason != ReasonNone {
		t.Fatalf("确认结果拒绝原因为空, got %q", confirmed.Reason)
	}
	// 来源是调用方指定的新版。
	if confirmed.Request != confirmedReq || confirmed.Request.VersionID != "seat-v2" {
		t.Fatalf("确认结果必须保留新版来源: %+v vs %+v", confirmed.Request, confirmedReq)
	}
	// 受理时刻仍是 3 月 15 日 10:00，不能写成未来查询时刻 3 月 20 日。
	if !confirmed.AcceptedAt.Equal(qqAcceptAt) {
		t.Fatalf("首次受理时刻应为 3 月 15 日 10:00, got %v", confirmed.AcceptedAt)
	}
	if confirmed.AcceptedAt.Equal(qqFutureQuery) {
		t.Fatalf("受理时刻被错误地写成了未来查询时刻 3 月 20 日: %v", confirmed.AcceptedAt)
	}

	// ⑤ 两笔报价都应能按各自标识查到，且与首次返回的结果逐条一致。
	gotRejected, err := b.Lookup(rejectedReq.RequestID)
	if err != nil {
		t.Fatalf("被拒绝的首次结果也应可查: %v", err)
	}
	if gotRejected != rejected {
		t.Fatalf("拒绝结果 Lookup 与首次返回不一致: %+v vs %+v", gotRejected, rejected)
	}
	gotConfirmed, err := b.Lookup(confirmedReq.RequestID)
	if err != nil {
		t.Fatalf("已确认的首次结果应可查: %v", err)
	}
	if gotConfirmed != confirmed {
		t.Fatalf("确认结果 Lookup 与首次返回不一致: %+v vs %+v", gotConfirmed, confirmed)
	}

	// 原样重试（相同标识、相同内容）同样返回各自的首次结果，不按当前账本重算。
	replayRejected, err := b.Quote(rejectedReq)
	if err != nil || replayRejected != rejected {
		t.Fatalf("拒绝结果原样重试应返回首次结果: %+v (%v), first %+v",
			replayRejected, err, rejected)
	}
	replayConfirmed, err := b.Quote(confirmedReq)
	if err != nil || replayConfirmed != confirmed {
		t.Fatalf("确认结果原样重试应返回首次结果: %+v (%v), first %+v",
			replayConfirmed, err, confirmed)
	}
}

// TestQuoteAcceptanceTimeUnaffectedByQueries 从另一个角度锁定同一约定：
// 在相同的受理时刻，无论此前对多少个不同时刻（过去命中、未来落空）做过
// 查询，报价结论都必须与“完全不查询直接报价”一致——查询不推进账本时钟，
// 也不留下任何能影响 Quote 的时间状态。
func TestQuoteAcceptanceTimeUnaffectedByQueries(t *testing.T) {
	// 账本 A：注册后不做任何查询，直接在受理时刻报价。
	bookA, _ := newQueryQuoteBook(t, qqAcceptAt)

	// 账本 B：注册后先对一串时刻反复查询（含成功与失败），再报同样的请求。
	bookB, setNowB := newQueryQuoteBook(t, qqAcceptAt)
	queryTimes := []time.Time{
		qqPastQuery,   // 过去：命中旧版
		qqV2Start,     // 交接点：命中新版
		qqAcceptAt,    // 受理时刻本身：命中新版
		qqFutureQuery, // 未来结束点：无生效版本
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), // 更远的未来：无生效版本
		qqPastQuery,   // 再回到过去重复查询
		qqFutureQuery, // 再重复一次失败查询
	}
	for i, at := range queryTimes {
		_, _ = bookB.EffectiveVersionAt("seat", at) // 结论如何都不应影响后面的报价
		// 显式把时钟拨回受理时刻，证明报价只依赖这个时钟而非任何查询参数。
		setNowB(qqAcceptAt)
		_ = i
	}

	// 两本账用完全相同的请求（含相同标识）引用新版，结果必须逐条相等。
	newReq := QuoteRequest{
		RequestID: "qqcmp-new-version", ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	}
	outA, errA := bookA.Quote(newReq)
	outB, errB := bookB.Quote(newReq)
	if errA != nil || errB != nil {
		t.Fatalf("两本账引用新版都不应返回调用错误: %v %v", errA, errB)
	}
	if outA != outB {
		t.Fatalf("查询不应改变报价结果: 无查询=%+v 多次查询=%+v", outA, outB)
	}
	if !outB.Confirmed || outB.UnitPrice != 180 || outB.Total != 720 ||
		outB.Reason != ReasonNone || !outB.AcceptedAt.Equal(qqAcceptAt) {
		t.Fatalf("受理时刻引用新版应确认 720 分且受理时刻为 3 月 15 日 10:00: %+v", outB)
	}

	// 同样的请求引用旧版：两本账都应按受理时刻判定 version_expired，金额为零，
	// 而不会因为账本 B 查过 3 月 9 日（旧版当时有效）就改成确认。
	oldReq := QuoteRequest{
		RequestID: "qqcmp-old-version", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	}
	oldA, errA := bookA.Quote(oldReq)
	oldB, errB := bookB.Quote(oldReq)
	if errA != nil || errB != nil {
		t.Fatalf("两本账引用旧版都不应返回调用错误: %v %v", errA, errB)
	}
	if oldA != oldB {
		t.Fatalf("查询不应改变旧版报价的拒绝结果: 无查询=%+v 多次查询=%+v", oldA, oldB)
	}
	if oldB.Confirmed || oldB.Reason != ReasonVersionExpired ||
		oldB.UnitPrice != 0 || oldB.Total != 0 ||
		!oldB.AcceptedAt.Equal(qqAcceptAt) || oldB.Request != oldReq {
		t.Fatalf("受理时刻引用旧版应为 version_expired 拒绝、金额为零并保留原请求: %+v", oldB)
	}
}
