package tariff

import (
	"errors"
	"math"
	"testing"
	"time"
)

// “首次报价因 total_overflow 被拒绝后，才补登记过去生效的替代版本”场景的
// 自动化回归。保护的是首次受理结果与版本当前有效期之间的区别：
//
//	旧版 seat-v1：单价 math.MaxInt64 分（有符号 64 位整数最大值），
//	             2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00（不含）
//	首次报价：2026-03-15 10:00，当时只有 v1 且版本有效，数量 2，
//	         2×MaxInt64 超出有符号 64 位整数上限 → total_overflow 拒绝
//	补登新版 seat-v2：单价 100 分，2026-03-10 00:00 起替代 v1，持续有效；
//	                于 2026-03-16 09:00 补登。补登后 v1 登记结束仍是 3 月 31 日，
//	                实际有效结束被提前到 3 月 10 日，早于原报价的受理时刻
//
// 必须保证：补登只改变版本此后的有效期，不能回头改写已经受理的拒绝。
// 原标识查询或原样重试取回的永远是 3 月 15 日那份 total_overflow；
// 新标识在补登后引用同一旧版，才按当前实际有效期得到 version_expired。
// 所有时刻均写死并通过固定时钟注入，结论不依赖运行当天，也无需等待真实时间。
var (
	retroOverflowOldStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	retroOverflowOldEnd     = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	retroOverflowHandoff    = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	retroOverflowAcceptedAt = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	retroOverflowRegisterAt = time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
)

// 场景中各笔报价各自使用独立的非空请求标识。
const (
	retroOverflowIDOriginal = "retro-overflow-original" // 补登前已受理的 total_overflow 拒绝
	retroOverflowIDExpired  = "retro-overflow-expired"  // 补登后用新标识引用旧版
)

// newRetroactiveOverflowBook 建立场景起点：只登记旧版 seat-v1，
// 时钟固定在 3 月 15 日 10 点，用原标识以数量 2 取得 total_overflow 首次拒绝。
// 返回账本、拨表函数、原请求与首次拒绝结果（此时新版尚未补登）。
func newRetroactiveOverflowBook(t *testing.T) (*Book, func(time.Time), QuoteRequest, Outcome) {
	t.Helper()
	// 测试前提：受理时刻落在旧版登记的有效期内，且当时没有其他版本。
	if !retroOverflowAcceptedAt.Before(retroOverflowOldEnd) {
		t.Fatal("测试前提不成立：报价受理时刻应早于旧版登记结束")
	}
	if !retroOverflowHandoff.Before(retroOverflowAcceptedAt) {
		t.Fatal("测试前提不成立：补登的交接点应早于首次报价受理时刻")
	}
	// 测试前提：数量大于 MaxInt64/MaxInt64（即 1），与账本同一口径下必然溢出。
	if retroOverflowQuantity <= math.MaxInt64/math.MaxInt64 {
		t.Fatalf("测试前提不成立：数量 %d 乘 MaxInt64 应溢出", retroOverflowQuantity)
	}

	now, setNow := fixedClock(retroOverflowAcceptedAt)
	b := NewBook(WithClock(now))
	oldEnd := retroOverflowOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: math.MaxInt64,
		Start: retroOverflowOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}

	req := QuoteRequest{
		RequestID: retroOverflowIDOriginal, ItemID: "seat",
		VersionID: "seat-v1", Quantity: retroOverflowQuantity,
	}
	first, err := b.Quote(req)
	if err != nil {
		t.Fatalf("版本有效但总价溢出时应正常受理，而不是返回调用错误: %v", err)
	}
	return b, setNow, req, first
}

// registerRetroactiveOverflowReplacement 在 3 月 16 日补登新版 seat-v2：
// 单价 100 分，从 3 月 10 日零点起替代旧版，不填结束时间。
func registerRetroactiveOverflowReplacement(t *testing.T, b *Book, setNow func(time.Time)) {
	t.Helper()
	// 登记本身不读时钟，但仍把时钟拨到 3 月 16 日，明确“事后补登”的语义，
	// 也让随后的新报价受理时刻与补登当天一致。
	setNow(retroOverflowRegisterAt)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 100,
		Start: retroOverflowHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("补登从过去生效、且无其他版本占用其时间的新版应成功: %v", err)
	}
}

