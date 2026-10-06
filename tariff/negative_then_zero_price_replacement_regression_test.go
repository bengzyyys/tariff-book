package tariff

import (
	"errors"
	"testing"
	"time"
)

// “替代费率登记先误填负单价、随后修正为零单价重新登记”的回归时间线
// （全部时刻按 UTC 理解，结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 零点起生效，登记结束 2026-03-31 零点
//	新版 seat-v2：拟从 2026-03-10 零点起替代 v1，不填结束时间
//
// 第一次登记 v2 时单价误填为 -1，其余内容合法，必须返回既有的
// ErrInvalidUnitPrice 且整次回滚：不能留下 v2（版本标识也不被占用），
// 也不能把旧版提前截短。随后只把单价改为 0（免费费率允许登记），
// 沿用同一版本标识、生效时刻与替代来源再次登记，应成功。
// 全程使用写死的时刻与注入时钟，结论不依赖运行当天的真实日期。
// 固定时刻复用 second_truncation_regression_test.go：
// dtOldStart=2026-03-01、dtOldEnd=2026-03-31、dtEarlyHandoff=2026-03-10（UTC 零点）。

// TestNegativeThenZeroPriceReplacementRegression 串联校验：
// 负单价登记失败的回滚性、零单价替代登记的成功、免费报价与失效拒绝的区分，
// 以及登记成功前后两份旧版确认报价的持久保留。
func TestNegativeThenZeroPriceReplacementRegression(t *testing.T) {
	now, setNow := fixedClock(dtOldStart)
	b := NewBook(WithClock(now))
	oldEnd := dtOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: dtOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}

	// 3 月 5 日按旧版、数量 4 取得一份 600 分的确认报价，
	// 保留其请求标识与首次受理时刻，供登记成功后回查。
	firstAcceptedAt := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	setNow(firstAcceptedAt)
	firstReq := QuoteRequest{
		RequestID: "neg-zero-v1-0305", ItemID: "seat",
		VersionID: "seat-v1", Quantity: 4,
	}
	first, err := b.Quote(firstReq)
	if err != nil {
		t.Fatalf("3 月 5 日旧版报价是正常受理，err 应为空: %v", err)
	}
	if !first.Confirmed || first.UnitPrice != 150 || first.Total != 600 {
		t.Fatalf("3 月 5 日旧版报价应确认 150 分单价、600 分总价: %+v", first)
	}
	if !first.AcceptedAt.Equal(firstAcceptedAt) || first.Reason != ReasonNone {
		t.Fatalf("3 月 5 日报价受理时刻/拒绝原因异常: %+v", first)
	}

	// 第一次登记 v2：单价 -1 分，其余内容合法（3 月 10 日零点起替代 v1，
	// 不填结束时间），应返回既有的 ErrInvalidUnitPrice。
	err = b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: -1,
		Start: dtEarlyHandoff, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrInvalidUnitPrice) {
		t.Fatalf("负单价登记应返回 ErrInvalidUnitPrice, got %v", err)
	}

	// 失败后账本保持原状：仍只有 v1；单价、登记起止时间、实际有效结束
	// （仍是登记的 3 月 31 日，不能被提前截短到 3 月 10 日）与替代关系一致。
	assertOnlyV1BeforeReplacement(t, b)

	// 3 月 12 日使用新的请求标识仍按 v1、数量 4 报价：失败的替代登记
	// 没有改变旧版的可用时间，仍应确认 150 分单价和 600 分总价。
	secondAcceptedAt := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	setNow(secondAcceptedAt)
	secondReq := QuoteRequest{
		RequestID: "neg-zero-v1-0312", ItemID: "seat",
		VersionID: "seat-v1", Quantity: 4,
	}
	second, err := b.Quote(secondReq)
	if err != nil {
		t.Fatalf("失败登记后引用 v1 是正常受理，err 应为空: %v", err)
	}
	if !second.Confirmed || second.UnitPrice != 150 || second.Total != 600 {
		t.Fatalf("失败登记后旧版仍应按 150 分确认 600 分: %+v", second)
	}
	if !second.AcceptedAt.Equal(secondAcceptedAt) || second.Reason != ReasonNone {
		t.Fatalf("失败登记后旧版报价受理时刻/拒绝原因异常: %+v", second)
	}

	// 只把 v2 的单价改为 0 分，沿用刚才登记失败的版本标识、生效时刻和
	// 替代来源再次登记：失败不占用版本标识，免费费率允许登记，应成功。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 0,
		Start: dtEarlyHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("修正为零单价后沿用同一版本标识登记应成功, got %v", err)
	}

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("成功后应有两个版本, got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("版本应按生效时刻排列为 v1/v2: %q, %q", old.VersionID, nv.VersionID)
	}

	// 旧版：登记结束仍是 3 月 31 日，实际有效结束变为 3 月 10 日交接点，
	// 并指明被 v2 替代；单价与登记起点不变。
	if old.UnitPrice != 150 || !old.Start.Equal(dtOldStart) {
		t.Fatalf("旧版单价/起点被改写: %+v", old)
	}
	if old.End == nil || !old.End.Equal(dtOldEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日, got %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(dtEarlyHandoff) {
		t.Fatalf("旧版实际有效结束应变为 3 月 10 日交接点, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版应显示被 seat-v2 替代: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 新版：0 分单价，从交接点起持续有效（登记结束与实际结束均为 nil），
	// 替代来源为 v1。
	if nv.UnitPrice != 0 {
		t.Fatalf("新版单价应为 0 分, got %d", nv.UnitPrice)
	}
	if !nv.Start.Equal(dtEarlyHandoff) || !nv.EffectiveStart.Equal(dtEarlyHandoff) {
		t.Fatalf("新版应从 3 月 10 日交接点起生效: %+v", nv)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("新版应持续有效: 登记结束=%v 实际结束=%v", nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}

	// 时钟停在 3 月 12 日：用从未使用过的请求标识引用 v2、数量 4，
	// 结果应明确为已确认，单价与总价均为 0，拒绝原因为空。
	setNow(secondAcceptedAt)
	free, err := b.Quote(QuoteRequest{
		RequestID: "neg-zero-v2-free", ItemID: "seat",
		VersionID: "seat-v2", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("免费报价是正常受理，err 应为空: %v", err)
	}
	if !free.Confirmed {
		t.Fatalf("免费报价应明确为已确认，不能与拒绝混淆: %+v", free)
	}
	if free.UnitPrice != 0 || free.Total != 0 {
		t.Fatalf("免费报价单价与总价均应为 0: 单价=%d 总价=%d", free.UnitPrice, free.Total)
	}
	if free.Reason != ReasonNone {
		t.Fatalf("免费报价拒绝原因应为空, got %q", free.Reason)
	}

	// 同一时刻另用新标识引用 v1：应得到 version_expired 的未确认结果，
	// 调用本身不返回错误——免费确认与失效拒绝靠确认状态和拒绝原因区分。
	expired, err := b.Quote(QuoteRequest{
		RequestID: "neg-zero-v1-expired", ItemID: "seat",
		VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("失效拒绝是正常受理，err 应为空: %v", err)
	}
	if expired.Confirmed {
		t.Fatalf("旧版实际有效结束已提前到 3 月 10 日，3 月 12 日不能确认: %+v", expired)
	}
	if expired.Reason != ReasonVersionExpired {
		t.Fatalf("旧版报价拒绝原因应为 version_expired, got %q", expired.Reason)
	}
	if expired.UnitPrice != 0 || expired.Total != 0 {
		t.Fatalf("失效拒绝单价/总价应为零: 单价=%d 总价=%d",
			expired.UnitPrice, expired.Total)
	}

	// 此前两份按旧版确认的报价在登记成功后仍可按各自标识查到，
	// 继续保留旧版本、数量 4、150 分单价、600 分总价和各自首次受理时刻，
	// 不按当前费率（0 分或失效）重算或改写。
	gotFirst, err := b.Lookup(firstReq.RequestID)
	if err != nil {
		t.Fatalf("3 月 5 日历史报价应可查: %v", err)
	}
	if gotFirst != first {
		t.Fatalf("3 月 5 日历史报价被改写: %+v -> %+v", first, gotFirst)
	}
	if gotFirst.Request != firstReq || gotFirst.UnitPrice != 150 ||
		gotFirst.Total != 600 || !gotFirst.AcceptedAt.Equal(firstAcceptedAt) ||
		!gotFirst.Confirmed || gotFirst.Reason != ReasonNone {
		t.Fatalf("3 月 5 日历史报价未原样保留: %+v", gotFirst)
	}

	gotSecond, err := b.Lookup(secondReq.RequestID)
	if err != nil {
		t.Fatalf("3 月 12 日历史报价应可查: %v", err)
	}
	if gotSecond != second {
		t.Fatalf("3 月 12 日历史报价被改写: %+v -> %+v", second, gotSecond)
	}
	if gotSecond.Request != secondReq || gotSecond.UnitPrice != 150 ||
		gotSecond.Total != 600 || !gotSecond.AcceptedAt.Equal(secondAcceptedAt) ||
		!gotSecond.Confirmed || gotSecond.Reason != ReasonNone {
		t.Fatalf("3 月 12 日历史报价未原样保留: %+v", gotSecond)
	}

	// 两份历史记录各自保留自己的首次受理时刻，不能互相串改。
	if gotFirst.AcceptedAt.Equal(gotSecond.AcceptedAt) {
		t.Fatalf("两份历史报价的首次受理时刻应不同: %v", gotFirst.AcceptedAt)
	}
}

// TestNegativePriceReplacementFailureLeavesLedgerUnchanged 单独固定负单价
// 失败登记的回滚性：失败后 v1 的登记边界、实际有效区间与替代关系均与
// 登记前一致，且不存在 v2。
func TestNegativePriceReplacementFailureLeavesLedgerUnchanged(t *testing.T) {
	now, _ := fixedClock(dtOldStart)
	b := NewBook(WithClock(now))
	oldEnd := dtOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: dtOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}

	for _, price := range []int64{-1, -150, -1 << 40} {
		if err := b.RegisterVersion(RegisterRequest{
			ItemID: "seat", VersionID: "seat-v2", UnitPrice: price,
			Start: dtEarlyHandoff, Replaces: "seat-v1",
		}); !errors.Is(err, ErrInvalidUnitPrice) {
			t.Fatalf("单价 %d 的登记应返回 ErrInvalidUnitPrice, got %v", price, err)
		}
		assertOnlyV1BeforeReplacement(t, b)
	}
}

// assertOnlyV1BeforeReplacement 校验负单价登记失败后账本仍只有未被截短的 v1。
func assertOnlyV1BeforeReplacement(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("失败登记后应仍只有 v1, got %d 个版本: %+v", len(views), views)
	}
	v1 := views[0]
	if v1.VersionID != "seat-v1" {
		t.Fatalf("失败登记不应留下 v2: %+v", v1)
	}
	if v1.UnitPrice != 150 {
		t.Fatalf("旧版单价被改写: %d", v1.UnitPrice)
	}
	if !v1.Start.Equal(dtOldStart) {
		t.Fatalf("旧版登记起点被改写: %v", v1.Start)
	}
	if v1.End == nil || !v1.End.Equal(dtOldEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日, got %v", v1.End)
	}
	// 实际有效结束不能被失败的替代登记提前到 3 月 10 日，仍是登记的 3 月 31 日。
	if v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(dtOldEnd) {
		t.Fatalf("旧版实际有效结束应仍为 3 月 31 日, got %v", v1.EffectiveEnd)
	}
	if v1.Replaces != "" || v1.SupersededBy != "" {
		t.Fatalf("失败登记不应写入替代关系: Replaces=%q SupersededBy=%q",
			v1.Replaces, v1.SupersededBy)
	}

	// 交接点（含）之后查询实际生效版本：旧版未被截短时仍应命中 v1。
	view, err := b.EffectiveVersionAt("seat", dtEarlyHandoff)
	if err != nil {
		t.Fatalf("失败登记后 3 月 10 日仍应只有 v1 可用: %v", err)
	}
	if view.VersionID != "seat-v1" || view.UnitPrice != 150 {
		t.Fatalf("失败登记后交接点应仍选 v1（150 分）, got %s（%d 分）",
			view.VersionID, view.UnitPrice)
	}
}
