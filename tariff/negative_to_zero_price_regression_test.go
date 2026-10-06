package tariff

import (
	"errors"
	"testing"
	"time"
)

// “替代登记先填了负单价、随后修正为零单价”场景的固定时间线（全部按 UTC 解释，
// 结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00
//	新版 seat-v2：2026-03-10 00:00 起替代旧版，不填结束时间；
//	              第一次登记误填单价 -1 分（应被 ErrInvalidUnitPrice 拒绝），
//	              随后只把单价修正为 0 分，沿用同一版本标识、生效时刻与替代来源重新登记。
//
//	报价受理时刻 negzeroFirstAcceptedAt：2026-03-05 00:00——按旧版取得 600 分确认；
//	报价受理时刻 negzeroQuoteAt：2026-03-12 00:00——先在登记失败后验证旧版仍可用，
//	                              登记成功后再在同一时刻分别引用新旧两版。
//
// 负单价登记必须在任何状态变更之前被拒绝，账本保持原状；零单价是合法费率，
// 修正后的登记应成功并把旧版的实际结束截短到交接点。
// 所有时刻写死并注入固定时钟，结论不依赖运行当天或真实时间等待。
var (
	negzeroOldStart        = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	negzeroOldEnd          = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	negzeroHandoff         = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	negzeroFirstAcceptedAt = time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	negzeroQuoteAt         = time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
)

// 场景中各笔报价各自使用独立的非空请求标识。
const (
	negzeroIDBeforeFail = "negzero-before-fail-600" // 登记失败前按旧版确认的 600 分报价
	negzeroIDAfterFail  = "negzero-after-fail-600"  // 登记失败后按旧版确认的 600 分报价
	negzeroIDFreeNew    = "negzero-free-new-0"      // 登记成功后引用零单价新版
	negzeroIDExpiredOld = "negzero-expired-old"     // 登记成功后引用已被截短的旧版
)