const retroOverflowQuantity = int64(2)

// 首次报价：版本在受理时刻有效，但总价无法表示。
// 调用必须正常受理（err 为空），结果未确认、原因为 total_overflow，
// 单价和总价均为零，请求中的费率项、版本、数量与首次受理时刻完整保留，
// 且这份首次拒绝立即按请求标识保存、可查。
func TestRetroactiveOverflowFirstQuoteAcceptedAsTotalOverflow(t *testing.T) {
	b, _, req, first := newRetroactiveOverflowBook(t)

	if first.Confirmed {
		t.Fatalf("2×MaxInt64 超出上限，不能确认: %+v", first)
	}
	if first.Reason != ReasonTotalOverflow {
		t.Fatalf("版本当时有效，拒绝原因应为 total_overflow, got %q", first.Reason)
	}
	if first.UnitPrice != 0 || first.Total != 0 {
		t.Fatalf("溢出拒绝的单价和总价都应为零: 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	if first.Request != req {
		t.Fatalf("拒绝结果必须完整保留原请求（费率项/版本/数量/标识）: %+v vs %+v",
			first.Request, req)
	}
	if !first.AcceptedAt.Equal(retroOverflowAcceptedAt) {
		t.Fatalf("首次受理时刻应为 3 月 15 日 10 点: %v", first.AcceptedAt)
	}

	// 首次拒绝与首次确认一样立即保存，可按标识取回。
	got, err := b.Lookup(retroOverflowIDOriginal)
	if err != nil {
		t.Fatalf("首次拒绝应可按标识查询: %v", err)
	}
	if got != first {
		t.Fatalf("查询结果应与首次拒绝完全一致: %+v vs %+v", got, first)
	}
}

// 补登成功后：v1 的登记结束仍是 3 月 31 日（不因后续登记改变），
// 实际有效结束提前到 3 月 10 日，且早于原报价的受理时刻；
// v2 单价 100 分，从 3 月 10 日起持续有效，两版互相可见替代关系。
func TestRetroactiveOverflowRegistrationTruncatesEffectiveInterval(t *testing.T) {
	b, setNow, _, _ := newRetroactiveOverflowBook(t)
	registerRetroactiveOverflowReplacement(t, b, setNow)

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("补登后应有两个版本, got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("版本应按生效时刻排列为 v1/v2: %q, %q", old.VersionID, nv.VersionID)
	}

	// 旧版：登记结束仍是 3 月 31 日；实际结束提前到交接点，且早于原受理时刻。
	if old.UnitPrice != math.MaxInt64 {
		t.Fatalf("旧版单价不应被补登改写: %d", old.UnitPrice)
	}
	if old.End == nil || !old.End.Equal(retroOverflowOldEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(retroOverflowHandoff) {
		t.Fatalf("旧版实际有效结束应被提前到 3 月 10 日: %v", old.EffectiveEnd)
	}
	if !old.EffectiveEnd.Before(retroOverflowAcceptedAt) {
		t.Fatalf("旧版实际结束应早于原报价受理时刻: 实际结束=%v 受理时刻=%v",
			old.EffectiveEnd, retroOverflowAcceptedAt)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 新版：100 分，从 3 月 10 日起持续有效，两种结束都为空，保留替代来源。
	if nv.UnitPrice != 100 || !nv.Start.Equal(retroOverflowHandoff) {
		t.Fatalf("新版登记信息异常: %+v", nv)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("新版不填结束时间，两种结束都应为空: 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}
}

// 补登之后按原标识 Lookup，或保持费率项、版本、数量不变原样重试，
// 都必须取回 3 月 15 日那份 total_overflow：来源、零金额、拒绝原因与首次受理
// 时刻全部不变，不能改判成 version_expired，也不能变成任何确认价。
// 无论把账本时钟拨到哪个实际运行日期，结论都一致——原样重试不重新受理。
func TestRetroactiveOverflowStoredRejectionSurvivesRetroactiveReplacement(t *testing.T) {
	b, setNow, req, first := newRetroactiveOverflowBook(t)
	registerRetroactiveOverflowReplacement(t, b, setNow)

	assertOriginalRejection := func(t *testing.T, got Outcome, stage string) {
		t.Helper()
		if got != first {
			t.Fatalf("%s: 首次拒绝被补登或时钟推进改写: 首次=%+v 取回=%+v", stage, first, got)
		}
		if got.Confirmed || got.Reason != ReasonTotalOverflow {
			t.Fatalf("%s: 必须仍是 total_overflow 拒绝, got Confirmed=%v Reason=%q",
				stage, got.Confirmed, got.Reason)
		}
		if got.UnitPrice != 0 || got.Total != 0 {
			t.Fatalf("%s: 零金额不应被改成任何确认价: 单价=%d 总价=%d",
				stage, got.UnitPrice, got.Total)
		}
		if got.Request != req {
			t.Fatalf("%s: 请求来源应原样保留: %+v vs %+v", stage, got.Request, req)
		}
		if !got.AcceptedAt.Equal(retroOverflowAcceptedAt) {
			t.Fatalf("%s: 首次受理时刻应仍是 3 月 15 日 10 点: %v", stage, got.AcceptedAt)
		}
	}

	got, err := b.Lookup(retroOverflowIDOriginal)
	if err != nil {
		t.Fatalf("补登后原标识应仍可查询: %v", err)
	}
	assertOriginalRejection(t, got, "补登后 Lookup")

	replay, err := b.Quote(req)
	if err != nil {
		t.Fatalf("原样重试不应返回错误: %v", err)
	}
	assertOriginalRejection(t, replay, "补登当刻原样重试")

	// 把时钟拨到与场景无关的不同实际运行日期再查再重试：
	// 早于首版生效、落在旧版（被截断前的）窗口内、恰为原受理时刻、远在未来，
	// 取回的都必须是同一份保存结果，不能按当下有效期或当下时钟重新受理。
	for _, instant := range []time.Time{
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), // 早于 v1 生效
		retroOverflowHandoff.Add(-time.Nanosecond),  // 补登后看仍在 v1 实际窗口内
		retroOverflowAcceptedAt,                     // 恰为原受理时刻
		time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), // 远在未来
	} {
		setNow(instant)
		got, err := b.Lookup(retroOverflowIDOriginal)
		if err != nil {
			t.Fatalf("时钟拨到 %s 后查询失败: %v", instant, err)
		}
		assertOriginalRejection(t, got, "时钟="+instant.Format(time.RFC3339)+" Lookup")

		replay, err := b.Quote(req)
		if err != nil {
			t.Fatalf("时钟拨到 %s 后原样重试失败: %v", instant, err)
		}
		assertOriginalRejection(t, replay, "时钟="+instant.Format(time.RFC3339)+" 原样重试")
	}
}

// 对照记录：补登后另用全新标识、仍引用 v1、数量 2，按当前实际有效期
// v1 已在 3 月 10 日失效，应得到 version_expired 而不是 total_overflow，
// 金额仍为零，来源与数量保留，受理时刻为补登当天。这份拒绝独立保存，
// 与原 total_overflow 记录互不覆盖。
func TestRetroactiveOverflowFreshRequestUsesCurrentValidityNotHistory(t *testing.T) {
	b, setNow, _, first := newRetroactiveOverflowBook(t)
	registerRetroactiveOverflowReplacement(t, b, setNow)

	freshReq := QuoteRequest{
		RequestID: retroOverflowIDExpired, ItemID: "seat",
		VersionID: "seat-v1", Quantity: retroOverflowQuantity,
	}
	expired, err := b.Quote(freshReq)
	if err != nil {
		t.Fatalf("引用已失效旧版是正常受理，err 应为空: %v", err)
	}
	if expired.Confirmed {
		t.Fatalf("v1 实际有效结束已提前到受理时刻之前，不能确认: %+v", expired)
	}
	if expired.Reason != ReasonVersionExpired {
		t.Fatalf("新标识应按当前有效期得到 version_expired, got %q", expired.Reason)
	}
	if expired.UnitPrice != 0 || expired.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: 单价=%d 总价=%d",
			expired.UnitPrice, expired.Total)
	}
	if expired.Request != freshReq {
		t.Fatalf("拒绝结果必须保留新标识、旧版来源与数量 2: %+v vs %+v",
			expired.Request, freshReq)
	}
	if !expired.AcceptedAt.Equal(retroOverflowRegisterAt) {
		t.Fatalf("新拒绝的受理时刻应为补登当天: %v", expired.AcceptedAt)
	}

	// 新拒绝独立保存，可按自己的标识取回；原 total_overflow 记录原封不动。
	gotExpired, err := b.Lookup(retroOverflowIDExpired)
	if err != nil || gotExpired != expired {
		t.Fatalf("新拒绝应独立保存: %+v (%v)", gotExpired, err)
	}
	gotOriginal, err := b.Lookup(retroOverflowIDOriginal)
	if err != nil || gotOriginal != first {
		t.Fatalf("新拒绝不能覆盖原 total_overflow 记录: %+v (%v)", gotOriginal, err)
	}
}

// 沿用原标识只把数量改成 1：即使 1×MaxInt64 恰好等于上限、乘积已可表示，
// 相同标识不同内容也必须先返回 ErrRequestIDConflict，原拒绝不被覆盖，
// 也不会产生新的受理记录；冲突后原样重试仍取回首次拒绝。
func TestRetroactiveOverflowQuantityChangeConflicts(t *testing.T) {
	b, setNow, req, first := newRetroactiveOverflowBook(t)
	registerRetroactiveOverflowReplacement(t, b, setNow)

	conflictReq := req
	conflictReq.Quantity = 1 // 只改数量；MaxInt64×1 == MaxInt64，本可确认
	out, err := b.Quote(conflictReq)
	if !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("沿用原标识改数量应返回 ErrRequestIDConflict, got %v", err)
	}
	if out != (Outcome{}) {
		t.Fatalf("冲突时不应返回任何报价结果: %+v", out)
	}

	// 原拒绝未被覆盖：Lookup 与原样重试取回的仍是首次 total_overflow。
	got, err := b.Lookup(retroOverflowIDOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("冲突调用改写了原拒绝: 首次=%+v 取回=%+v", first, got)
	}
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatalf("原请求内容未变，不应冲突: %v", err)
	}
	if replay != first {
		t.Fatalf("冲突后原样重试应仍取回首次拒绝: 首次=%+v 取回=%+v", first, replay)
	}
}

