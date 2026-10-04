package tariff

import (
	"errors"
	"testing"
	"time"
)

// 同一费率项连续替代的回归保障：覆盖“已经替代过旧版的版本，后来又被更新版替代”。
//
// 固定时间线（均为 2026 年 UTC 零点，结束时刻不含；时钟由测试注入，结论不依赖执行当天）：
//
//	甲 seat-chain-v1：单价 150 分，2026-03-01 生效，登记结束 2026-03-31
//	乙 seat-chain-v2：单价 180 分，2026-03-10 起替代甲，登记结束 2026-03-25
//	丙 seat-chain-v3：单价 200 分，2026-03-20 起替代乙，登记结束 2026-03-24
//
// 登记顺序也是时间顺序：甲 → 乙 → 丙。乙先成功，之后才登记丙。
var (
	chainItem = "seat-chain"

	chainV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	chainV1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	chainV2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	chainV2End   = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	chainV3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	chainV3End   = time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC)

	chainBeforeSecondHandoff = time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC)
)

// newChainBook 返回按 甲 → 乙 → 丙 顺序登记完三版的账本和可手动设置的时钟。
func newChainBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))

	v1End := chainV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: chainItem, VersionID: "seat-chain-v1", UnitPrice: 150,
		Start: chainV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register 甲: %v", err)
	}
	v2End := chainV2End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: chainItem, VersionID: "seat-chain-v2", UnitPrice: 180,
		Start: chainV2Start, End: &v2End, Replaces: "seat-chain-v1",
	}); err != nil {
		t.Fatalf("register 乙: %v", err)
	}
	v3End := chainV3End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: chainItem, VersionID: "seat-chain-v3", UnitPrice: 200,
		Start: chainV3Start, End: &v3End, Replaces: "seat-chain-v2",
	}); err != nil {
		t.Fatalf("register 丙: %v", err)
	}
	return b, setNow
}

// chainViewByID 把按生效时间排列的版本视图改为按版本标识索引，便于逐版断言。
func chainViewByID(t *testing.T, b *Book) map[string]VersionView {
	t.Helper()
	views, err := b.ItemVersions(chainItem)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]VersionView, len(views))
	for _, v := range views {
		byID[v.VersionID] = v
	}
	return byID
}

// 三版全部登记后，版本查询仍按生效时间排列，登记结束保留原值，
// 实际有效结束与替代关系逐版正确：甲被乙截断的关系不变，乙同时替代甲且被丙截断。
func TestChainReplacementVersionView(t *testing.T) {
	b, _ := newChainBook(t, chainV1Start)

	views, err := b.ItemVersions(chainItem)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("want 3 versions, got %d", len(views))
	}
	got := []string{views[0].VersionID, views[1].VersionID, views[2].VersionID}
	want := []string{"seat-chain-v1", "seat-chain-v2", "seat-chain-v3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("版本应按生效时间排列为 %v, got %v", want, got)
		}
	}

	v := chainViewByID(t, b)

	// 甲：登记结束仍为 3 月 31 日；实际结束保持第一次交接点 3 月 10 日，
	// 被乙替代的关系不随丙的登记改变。
	jia := v["seat-chain-v1"]
	if jia.UnitPrice != 150 || !jia.Start.Equal(chainV1Start) {
		t.Fatalf("甲登记信息异常: %+v", jia)
	}
	if jia.End == nil || !jia.End.Equal(chainV1End) {
		t.Fatalf("甲登记结束应保留原值 3 月 31 日, got %v", jia.End)
	}
	if jia.EffectiveEnd == nil || !jia.EffectiveEnd.Equal(chainV2Start) {
		t.Fatalf("甲实际结束应保持 3 月 10 日, got %v", jia.EffectiveEnd)
	}
	if jia.Replaces != "" || jia.SupersededBy != "seat-chain-v2" {
		t.Fatalf("甲替代关系异常: Replaces=%q SupersededBy=%q", jia.Replaces, jia.SupersededBy)
	}

	// 乙：仍显示它替代甲，同时显示被丙替代；登记结束保留 3 月 25 日，
	// 实际结束被第二次交接截断到 3 月 20 日。
	yi := v["seat-chain-v2"]
	if yi.UnitPrice != 180 || !yi.Start.Equal(chainV2Start) {
		t.Fatalf("乙登记信息异常: %+v", yi)
	}
	if yi.End == nil || !yi.End.Equal(chainV2End) {
		t.Fatalf("乙登记结束应保留原值 3 月 25 日, got %v", yi.End)
	}
	if yi.EffectiveEnd == nil || !yi.EffectiveEnd.Equal(chainV3Start) {
		t.Fatalf("乙实际结束应被丙截断到 3 月 20 日, got %v", yi.EffectiveEnd)
	}
	if yi.Replaces != "seat-chain-v1" || yi.SupersededBy != "seat-chain-v3" {
		t.Fatalf("乙应同时替代甲且被丙替代: Replaces=%q SupersededBy=%q",
			yi.Replaces, yi.SupersededBy)
	}

	// 丙：替代乙，登记结束即实际结束 3 月 24 日，尚无后续版本。
	bing := v["seat-chain-v3"]
	if bing.UnitPrice != 200 || !bing.Start.Equal(chainV3Start) {
		t.Fatalf("丙登记信息异常: %+v", bing)
	}
	if bing.End == nil || !bing.End.Equal(chainV3End) {
		t.Fatalf("丙登记结束应为 3 月 24 日, got %v", bing.End)
	}
	if bing.EffectiveEnd == nil || !bing.EffectiveEnd.Equal(chainV3End) {
		t.Fatalf("丙实际结束应为登记的 3 月 24 日, got %v", bing.EffectiveEnd)
	}
	if bing.Replaces != "seat-chain-v2" || bing.SupersededBy != "" {
		t.Fatalf("丙替代关系异常: Replaces=%q SupersededBy=%q",
			bing.Replaces, bing.SupersededBy)
	}
}

