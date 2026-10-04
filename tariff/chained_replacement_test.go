package tariff

import (
	"errors"
	"testing"
	"time"
)

// 连续替代场景的固定时间线（费率时间以东八区零点表示，结束时刻不含）：
//
//	甲版 seat-v1：单价 150 分，2026-03-01 起生效，登记结束 2026-03-31
//	乙版 seat-v2：单价 180 分，2026-03-10 起替代甲版，登记结束 2026-03-25
//	丙版 seat-v3：单价 200 分，2026-03-20 起替代乙版，登记结束 2026-03-24
//
// 乙版先登记成功后再登记丙版：乙版本身已替代过旧版，随后又被新版替代。
// 所有时刻写死并注入固定时钟，结论不依赖执行当天的真实日期。
var (
	chainV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, east8)
	chainV1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, east8)
	chainV2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, east8)
	chainV2End   = time.Date(2026, 3, 25, 0, 0, 0, 0, east8)
	chainV3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, east8)
	chainV3End   = time.Date(2026, 3, 24, 0, 0, 0, 0, east8)
)

// registerChainV1V2 依次登记甲版、乙版，建立第一次替代关系。
func registerChainV1V2(t *testing.T, b *Book) {
	t.Helper()
	v1End := chainV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: chainV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	v2End := chainV2End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: chainV2Start, End: &v2End, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
}

// registerChainV3 登记丙版替代乙版，建立第二次替代关系。
func registerChainV3(t *testing.T, b *Book) {
	t.Helper()
	v3End := chainV3End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: chainV3Start, End: &v3End, Replaces: "seat-v2",
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}
}

// newChainedBook 返回一本已完成甲→乙→丙连续替代的账本及其时钟。
func newChainedBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	registerChainV1V2(t, b)
	registerChainV3(t, b)
	return b, setNow
}

// 连续替代成功后的版本视图：三版按生效时刻排列；
// 甲版实际结束保持 3 月 10 日、被乙版替代的关系不变；
// 乙版同时显示替代甲版与被丙版替代，实际结束改为 3 月 20 日；
// 丙版显示替代乙版，实际结束为 3 月 24 日；三版登记结束都保留原值。
func TestChainedReplacementVersionView(t *testing.T) {
	b, _ := newChainedBook(t, chainV1Start)

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("want 3 versions, got %d", len(views))
	}
	v1, v2, v3 := views[0], views[1], views[2]
	if v1.VersionID != "seat-v1" || v2.VersionID != "seat-v2" || v3.VersionID != "seat-v3" {
		t.Fatalf("版本应按生效时刻排列为 v1/v2/v3: %q, %q, %q",
			v1.VersionID, v2.VersionID, v3.VersionID)
	}

	// 甲版：登记结束仍是 3 月 31 日，实际结束保持第一次交接点 3 月 10 日，
	// 被乙版替代的关系不因丙版登记而改变。
	if v1.UnitPrice != 150 || !v1.Start.Equal(chainV1Start) {
		t.Fatalf("甲版登记信息异常: %+v", v1)
	}
	if v1.End == nil || !v1.End.Equal(chainV1End) {
		t.Fatalf("甲版登记结束应仍为 3 月 31 日: %v", v1.End)
	}
	if v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(chainV2Start) {
		t.Fatalf("甲版实际结束应保持 3 月 10 日: %v", v1.EffectiveEnd)
	}
	if v1.Replaces != "" || v1.SupersededBy != "seat-v2" {
		t.Fatalf("甲版替代关系异常: Replaces=%q SupersededBy=%q", v1.Replaces, v1.SupersededBy)
	}

	// 乙版：既显示替代甲版，又显示被丙版替代；
	// 登记结束仍是 3 月 25 日，实际结束被第二次交接截断到 3 月 20 日。
	if v2.UnitPrice != 180 || !v2.Start.Equal(chainV2Start) {
		t.Fatalf("乙版登记信息异常: %+v", v2)
	}
	if v2.End == nil || !v2.End.Equal(chainV2End) {
		t.Fatalf("乙版登记结束应仍为 3 月 25 日: %v", v2.End)
	}
	if v2.EffectiveEnd == nil || !v2.EffectiveEnd.Equal(chainV3Start) {
		t.Fatalf("乙版实际结束应为第二次交接点 3 月 20 日: %v", v2.EffectiveEnd)
	}
	if v2.Replaces != "seat-v1" || v2.SupersededBy != "seat-v3" {
		t.Fatalf("乙版应同时显示替代甲版与被丙版替代: Replaces=%q SupersededBy=%q",
			v2.Replaces, v2.SupersededBy)
	}

	// 丙版：显示替代乙版，登记结束与实际结束都是 3 月 24 日。
	if v3.UnitPrice != 200 || !v3.Start.Equal(chainV3Start) {
		t.Fatalf("丙版登记信息异常: %+v", v3)
	}
	if v3.End == nil || !v3.End.Equal(chainV3End) {
		t.Fatalf("丙版登记结束应为 3 月 24 日: %v", v3.End)
	}
	if v3.EffectiveEnd == nil || !v3.EffectiveEnd.Equal(chainV3End) {
		t.Fatalf("丙版实际结束应为 3 月 24 日: %v", v3.EffectiveEnd)
	}
	if v3.Replaces != "seat-v2" || v3.SupersededBy != "" {
		t.Fatalf("丙版替代关系异常: Replaces=%q SupersededBy=%q", v3.Replaces, v3.SupersededBy)
	}
}