// 按费率项查看已受理报价：原 total_overflow 拒绝必须保留在列表中，
// 不能因 v1 现在已失效而被重新归类为 version_expired 或被剔除。
// 原样重试与冲突提交都不增加记录；全新标识产生的 version_expired 拒绝
// 独立保存，按受理时刻排在原记录之后。返回的是独立副本，外部改写不污染账本。
func TestRetroactiveOverflowItemOutcomesKeepsHistoricalRejection(t *testing.T) {
	b, setNow, req, first := newRetroactiveOverflowBook(t)

	// 首次拒绝后：seat 下已有 1 条记录。
	list, err := b.ItemOutcomes("seat")
	if err != nil || len(list) != 1 {
		t.Fatalf("首次拒绝后应有 1 条记录: %d (%v) %+v", len(list), err, list)
	}

	registerRetroactiveOverflowReplacement(t, b, setNow)

	// 补登本身不增删受理记录。
	if list, err := b.ItemOutcomes("seat"); err != nil || len(list) != 1 {
		t.Fatalf("补登版本不应改变受理记录数: %d (%v)", len(list), err)
	}

	// 原样重试不增加记录。
	if _, err := b.Quote(req); err != nil {
		t.Fatalf("原样重试: %v", err)
	}
	// 相同标识改数量的冲突是调用错误，也不增加记录。
	conflictReq := req
	conflictReq.Quantity = 1
	if _, err := b.Quote(conflictReq); !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("改数量应冲突, got %v", err)
	}
	if list, err := b.ItemOutcomes("seat"); err != nil || len(list) != 1 {
		t.Fatalf("原样重试与冲突提交都不应增加记录: %d (%v)", len(list), err)
	}

	// 全新标识的拒绝独立保存，记录数变为 2。
	freshReq := QuoteRequest{
		RequestID: retroOverflowIDExpired, ItemID: "seat",
		VersionID: "seat-v1", Quantity: retroOverflowQuantity,
	}
	expired, err := b.Quote(freshReq)
	if err != nil {
		t.Fatalf("新标识引用旧版应正常受理: %v", err)
	}
	list, err = b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("全新标识的拒绝应独立保存，应有 2 条记录, got %d", len(list))
	}

	// 按首次受理时刻：原 total_overflow（3 月 15 日）在前，新拒绝（3 月 16 日）在后。
	if list[0] != first {
		t.Fatalf("原拒绝必须原样保留在列表中，不能被重新归类: %+v vs %+v", list[0], first)
	}
	if list[1] != expired || list[1].Reason != ReasonVersionExpired {
		t.Fatalf("新拒绝应作为独立记录列在其后: %+v vs %+v", list[1], expired)
	}
	// 显式点明：旧记录的原因没有被当前有效期改写，零金额也没有变成确认价。
	if list[0].Confirmed || list[0].Reason != ReasonTotalOverflow ||
		list[0].UnitPrice != 0 || list[0].Total != 0 {
		t.Fatalf("原记录必须仍是 total_overflow 零金额拒绝: %+v", list[0])
	}

	// 列表是独立副本：改写返回记录后，单笔查询与再次列表仍是账本原值。
	list[0].Confirmed = true
	list[0].Reason = ReasonVersionExpired
	list[0].Total = 100
	list[0].Request.Quantity = 1
	again, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if again[0] != first {
		t.Fatalf("外部改写列表副本不能污染账本: %+v vs %+v", again[0], first)
	}
	got, err := b.Lookup(retroOverflowIDOriginal)
	if err != nil || got != first {
		t.Fatalf("外部改写后单笔查询仍应为首次拒绝: %+v (%v)", got, err)
	}
}

