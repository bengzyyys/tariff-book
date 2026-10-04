package tariff

import (
	"errors"
	"testing"
	"time"
)

// “后来补登过去生效的替代版本”场景的固定时间线（全部按 UTC 解释，结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00
//	新版 seat-v2：单价 180 分，2026-03-10 00:00 起替代旧版，不填结束时间
//	报价受理时刻 retroAcceptedAt：2026-03-15 10:00——首次报价时旧版尚未被替代；
//	                           随后仍在这一受理时刻补登从 5 天前就已生效的新版。
//
// 登记只按旧版当前的实际有效期判断交接点是否合法，允许交接点早于登记当天，
// 因此补登成功后旧版的实际结束被提前到 3 月 10 日零点，
// 早于那笔 600 分确认报价的受理时刻。所有时刻写死并注入固定时钟，
// 结论不依赖运行当天或真实时间等待。
var (
	retroOldStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	retroOldEnd     = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	retroHandoff    = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	retroAcceptedAt = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
)

// 场景中各笔报价各自使用独立的非空请求标识。
const (
	retroIDOriginal = "retro-original-600" // 补登前已确认的 600 分报价
	retroIDExpired  = "retro-old-expired"  // 补登后用新标识引用旧版
	retroIDNew      = "retro-new-720"      // 补登后用新标识引用新版
)

// newRetroactiveBookWithConfirmedQuote 建立补登场景的起点：
// 只登记旧版 seat-v1，时钟固定在 3 月 15 日 10 点，
// 此时旧版尚未被替代，用原标识以数量 4 取得 150 分单价、600 分总价的确认。
// 返回账本、推进时钟的函数、原请求与首次确认结果。
func newRetroactiveBookWithConfirmedQuote(t *testing.T) (*Book, func(time.Time), QuoteRequest, Outcome) {
	t.Helper()
	// 场景前提：交接点早于报价受理时刻，且受理时刻仍在旧版登记的有效期内。
	if !retroHandoff.Before(retroAcceptedAt) {
		t.Fatal("测试前提不成立：新版生效时刻应早于报价受理时刻")
	}
	if !retroAcceptedAt.Before(retroOldEnd) {
		t.Fatal("测试前提不成立：报价受理时刻应早于旧版登记结束")
	}

	now, setNow := fixedClock(retroAcceptedAt)
	b := NewBook(WithClock(now))
	oldEnd := retroOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: retroOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}

	req := QuoteRequest{
		RequestID: retroIDOriginal, ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	}
	first, err := b.Quote(req)
	if err != nil {
		t.Fatalf("旧版尚未被替代时报价应正常受理: %v", err)
	}
	if !first.Confirmed || first.UnitPrice != 150 || first.Total != 600 {
		t.Fatalf("补登前报价应按 150 分确认 600 分: %+v", first)
	}
	if !first.AcceptedAt.Equal(retroAcceptedAt) || first.Reason != ReasonNone {
		t.Fatalf("首次结果受理时刻/拒绝原因异常: %+v", first)
	}
	return b, setNow, req, first
}

// registerRetroactiveReplacement 仍停在报价受理时刻补登新版 seat-v2：
// 单价 180 分，从 3 月 10 日零点起替代旧版，不填结束时间。
// 同项没有其他版本占用新版的有效时间，登记必须成功。
func registerRetroactiveReplacement(t *testing.T, b *Book) {
	t.Helper()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: retroHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("在受理时刻补登过去生效、且无其他版本占用其时间的新版应成功: %v", err)
	}
}