// 第二次交接前后的指定版本报价：
// 3 月 19 日乙仍在实际有效期内，数量 4 确认 720 分；
// 3 月 20 日起引用乙是 err 为空的 version_expired 拒绝（金额为零、来源保留），
// 引用丙确认 800 分。拒绝本身不是调用错误。
func TestChainQuotesAcrossSecondHandoff(t *testing.T) {
	b, setNow := newChainBook(t, chainBeforeSecondHandoff)

	// 第二次交接前的 3 月 19 日：乙有效（[03-10, 03-20)），180 × 4 = 720。
	setNow(chainBeforeSecondHandoff)
	yiReq := QuoteRequest{
		RequestID: "chain-yi-before-handoff",
		ItemID:    chainItem, VersionID: "seat-chain-v2", Quantity: 4,
	}
	before, err := b.Quote(yiReq)
	if err != nil {
		t.Fatalf("交接前引用乙不应返回调用错误: %v", err)
	}
	if !before.Confirmed || before.UnitPrice != 180 || before.Total != 720 {
		t.Fatalf("交接前乙报价应确认 720 分: %+v", before)
	}
	if before.Request != yiReq {
		t.Fatalf("确认结果必须保留调用者指定的版本: %+v vs %+v", before.Request, yiReq)
	}
	if !before.AcceptedAt.Equal(chainBeforeSecondHandoff) {
		t.Fatalf("受理时刻应为注入时钟的 3 月 19 日: %v", before.AcceptedAt)
	}

	// 交接点 3 月 20 日本身：乙的实际结束不含该点，用新的请求标识引用乙必须拒绝。
	setNow(chainV3Start)
	expiredReq := QuoteRequest{
		RequestID: "chain-yi-at-second-handoff",
		ItemID:    chainItem, VersionID: "seat-chain-v2", Quantity: 4,
	}
	expired, err := b.Quote(expiredReq)
	if err != nil {
		t.Fatalf("引用已被丙替代的乙是正常受理，err 应为空, got %v", err)
	}
	if expired.Confirmed {
		t.Fatalf("第二次交接点引用乙不能确认: %+v", expired)
	}
	if expired.Reason != ReasonVersionExpired {
		t.Fatalf("拒绝原因=%q, want %q", expired.Reason, ReasonVersionExpired)
	}
	// 拒绝的单价和总价为零，结果保留调用者指定的版本，不自动换成丙。
	if expired.UnitPrice != 0 || expired.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: %d/%d", expired.UnitPrice, expired.Total)
	}
	if expired.Request != expiredReq {
		t.Fatalf("拒绝结果必须保留调用者指定的乙版: %+v vs %+v", expired.Request, expiredReq)
	}
	if !expired.AcceptedAt.Equal(chainV3Start) {
		t.Fatalf("拒绝受理时刻应为 3 月 20 日: %v", expired.AcceptedAt)
	}

	// 同一时刻引用丙：已生效（[03-20, 03-24)），200 × 4 = 800。
	bingReq := QuoteRequest{
		RequestID: "chain-bing-at-second-handoff",
		ItemID:    chainItem, VersionID: "seat-chain-v3", Quantity: 4,
	}
	bing, err := b.Quote(bingReq)
	if err != nil {
		t.Fatalf("交接点引用丙不应返回调用错误: %v", err)
	}
	if !bing.Confirmed || bing.Reason != ReasonNone {
		t.Fatalf("交接点引用丙应确认: %+v", bing)
	}
	if bing.UnitPrice != 200 || bing.Total != 800 {
		t.Fatalf("丙报价应为 800 分: 单价=%d 总价=%d", bing.UnitPrice, bing.Total)
	}
	if bing.Request != bingReq || !bing.AcceptedAt.Equal(chainV3Start) {
		t.Fatalf("丙确认结果的请求来源/受理时刻异常: %+v", bing)
	}

	// 交接点之后引用乙同样拒绝；甲早已失效，不会在任何时点恢复。
	setNow(chainV3Start.Add(time.Hour))
	later, err := b.Quote(QuoteRequest{
		RequestID: "chain-yi-after-handoff",
		ItemID:    chainItem, VersionID: "seat-chain-v2", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("交接后引用乙 err 应为空: %v", err)
	}
	if later.Confirmed || later.Reason != ReasonVersionExpired {
		t.Fatalf("交接后引用乙应仍为 version_expired 拒绝: %+v", later)
	}
	jia, err := b.Quote(QuoteRequest{
		RequestID: "chain-jia-after-second-handoff",
		ItemID:    chainItem, VersionID: "seat-chain-v1", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("交接后引用甲 err 应为空: %v", err)
	}
	if jia.Confirmed || jia.Reason != ReasonVersionExpired {
		t.Fatalf("甲被乙替代后不能因丙的登记恢复生效: %+v", jia)
	}
}

// 第二次交接前已确认的乙版 720 分报价，在丙登记、第二次交接发生之后查询，
// 仍是原请求、原金额、原受理时刻，不随丙登记改变，也不按当前费率重算。
func TestChainConfirmedQuoteSurvivesSecondHandoff(t *testing.T) {
	b, setNow := newChainBook(t, chainBeforeSecondHandoff)

	req := QuoteRequest{
		RequestID: "chain-keep-720",
		ItemID:    chainItem, VersionID: "seat-chain-v2", Quantity: 4,
	}
	first, err := b.Quote(req)
	if err != nil || !first.Confirmed {
		t.Fatalf("交接前乙版报价应确认: %v %+v", err, first)
	}
	if first.UnitPrice != 180 || first.Total != 720 {
		t.Fatalf("交接前金额应为 720 分: 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	if !first.AcceptedAt.Equal(chainBeforeSecondHandoff) {
		t.Fatalf("首次受理时刻应为 3 月 19 日: %v", first.AcceptedAt)
	}

	// 推进到第二次交接点之后（丙已生效、乙已失效），历史确认原样保留。
	setNow(chainV3Start.Add(24 * time.Hour))

	got, err := b.Lookup("chain-keep-720")
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("历史报价被重算: %+v -> %+v", first, got)
	}
	if got.Request != req || got.UnitPrice != 180 || got.Total != 720 ||
		!got.AcceptedAt.Equal(chainBeforeSecondHandoff) {
		t.Fatalf("历史报价未保留原请求/金额/受理时刻: %+v", got)
	}

	// 原样重试同样返回首次结果，而不是按丙的 200 分重算或判为失效。
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatal(err)
	}
	if replay != first {
		t.Fatalf("原样重试被重算: %+v -> %+v", first, replay)
	}
}

