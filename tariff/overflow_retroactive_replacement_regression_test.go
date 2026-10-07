package tariff

import (
	"errors"
	"math"
	"testing"
	"time"
)

// “首次报价因溢出被拒绝后，补登记过去生效的替代版本”场景的回归测试。
//
// 保护的规则：首次受理结果（total_overflow 拒绝）记录的是受理那一刻的判断，
// 与版本当前的有效期是两回事。之后补登一个从过去起替代旧版的新版本，
// 把旧版的实际有效结束提前到首次受理时刻之前，也绝不能回头改写那笔
// total_overflow 拒绝——不能改判成 version_expired，更不能改成确认价；
// 按费率项查看已受理报价时也必须保留这条记录。只有换用全新标识的首次报价，
// 才按补登后的当前有效期得到 version_expired。
//
// 固定时间线（均为 UTC，结束时刻不含）：
//
//	旧版 seat-v1：单价 math.MaxInt64 分，2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00
//	首次报价受理时刻：2026-03-15 10:00——当时只有 v1 且 v1 有效，
//	                 数量 2 的总价 2×MaxInt64 超出有符号 64 位整数上限
//	补登时刻：2026-03-16 10:00——登记新版 seat-v2（单价 100 分），
//	          从 2026-03-10 00:00 起替代旧版并持续有效
//
// 补登成功后旧版的登记结束仍是 3 月 31 日，实际有效结束被提前到 3 月 10 日，
// 早于那笔 total_overflow 拒绝的受理时刻（3 月 15 日 10 点）。
// 所有时刻写死并注入固定时钟，结论不依赖运行当天，无需等待真实时间跨过生效点。
var (
	overflowRetroOldStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	overflowRetroOldEnd     = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	overflowRetroHandoff    = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	overflowRetroAcceptedAt = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	overflowRetroRegisterAt = time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
)

// 场景中各笔报价各自使用独立的非空请求标识。
const (
	overflowRetroIDOriginal = "overflow-retro-original" // 补登前因 total_overflow 被拒绝的首次报价
	overflowRetroIDFresh    = "overflow-retro-fresh"    // 补登后用全新标识引用旧版
)

// newOverflowRetroBookWithRejection 建立场景起点：
// 只登记旧版 seat-v1（单价 MaxInt64 分），时钟固定在 3 月 15 日 10 点，
// 此时旧版尚未被替代、处于有效期内，用原标识以数量 2 首次报价，
// 正常受理后因 total_overflow 被拒绝。返回账本、推进时钟的函数、原请求与首次拒绝结果。
func newOverflowRetroBookWithRejection(t *testing.T) (*Book, func(time.Time), QuoteRequest, Outcome) {
	t.Helper()
	// 场景前提：首次受理时刻在旧版登记的有效期内，而补登的交接点早于受理时刻。
	if !overflowRetroAcceptedAt.Before(overflowRetroOldEnd) {
		t.Fatal("测试前提不成立：首次受理时刻应早于旧版登记结束")
	}
	if !overflowRetroHandoff.Before(overflowRetroAcceptedAt) {
		t.Fatal("测试前提不成立：新版生效时刻应早于首次受理时刻")
	}

	now, setNow := fixedClock(overflowRetroAcceptedAt)
	b := NewBook(WithClock(now))
	oldEnd := overflowRetroOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: math.MaxInt64,
		Start: overflowRetroOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}

	req := QuoteRequest{
		RequestID: overflowRetroIDOriginal, ItemID: "seat", VersionID: "seat-v1", Quantity: 2,
	}
	first, err := b.Quote(req)
	if err != nil {
		t.Fatalf("溢出拒绝是正常受理结果，不应返回 error: %v", err)
	}
	if first.Confirmed {
		t.Fatalf("总价 2×MaxInt64 不可表示，不能确认: %+v", first)
	}
	if first.Reason != ReasonTotalOverflow {
		t.Fatalf("当时只有 v1 且有效，拒绝原因应为 total_overflow, got %q", first.Reason)
	}
	if first.UnitPrice != 0 || first.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	if first.Request != req {
		t.Fatalf("首次结果应完整保留请求来源: %+v vs %+v", first.Request, req)
	}
	if !first.AcceptedAt.Equal(overflowRetroAcceptedAt) {
		t.Fatalf("首次受理时刻应为 3 月 15 日 10 点: %v", first.AcceptedAt)
	}
	return b, setNow, req, first
}