// 补登成功后：旧版登记结束仍是 3 月 31 日，实际有效结束被提前到 3 月 10 日，
// 新旧版本互相可见替代关系；新版从 3 月 10 日起持续有效。
func TestRetroactiveReplacementTruncatesOldVersion(t *testing.T) {
	b, _, _, _ := newRetroactiveBookWithConfirmedQuote(t)
	registerRetroactiveReplacement(t, b)

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("补登后应有两个版本，got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("版本应按生效时刻排列为 v1/v2: %q, %q", old.VersionID, nv.VersionID)
	}

	// 旧版：登记结束仍是 3 月 31 日（永不因后续登记改变），
	// 实际结束提前到 3 月 10 日，且该点早于已确认报价的受理时刻。
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价被改写: %d", old.UnitPrice)
	}
	if old.End == nil || !old.End.Equal(retroOldEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(retroHandoff) {
		t.Fatalf("旧版实际结束应被提前到 3 月 10 日: %v", old.EffectiveEnd)
	}
	if !old.EffectiveEnd.Before(retroAcceptedAt) {
		t.Fatalf("旧版实际结束应早于已确认报价的受理时刻: 实际结束=%v 受理时刻=%v",
			old.EffectiveEnd, retroAcceptedAt)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}

	// 新版：从 3 月 10 日起持续有效，登记结束与实际结束均为空，并保留替代来源。
	if nv.UnitPrice != 180 || !nv.Start.Equal(retroHandoff) {
		t.Fatalf("新版登记信息异常: %+v", nv)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("新版不填结束时间，两种结束都应为空: 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q", nv.Replaces, nv.SupersededBy)
	}
}

// 补登之后按原标识 Lookup，或保持费率项、版本和数量不变再次报价，
// 都必须取回首次确认结果：来源仍是旧版，数量 4、单价 150、总价 600、
// 首次受理时刻 3 月 15 日 10 点全部保留，拒绝原因为空。
func TestRetroactiveReplacementPreservesConfirmedQuote(t *testing.T) {
	b, _, req, first := newRetroactiveBookWithConfirmedQuote(t)
	registerRetroactiveReplacement(t, b)

	assertOriginal600 := func(t *testing.T, got Outcome) {
		t.Helper()
		if got != first {
			t.Fatalf("历史确认结果被补登改写: 首次=%+v 取回=%+v", first, got)
		}
		if got.Request != req {
			t.Fatalf("历史结果来源应仍是旧版原请求: %+v vs %+v", got.Request, req)
		}
		if !got.Confirmed || got.UnitPrice != 150 || got.Total != 600 {
			t.Fatalf("历史结果应保留确认与 600 分金额: %+v", got)
		}
		if !got.AcceptedAt.Equal(retroAcceptedAt) {
			t.Fatalf("首次受理时刻应保留为 3 月 15 日 10 点: %v", got.AcceptedAt)
		}
		if got.Reason != ReasonNone {
			t.Fatalf("已确认记录的拒绝原因应为空: %q", got.Reason)
		}
	}

	got, err := b.Lookup(retroIDOriginal)
	if err != nil {
		t.Fatalf("补登后原标识应仍可查询: %v", err)
	}
	assertOriginal600(t, got)

	// 原样重试同样返回首次结果，而不是因旧版实际结束被提前而改判拒绝或改用新版价格。
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatalf("原样重试不应返回错误: %v", err)
	}
	assertOriginal600(t, replay)
}