// 端到端再保一次“不同实际运行日期下一致”：完整重建场景，期间所有受理时刻
// 都由固定时钟显式给出，场景结束后把时钟拨到任意无关日期，
// Lookup、原样重试和按费率项列表三种读取方式在每个日期下都返回完全相同的结果。
func TestRetroactiveOverflowScenarioDeterministicAcrossRunDates(t *testing.T) {
	b, setNow, req, first := newRetroactiveOverflowBook(t)
	registerRetroactiveOverflowReplacement(t, b, setNow)

	expiredReq := QuoteRequest{
		RequestID: retroOverflowIDExpired, ItemID: "seat",
		VersionID: "seat-v1", Quantity: retroOverflowQuantity,
	}
	expired, err := b.Quote(expiredReq)
	if err != nil {
		t.Fatalf("新标识引用旧版应正常受理: %v", err)
	}
	if expired.Reason != ReasonVersionExpired {
		t.Fatalf("应得到 version_expired, got %q", expired.Reason)
	}

	runDates := []time.Time{
		time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC),
		time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		time.Date(2031, 7, 4, 12, 0, 0, 0, time.UTC),
	}
	for _, d := range runDates {
		setNow(d)

		got, err := b.Lookup(retroOverflowIDOriginal)
		if err != nil || got != first {
			t.Fatalf("运行日期 %s: 原标识查询结果不一致: %+v (%v)", d, got, err)
		}
		replay, err := b.Quote(req)
		if err != nil || replay != first {
			t.Fatalf("运行日期 %s: 原样重试结果不一致: %+v (%v)", d, replay, err)
		}
		gotExpired, err := b.Lookup(retroOverflowIDExpired)
		if err != nil || gotExpired != expired {
			t.Fatalf("运行日期 %s: 新拒绝查询结果不一致: %+v (%v)", d, gotExpired, err)
		}

		list, err := b.ItemOutcomes("seat")
		if err != nil {
			t.Fatalf("运行日期 %s: 按费率项查询失败: %v", d, err)
		}
		if len(list) != 2 || list[0] != first || list[1] != expired {
			t.Fatalf("运行日期 %s: 列表内容应与运行日期无关: %+v", d, list)
		}
	}
}