// registerOverflowRetroReplacement 把时钟推进到 3 月 16 日 10 点并补登新版 seat-v2：
// 单价 100 分，从 3 月 10 日零点起替代旧版，不填结束时间（持续有效）。
// 登记只按旧版当前的实际有效期判断交接点是否合法，允许交接点早于登记当天，登记必须成功。
func registerOverflowRetroReplacement(t *testing.T, b *Book, setNow func(time.Time)) {
	t.Helper()
	setNow(overflowRetroRegisterAt)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 100,
		Start: overflowRetroHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("补登过去生效、且无其他版本占用其时间的新版应成功: %v", err)
	}
}

// 补登成功后：旧版登记结束仍是 3 月 31 日，实际有效结束被提前到 3 月 10 日，
// 早于首次报价的受理时刻；新版从 3 月 10 日起持续有效。
func TestOverflowRetroReplacementTruncatesOldVersion(t *testing.T) {
	b, setNow, _, _ := newOverflowRetroBookWithRejection(t)
	registerOverflowRetroReplacement(t, b, setNow)

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
	// 实际结束提前到 3 月 10 日，且该点早于首次拒绝的受理时刻。
	if old.UnitPrice != math.MaxInt64 {
		t.Fatalf("旧版单价被改写: %d", old.UnitPrice)
	}
	if old.End == nil || !old.End.Equal(overflowRetroOldEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(overflowRetroHandoff) {
		t.Fatalf("旧版实际结束应被提前到 3 月 10 日: %v", old.EffectiveEnd)
	}
	if !old.EffectiveEnd.Before(overflowRetroAcceptedAt) {
		t.Fatalf("旧版实际结束应早于首次拒绝的受理时刻: 实际结束=%v 受理时刻=%v",
			old.EffectiveEnd, overflowRetroAcceptedAt)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}

	// 新版：单价 100 分，从 3 月 10 日起持续有效，两种结束均为空，并保留替代来源。
	if nv.UnitPrice != 100 || !nv.Start.Equal(overflowRetroHandoff) {
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

// 补登之后按原标识 Lookup，或保持费率项、版本和数量不变再次提交，
// 都必须取回 3 月 15 日那份 total_overflow 拒绝：来源仍是旧版、数量 2，
// 单价总价为零，首次受理时刻不变——不能因旧版实际结束已被提前到受理时刻之前
// 而改判 version_expired，更不能改成确认价。
func TestOverflowRetroReplacementPreservesOverflowRejection(t *testing.T) {
	b, setNow, req, first := newOverflowRetroBookWithRejection(t)
	registerOverflowRetroReplacement(t, b, setNow)

	assertOriginalOverflow := func(t *testing.T, got Outcome) {
		t.Helper()
		if got != first {
			t.Fatalf("历史 total_overflow 拒绝被补登改写: 首次=%+v 取回=%+v", first, got)
		}
		if got.Request != req {
			t.Fatalf("历史结果来源应仍是旧版原请求: %+v vs %+v", got.Request, req)
		}
		if got.Confirmed {
			t.Fatalf("历史拒绝不能改成确认价: %+v", got)
		}
		if got.Reason != ReasonTotalOverflow {
			t.Fatalf("历史拒绝原因应仍是 total_overflow，不能改判 version_expired: %q", got.Reason)
		}
		if got.UnitPrice != 0 || got.Total != 0 {
			t.Fatalf("历史拒绝金额应仍为零: 单价=%d 总价=%d", got.UnitPrice, got.Total)
		}
		if !got.AcceptedAt.Equal(overflowRetroAcceptedAt) {
			t.Fatalf("首次受理时刻应保留为 3 月 15 日 10 点: %v", got.AcceptedAt)
		}
	}

	got, err := b.Lookup(overflowRetroIDOriginal)
	if err != nil {
		t.Fatalf("补登后原标识应仍可查询: %v", err)
	}
	assertOriginalOverflow(t, got)

	// 原样重试同样返回首次拒绝，而不是按补登后的当前有效期重新受理。
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatalf("原样重试不应返回错误: %v", err)
	}
	assertOriginalOverflow(t, replay)
}

// 补登之后按费率项查看已受理报价：那条 total_overflow 拒绝必须保留在列表中，
// 不能依据现在的有效期重新归类或剔除；来源、金额、受理时刻与首次结果一致。
func TestOverflowRetroReplacementItemOutcomesKeepsRejection(t *testing.T) {
	b, setNow, req, first := newOverflowRetroBookWithRejection(t)
	registerOverflowRetroReplacement(t, b, setNow)

	outcomes, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatalf("按费率项查看已受理报价不应报错: %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("seat 项下应只有那笔首次拒绝一条记录，got %d: %+v", len(outcomes), outcomes)
	}
	got := outcomes[0]
	if got != first {
		t.Fatalf("列表中的记录被按当前有效期改写: 首次=%+v 列表=%+v", first, got)
	}
	if got.Confirmed || got.Reason != ReasonTotalOverflow {
		t.Fatalf("列表中的记录应仍是 total_overflow 拒绝: %+v", got)
	}
	if got.Request != req || !got.AcceptedAt.Equal(overflowRetroAcceptedAt) {
		t.Fatalf("列表中的记录应保留原请求来源与首次受理时刻: %+v", got)
	}
}

// 与旧记录对照：补登后另用全新标识、仍引用旧版且数量为 2，
// 同样的金额请求这时应按补登后的当前有效期得到 version_expired 拒绝，
// 而不是 total_overflow，金额仍为零；这笔新拒绝独立保存，不影响原记录。
func TestOverflowRetroReplacementFreshIDGetsVersionExpired(t *testing.T) {
	b, setNow, _, first := newOverflowRetroBookWithRejection(t)
	registerOverflowRetroReplacement(t, b, setNow)

	freshReq := QuoteRequest{
		RequestID: overflowRetroIDFresh, ItemID: "seat", VersionID: "seat-v1", Quantity: 2,
	}
	fresh, err := b.Quote(freshReq)
	if err != nil {
		t.Fatalf("引用旧版是正常受理，err 应为空: %v", err)
	}
	if fresh.Confirmed {
		t.Fatalf("旧版实际结束已提前到受理时刻之前，不能确认: %+v", fresh)
	}
	if fresh.Reason != ReasonVersionExpired {
		t.Fatalf("补登后新标识引用旧版应得到 version_expired 而非 total_overflow, got %q", fresh.Reason)
	}
	if fresh.UnitPrice != 0 || fresh.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: 单价=%d 总价=%d", fresh.UnitPrice, fresh.Total)
	}
	if fresh.Request != freshReq {
		t.Fatalf("新拒绝必须保留指定的旧版来源与数量: %+v vs %+v", fresh.Request, freshReq)
	}
	if !fresh.AcceptedAt.Equal(overflowRetroRegisterAt) {
		t.Fatalf("新拒绝的受理时刻应为补登后的当前时刻: %v", fresh.AcceptedAt)
	}

	// 新拒绝独立保存，可按自己的标识原样取回。
	gotFresh, err := b.Lookup(overflowRetroIDFresh)
	if err != nil || gotFresh != fresh {
		t.Fatalf("新拒绝应可按自己的标识原样取回: %+v (%v)", gotFresh, err)
	}
	// 原 total_overflow 记录不受新报价影响。
	gotOriginal, err := b.Lookup(overflowRetroIDOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if gotOriginal != first {
		t.Fatalf("原 total_overflow 记录被新报价覆盖: 首次=%+v 取回=%+v", first, gotOriginal)
	}

	// 按费率项查看：两笔拒绝都在，按首次受理时刻先旧后新。
	outcomes, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("seat 项下应有原拒绝与新拒绝两条记录，got %d: %+v", len(outcomes), outcomes)
	}
	if outcomes[0] != first || outcomes[1] != fresh {
		t.Fatalf("列表应按受理时刻先列出 total_overflow 再列出 version_expired: %+v", outcomes)
	}
}

// 沿用原标识只把数量改为 1：即使 1×MaxInt64 已可表示，也属于“相同标识、
// 不同内容”，必须返回 ErrRequestIDConflict，没有可使用的新报价结果，
// 原 total_overflow 拒绝不被覆盖；原样重试与冲突提交都不增加受理记录。
func TestOverflowRetroReplacementRequestIDConflict(t *testing.T) {
	b, setNow, req, first := newOverflowRetroBookWithRejection(t)
	registerOverflowRetroReplacement(t, b, setNow)

	conflictReq := req
	conflictReq.Quantity = 1 // 只把数量改成可表示的 1，标识、费率项、版本不变
	out, err := b.Quote(conflictReq)
	if !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("沿用原标识改数量应返回 ErrRequestIDConflict, got %v", err)
	}
	if out != (Outcome{}) {
		t.Fatalf("冲突时不应返回任何报价结果: %+v", out)
	}

	// 原记录未被改写：Lookup 与原样重试取回的仍是首次 total_overflow 拒绝。
	got, err := b.Lookup(overflowRetroIDOriginal)
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

	// 原样重试与冲突提交都不产生新记录：列表仍只有那笔首次拒绝。
	outcomes, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0] != first {
		t.Fatalf("原样重试与冲突提交不应增加受理记录: %+v", outcomes)
	}
}