// 仍在 3 月 15 日 10 点这一受理时刻：
// 用新标识引用旧版应正常受理后以 version_expired 拒绝，单价、总价为零；
// 用另一个新标识引用新版、数量 4，应按 180 分确认 720 分，来源指向新版；
// 两笔新报价都不能覆盖原来的 600 分记录。
func TestRetroactiveReplacementNewQuotesAtSameInstant(t *testing.T) {
	b, setNow, _, first := newRetroactiveBookWithConfirmedQuote(t)
	registerRetroactiveReplacement(t, b)
	// 补登不推进时钟；显式固定在原受理时刻，确认补登后的当场判定不依赖真实时间。
	setNow(retroAcceptedAt)

	// 新标识引用已被提前结束的旧版：正常受理（err 为空）后拒绝。
	expiredReq := QuoteRequest{
		RequestID: retroIDExpired, ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	}
	expired, err := b.Quote(expiredReq)
	if err != nil {
		t.Fatalf("引用旧版是正常受理，err 应为空: %v", err)
	}
	if expired.Confirmed {
		t.Fatalf("旧版实际结束已提前到受理时刻之前，不能确认: %+v", expired)
	}
	if expired.Reason != ReasonVersionExpired {
		t.Fatalf("拒绝原因应为 version_expired, got %q", expired.Reason)
	}
	if expired.UnitPrice != 0 || expired.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: 单价=%d 总价=%d", expired.UnitPrice, expired.Total)
	}
	if expired.Request != expiredReq {
		t.Fatalf("拒绝结果必须保留指定的旧版来源与数量: %+v vs %+v", expired.Request, expiredReq)
	}
	if !expired.AcceptedAt.Equal(retroAcceptedAt) {
		t.Fatalf("拒绝结果受理时刻应为当前受理时刻: %v", expired.AcceptedAt)
	}

	// 另一个新标识引用新版：按 180 分确认 720 分。
	newReq := QuoteRequest{
		RequestID: retroIDNew, ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	}
	fresh, err := b.Quote(newReq)
	if err != nil {
		t.Fatalf("引用新版是正常受理，err 应为空: %v", err)
	}
	if !fresh.Confirmed || fresh.UnitPrice != 180 || fresh.Total != 720 {
		t.Fatalf("新版报价应按 180 分确认 720 分: %+v", fresh)
	}
	if fresh.Request != newReq || fresh.Reason != ReasonNone {
		t.Fatalf("新确认结果应指向新版原请求且无拒绝原因: %+v", fresh)
	}
	if !fresh.AcceptedAt.Equal(retroAcceptedAt) {
		t.Fatalf("新确认结果受理时刻应为当前受理时刻: %v", fresh.AcceptedAt)
	}

	// 新的拒绝与新的确认都不能覆盖原来的 600 分记录。
	got, err := b.Lookup(retroIDOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("600 分原记录被新报价覆盖: 首次=%+v 取回=%+v", first, got)
	}
	// 三笔结果各按自己的标识取回，互不覆盖。
	if gotExpired, err := b.Lookup(retroIDExpired); err != nil || gotExpired != expired {
		t.Fatalf("拒绝记录应可按自己的标识原样取回: %+v (%v)", gotExpired, err)
	}
	if gotNew, err := b.Lookup(retroIDNew); err != nil || gotNew != fresh {
		t.Fatalf("720 分确认应可按自己的标识原样取回: %+v (%v)", gotNew, err)
	}
}

// 沿用原标识只把版本改成新版：返回 ErrRequestIDConflict，
// 不产生新的确认价，也不改写原 600 分记录；原标识原样重试仍取回首次结果。
func TestRetroactiveReplacementRequestIDConflict(t *testing.T) {
	b, _, req, first := newRetroactiveBookWithConfirmedQuote(t)
	registerRetroactiveReplacement(t, b)

	conflictReq := req
	conflictReq.VersionID = "seat-v2" // 只把版本改成新版，标识、费率项、数量不变
	out, err := b.Quote(conflictReq)
	if !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("沿用原标识改版本应返回 ErrRequestIDConflict, got %v", err)
	}
	if out != (Outcome{}) {
		t.Fatalf("冲突时不应返回任何报价结果: %+v", out)
	}

	// 原记录未被改写：Lookup 与原样重试取回的仍是首次 600 分确认。
	got, err := b.Lookup(retroIDOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("冲突调用改写了原记录: 首次=%+v 取回=%+v", first, got)
	}
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatalf("原请求内容未变，不应冲突: %v", err)
	}
	if replay != first {
		t.Fatalf("冲突后原样重试应仍取回首次结果: 首次=%+v 取回=%+v", first, replay)
	}
}