// 负单价的替代登记应返回 ErrInvalidUnitPrice 且不留下任何变更：
// 账本里仍只有旧版，单价、登记起止、实际有效结束与替代关系都与登记前一致；
// 随后在 3 月 12 日用新标识按旧版报价仍确认 600 分，证明旧版可用时间未被截短。
// 把单价修正为 0 分后沿用同一版本标识、生效时刻与替代来源重新登记应成功：
// 旧版登记结束仍是 3 月 31 日，实际结束变为 3 月 10 日并指明被新版替代；
// 新版 0 分单价从交接点起持续有效。同一受理时刻引用新版确认 0 分、引用旧版
// 以 version_expired 拒绝，两笔历史确认仍可按各自标识原样取回。
func TestNegativeThenZeroPriceReplacement(t *testing.T) {
	now, setNow := fixedClock(negzeroFirstAcceptedAt)
	b := NewBook(WithClock(now))

	oldEnd := negzeroOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: negzeroOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}

	// 3 月 5 日按旧版、数量 4 取得 600 分确认，保留请求标识与首次受理时刻。
	reqBefore := QuoteRequest{
		RequestID: negzeroIDBeforeFail, ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	}
	first, err := b.Quote(reqBefore)
	if err != nil {
		t.Fatalf("旧版有效期内报价应正常受理: %v", err)
	}
	if !first.Confirmed || first.UnitPrice != 150 || first.Total != 600 {
		t.Fatalf("首次报价应按 150 分确认 600 分: %+v", first)
	}
	if !first.AcceptedAt.Equal(negzeroFirstAcceptedAt) || first.Reason != ReasonNone {
		t.Fatalf("首次结果受理时刻/拒绝原因异常: %+v", first)
	}

	// 第一次登记新版：单价误填 -1 分，其余内容合法，应返回既有的 ErrInvalidUnitPrice。
	negReq := RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: -1,
		Start: negzeroHandoff, Replaces: "seat-v1",
	}
	if err := b.RegisterVersion(negReq); !errors.Is(err, ErrInvalidUnitPrice) {
		t.Fatalf("负单价登记应返回 ErrInvalidUnitPrice, got %v", err)
	}

	// 失败后账本保持原状：仍只有旧版，登记信息、实际有效区间与替代关系均未变。
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("负单价登记失败后不应留下新版，got %d 个版本: %+v", len(views), views)
	}
	old := views[0]
	if old.VersionID != "seat-v1" || old.UnitPrice != 150 {
		t.Fatalf("旧版登记信息被改写: %+v", old)
	}
	if !old.Start.Equal(negzeroOldStart) || old.End == nil || !old.End.Equal(negzeroOldEnd) {
		t.Fatalf("旧版登记起止被改写: 开始=%v 结束=%v", old.Start, old.End)
	}
	if !old.EffectiveStart.Equal(negzeroOldStart) || old.EffectiveEnd == nil ||
		!old.EffectiveEnd.Equal(negzeroOldEnd) {
		t.Fatalf("旧版实际有效区间被提前截短: 实际开始=%v 实际结束=%v",
			old.EffectiveStart, old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "" {
		t.Fatalf("失败的登记不应留下替代关系: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 3 月 12 日用新标识按旧版、数量 4 报价：仍确认 150 分单价、600 分总价，
	// 证明失败的替代登记没有改变旧版的可用时间。
	setNow(negzeroQuoteAt)
	reqAfter := QuoteRequest{
		RequestID: negzeroIDAfterFail, ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	}
	second, err := b.Quote(reqAfter)
	if err != nil {
		t.Fatalf("登记失败后旧版报价应正常受理: %v", err)
	}
	if !second.Confirmed || second.UnitPrice != 150 || second.Total != 600 {
		t.Fatalf("失败的替代登记不应影响旧版报价，仍应确认 600 分: %+v", second)
	}
	if !second.AcceptedAt.Equal(negzeroQuoteAt) || second.Reason != ReasonNone {
		t.Fatalf("第二笔结果受理时刻/拒绝原因异常: %+v", second)
	}

	// 只把单价修正为 0 分，沿用刚才失败的版本标识、生效时刻与替代来源再次登记，应成功。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 0,
		Start: negzeroHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("零单价是合法费率，修正后的登记应成功: %v", err)
	}

	// 登记成功后：旧版登记结束仍是 3 月 31 日，实际结束变为 3 月 10 日并被新版替代；
	// 新版 0 分单价，从交接点起持续有效。
	views, err = b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("登记成功后应有两个版本，got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("版本应按生效时刻排列为 v1/v2: %q, %q", old.VersionID, nv.VersionID)
	}
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价被改写: %d", old.UnitPrice)
	}
	if old.End == nil || !old.End.Equal(negzeroOldEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(negzeroHandoff) {
		t.Fatalf("旧版实际结束应被截短到 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}
	if nv.UnitPrice != 0 || !nv.Start.Equal(negzeroHandoff) {
		t.Fatalf("新版应为 0 分单价、3 月 10 日起生效: %+v", nv)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("新版不填结束时间，两种结束都应为空: 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if !nv.EffectiveStart.Equal(negzeroHandoff) {
		t.Fatalf("新版应从交接点起持续有效: 实际开始=%v", nv.EffectiveStart)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q", nv.Replaces, nv.SupersededBy)
	}

	// 仍在 3 月 12 日这一受理时刻，用从未用过的标识引用新版、数量 4：
	// 正常受理（err 为空）后明确确认，单价与总价均为 0，拒绝原因为空。
	freeReq := QuoteRequest{
		RequestID: negzeroIDFreeNew, ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	}
	free, err := b.Quote(freeReq)
	if err != nil {
		t.Fatalf("引用零单价新版是正常受理，err 应为空: %v", err)
	}
	if !free.Confirmed {
		t.Fatalf("零单价新版应确认而非拒绝: %+v", free)
	}
	if free.UnitPrice != 0 || free.Total != 0 {
		t.Fatalf("免费报价单价/总价应均为 0: 单价=%d 总价=%d", free.UnitPrice, free.Total)
	}
	if free.Reason != ReasonNone {
		t.Fatalf("已确认的免费报价拒绝原因应为空: %q", free.Reason)
	}
	if free.Request != freeReq || !free.AcceptedAt.Equal(negzeroQuoteAt) {
		t.Fatalf("免费报价应保留新版来源与受理时刻: %+v", free)
	}

	// 同一时刻另用新标识引用旧版：正常受理（err 为空）后以 version_expired 拒绝。
	expiredReq := QuoteRequest{
		RequestID: negzeroIDExpiredOld, ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	}
	expired, err := b.Quote(expiredReq)
	if err != nil {
		t.Fatalf("引用已失效旧版是正常受理，err 应为空: %v", err)
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

	// 此前两份按旧版确认的报价在登记成功后仍可按各自标识查到：
	// 来源仍是旧版、数量 4、150 分单价、600 分总价，各自首次受理时刻保留。
	assertOld600 := func(t *testing.T, requestID string, wantReq QuoteRequest, wantAcceptedAt time.Time) {
		t.Helper()
		got, err := b.Lookup(requestID)
		if err != nil {
			t.Fatalf("历史确认报价应可按标识 %q 查到: %v", requestID, err)
		}
		if got.Request != wantReq {
			t.Fatalf("历史结果来源应仍是旧版原请求: %+v vs %+v", got.Request, wantReq)
		}
		if !got.Confirmed || got.UnitPrice != 150 || got.Total != 600 {
			t.Fatalf("历史结果应保留确认与 600 分金额: %+v", got)
		}
		if !got.AcceptedAt.Equal(wantAcceptedAt) {
			t.Fatalf("首次受理时刻应保留为 %v: %v", wantAcceptedAt, got.AcceptedAt)
		}
		if got.Reason != ReasonNone {
			t.Fatalf("已确认记录的拒绝原因应为空: %q", got.Reason)
		}
	}
	assertOld600(t, negzeroIDBeforeFail, reqBefore, negzeroFirstAcceptedAt)
	assertOld600(t, negzeroIDAfterFail, reqAfter, negzeroQuoteAt)
}