// 第二次交接前后的报价行为：3 月 19 日乙版以数量 4 确认 720 分；
// 3 月 20 日起引用乙版（新的请求标识）返回 version_expired 拒绝，
// 引用丙版确认 800 分；拒绝是正常受理而非调用错误，金额为零且保留指定版本。
func TestChainedReplacementQuotesAcrossSecondHandoff(t *testing.T) {
	b, setNow := newChainedBook(t, chainV1Start)

	// 第二次交接前：乙版仍有效，数量 4 确认 180*4=720 分。
	setNow(time.Date(2026, 3, 19, 0, 0, 0, 0, east8))
	before, err := b.Quote(QuoteRequest{
		RequestID: "chain-before", ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("确认是正常受理，err 应为空: %v", err)
	}
	if !before.Confirmed || before.UnitPrice != 180 || before.Total != 720 {
		t.Fatalf("3 月 19 日乙版报价应确认 720 分: %+v", before)
	}

	// 第二次交接点：乙版已失效，丙版生效。
	setNow(chainV3Start)

	expiredReq := QuoteRequest{
		RequestID: "chain-v2-expired", ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	}
	expired, err := b.Quote(expiredReq)
	if err != nil {
		t.Fatalf("拒绝是正常受理，err 应为空: %v", err)
	}
	if expired.Confirmed || expired.Reason != ReasonVersionExpired {
		t.Fatalf("3 月 20 日引用乙版应返回 version_expired 拒绝: %+v", expired)
	}
	if expired.UnitPrice != 0 || expired.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: 单价=%d 总价=%d", expired.UnitPrice, expired.Total)
	}
	if expired.Request != expiredReq {
		t.Fatalf("拒绝结果必须保留调用者指定的版本等请求内容: %+v vs %+v", expired.Request, expiredReq)
	}

	confirmed, err := b.Quote(QuoteRequest{
		RequestID: "chain-v3-800", ItemID: "seat", VersionID: "seat-v3", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("确认是正常受理，err 应为空: %v", err)
	}
	if !confirmed.Confirmed || confirmed.UnitPrice != 200 || confirmed.Total != 800 {
		t.Fatalf("3 月 20 日丙版报价应确认 800 分: %+v", confirmed)
	}
}

// 第二次交接前已确认的乙版报价，在丙版登记之后查询仍是
// 原请求、原金额和原受理时刻，不随丙版登记或时间推进改变。
func TestChainedReplacementConfirmedQuoteSurvives(t *testing.T) {
	now, setNow := fixedClock(chainV1Start)
	b := NewBook(WithClock(now))
	registerChainV1V2(t, b)

	// 第二次交接前、丙版尚未登记时确认乙版报价。
	acceptedAt := time.Date(2026, 3, 19, 0, 0, 0, 0, east8)
	setNow(acceptedAt)
	req := QuoteRequest{RequestID: "chain-keep-720", ItemID: "seat", VersionID: "seat-v2", Quantity: 4}
	first, err := b.Quote(req)
	if err != nil || !first.Confirmed {
		t.Fatalf("交接前报价应确认: %v %+v", err, first)
	}
	if first.UnitPrice != 180 || first.Total != 720 {
		t.Fatalf("交接前金额: 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	if !first.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("首次受理时刻: %v", first.AcceptedAt)
	}

	// 随后登记丙版并推进到第二次交接点之后：乙版已失效，丙版 200 分已生效。
	registerChainV3(t, b)
	setNow(chainV3Start.Add(24 * time.Hour))

	got, err := b.Lookup("chain-keep-720")
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("历史报价被丙版登记改写: %+v -> %+v", first, got)
	}
	if got.Request != req || got.UnitPrice != 180 || got.Total != 720 ||
		!got.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("历史报价未保留原请求/金额/受理时刻: %+v", got)
	}

	// 原样重试同样返回首次结果，而不是按当前费率重算或改判拒绝。
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatal(err)
	}
	if replay != first {
		t.Fatalf("重试结果被改写: %+v -> %+v", first, replay)
	}
}