// 第二次替代选错来源必须失败且整次回滚：丙同样从 3 月 20 日开始，
// 却填写替代甲。甲的当前实际有效区间早在 3 月 10 日就被乙截断，
// 即使甲登记结束仍是 3 月 31 日，也返回现有的 ErrInvalidReplacement。
func TestChainSecondReplacementWrongTargetRejected(t *testing.T) {
	// 只登记甲、乙两版的账本（丙尚未登记）。
	now, _ := fixedClock(chainV1Start)
	b := NewBook(WithClock(now))
	v1End := chainV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: chainItem, VersionID: "seat-chain-v1", UnitPrice: 150,
		Start: chainV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register 甲: %v", err)
	}
	v2End := chainV2End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: chainItem, VersionID: "seat-chain-v2", UnitPrice: 180,
		Start: chainV2Start, End: &v2End, Replaces: "seat-chain-v1",
	}); err != nil {
		t.Fatalf("register 乙: %v", err)
	}

	// 丙：开始时刻与合法方案相同，但错误地把替代来源填成甲。
	v3End := chainV3End
	err := b.RegisterVersion(RegisterRequest{
		ItemID: chainItem, VersionID: "seat-chain-v3", UnitPrice: 200,
		Start: chainV3Start, End: &v3End, Replaces: "seat-chain-v1",
	})
	if !errors.Is(err, ErrInvalidReplacement) {
		t.Fatalf("第二次替代选错来源应返回 ErrInvalidReplacement, got %v", err)
	}

	// 失败不留痕迹：仍只有甲、乙两版，丙及其标识都不存在。
	views, err := b.ItemVersions(chainItem)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("失败登记后应仍只有两个版本, got %d: %+v", len(views), views)
	}
	v := chainViewByID(t, b)
	if _, ok := v["seat-chain-v3"]; ok {
		t.Fatalf("失败的丙版不能留在账本中: %+v", v["seat-chain-v3"])
	}

	// 甲：登记结束 3 月 31 日、实际结束 3 月 10 日、被乙替代——全部不变。
	jia := v["seat-chain-v1"]
	if jia.End == nil || !jia.End.Equal(chainV1End) {
		t.Fatalf("甲登记结束被失败登记改写: %v", jia.End)
	}
	if jia.EffectiveEnd == nil || !jia.EffectiveEnd.Equal(chainV2Start) {
		t.Fatalf("甲实际结束应仍为 3 月 10 日, got %v", jia.EffectiveEnd)
	}
	if jia.Replaces != "" || jia.SupersededBy != "seat-chain-v2" {
		t.Fatalf("甲替代关系不能被失败登记改写: Replaces=%q SupersededBy=%q",
			jia.Replaces, jia.SupersededBy)
	}

	// 乙：登记结束 3 月 25 日、实际结束仍为登记值，替代甲的关系不变，不能出现被丙替代。
	yi := v["seat-chain-v2"]
	if yi.End == nil || !yi.End.Equal(chainV2End) {
		t.Fatalf("乙登记结束被失败登记改写: %v", yi.End)
	}
	if yi.EffectiveEnd == nil || !yi.EffectiveEnd.Equal(chainV2End) {
		t.Fatalf("乙实际结束不能被失败登记截断到 3 月 20 日, got %v", yi.EffectiveEnd)
	}
	if yi.Replaces != "seat-chain-v1" || yi.SupersededBy != "" {
		t.Fatalf("乙替代关系不能被失败登记改写: Replaces=%q SupersededBy=%q",
			yi.Replaces, yi.SupersededBy)
	}

	// 沿用丙的版本标识，把替代来源改为乙，其余条件不变，仍可成功。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: chainItem, VersionID: "seat-chain-v3", UnitPrice: 200,
		Start: chainV3Start, End: &v3End, Replaces: "seat-chain-v2",
	}); err != nil {
		t.Fatalf("失败登记不占用标识，改为替代乙后沿用丙标识应成功: %v", err)
	}
	views, err = b.ItemVersions(chainItem)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("成功后应有三个版本, got %d", len(views))
	}
	v = chainViewByID(t, b)
	jia = v["seat-chain-v1"]
	yi = v["seat-chain-v2"]
	bing := v["seat-chain-v3"]
	if jia.EffectiveEnd == nil || !jia.EffectiveEnd.Equal(chainV2Start) ||
		jia.SupersededBy != "seat-chain-v2" {
		t.Fatalf("成功后甲仍止于 3 月 10 日且被乙替代: %+v", jia)
	}
	if yi.EffectiveEnd == nil || !yi.EffectiveEnd.Equal(chainV3Start) ||
		yi.Replaces != "seat-chain-v1" || yi.SupersededBy != "seat-chain-v3" {
		t.Fatalf("成功后乙应替代甲且实际结束被丙截断到 3 月 20 日: %+v", yi)
	}
	if bing.Replaces != "seat-chain-v2" ||
		bing.EffectiveEnd == nil || !bing.EffectiveEnd.Equal(chainV3End) {
		t.Fatalf("成功后丙应替代乙、实际结束为 3 月 24 日: %+v", bing)
	}
}