// 第二次替代选错来源：丙版同样从 3 月 20 日开始却填写替代甲版，
// 即使甲版登记结束仍是 3 月 31 日，也必须返回 ErrInvalidReplacement；
// 失败不改变甲、乙两版的实际结束与替代关系，也不留下丙版；
// 随后沿用丙版标识改为替代乙版，仍可按原条件成功。
func TestChainedReplacementWrongSourceThenFixed(t *testing.T) {
	now, _ := fixedClock(chainV1Start)
	b := NewBook(WithClock(now))
	registerChainV1V2(t, b)

	v3End := chainV3End
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: chainV3Start, End: &v3End, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrInvalidReplacement) {
		t.Fatalf("替代已被截断的甲版应返回 ErrInvalidReplacement, got %v", err)
	}

	// 失败不留痕迹：仍只有甲、乙两版，边界与替代关系保持第一次交接后的状态。
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("失败登记后应仍只有两个版本，got %d: %+v", len(views), views)
	}
	v1, v2 := views[0], views[1]
	if v1.VersionID != "seat-v1" || v2.VersionID != "seat-v2" {
		t.Fatalf("失败登记后版本集合/顺序异常: %q, %q", v1.VersionID, v2.VersionID)
	}
	if v1.End == nil || !v1.End.Equal(chainV1End) {
		t.Fatalf("甲版登记结束被改写: %v", v1.End)
	}
	if v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(chainV2Start) {
		t.Fatalf("甲版实际结束应仍为 3 月 10 日: %v", v1.EffectiveEnd)
	}
	if v1.Replaces != "" || v1.SupersededBy != "seat-v2" {
		t.Fatalf("甲版替代关系被改写: Replaces=%q SupersededBy=%q", v1.Replaces, v1.SupersededBy)
	}
	if v2.End == nil || !v2.End.Equal(chainV2End) {
		t.Fatalf("乙版登记结束被改写: %v", v2.End)
	}
	if v2.EffectiveEnd == nil || !v2.EffectiveEnd.Equal(chainV2End) {
		t.Fatalf("乙版实际结束不能被失败登记提前截断，应仍为 3 月 25 日: %v", v2.EffectiveEnd)
	}
	if v2.Replaces != "seat-v1" || v2.SupersededBy != "" {
		t.Fatalf("乙版替代关系被改写: Replaces=%q SupersededBy=%q", v2.Replaces, v2.SupersededBy)
	}

	// 沿用丙版标识，改为替代乙版，按原条件登记成功。
	registerChainV3(t, b)

	views, err = b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("修正后应有三个版本, got %d", len(views))
	}
	v1, v2, v3 := views[0], views[1], views[2]
	if v1.VersionID != "seat-v1" || v2.VersionID != "seat-v2" || v3.VersionID != "seat-v3" {
		t.Fatalf("修正后版本集合/顺序异常: %q, %q, %q", v1.VersionID, v2.VersionID, v3.VersionID)
	}
	if v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(chainV2Start) ||
		v1.SupersededBy != "seat-v2" {
		t.Fatalf("甲版实际结束与替代关系应保持不变: %+v", v1)
	}
	if v2.EffectiveEnd == nil || !v2.EffectiveEnd.Equal(chainV3Start) ||
		v2.Replaces != "seat-v1" || v2.SupersededBy != "seat-v3" {
		t.Fatalf("乙版应被截断到 3 月 20 日并显示双向替代关系: %+v", v2)
	}
	if v3.Replaces != "seat-v2" || v3.EffectiveEnd == nil || !v3.EffectiveEnd.Equal(chainV3End) {
		t.Fatalf("丙版应显示替代乙版、实际结束 3 月 24 日: %+v", v3)
	}
}
