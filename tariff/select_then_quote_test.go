package tariff

import (
	"errors"
	"testing"
	"time"
)

// “按时刻选版后再发起报价”完整使用过程的固定时间线（全部按 UTC 解释，结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00
//	新版 seat-v2：单价 180 分，2026-03-10 00:00 起替代旧版，2026-03-20 00:00 结束
//
// 查询时刻有两个：selectBeforeHandoff（3 月 9 日，旧版仍生效）与
// selectAfterNewEnded（3 月 20 日零点，两版都不生效）；
// 报价受理时刻始终是 quoteAcceptedAt（3 月 15 日 10 点）。
//
// 这段回归保护的既有约定：EffectiveVersionAt 回答的是“调用方所查时刻”生效的版本，
// Quote 则按“新请求首次受理的时刻”判断指定版本是否可用——两者可以使用不同的时刻，
// 查询结果不能成为随后报价的时间依据：3 月 9 日查到旧版，不代表 3 月 15 日引用旧版
// 仍会被确认；3 月 20 日查询失败，也不影响仍在 3 月 15 日受理的新报价。
var (
	selectBeforeHandoff = time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	selectAfterNewEnded = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	quoteAcceptedAt     = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
)

// 两笔报价各用一个从未使用过的非空请求标识。
const (
	selectQuoteOldID = "select-then-quote-old-v1"
	selectQuoteNewID = "select-then-quote-new-v2"
)

// newSelectThenQuoteBook 登记好两版并把账本时钟固定在报价受理时刻 3 月 15 日 10 点。
func newSelectThenQuoteBook(t *testing.T) *Book {
	t.Helper()
	now, _ := fixedClock(quoteAcceptedAt)
	b := NewBook(WithClock(now))

	v1End := effV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: effV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	v2End := effV2End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: effV2Start, End: &v2End, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b
}

// TestSelectVersionThenQuote 串联“按时刻选版后再发起报价”的完整使用过程：
// 查询时刻（3 月 9 日、3 月 20 日）与报价受理时刻（3 月 15 日 10 点）始终分开，
// 两次查询都不能成为报价的时间依据，公开入口与拒绝含义保持不变。
func TestSelectVersionThenQuote(t *testing.T) {
	b := newSelectThenQuoteBook(t)

	// 前提：受理时刻既不是查询的 3 月 9 日，也不是查询的 3 月 20 日，
	// 且落在新版实际有效期 [03-10, 03-20) 内、旧版实际有效期之外；
	// 数量 4 与任一单价相乘都不会溢出。
	if quoteAcceptedAt.Equal(selectBeforeHandoff) || quoteAcceptedAt.Equal(selectAfterNewEnded) {
		t.Fatal("测试前提不成立：受理时刻必须与两个查询时刻不同")
	}
	if 180*4 != 720 {
		t.Fatal("测试前提不成立：180 × 4 应为 720")
	}

	// ① 查询 3 月 9 日的生效版本：应为旧版。
	//    视图里同时能看到登记结束（3 月 31 日，尚未到）与实际结束（3 月 10 日）的区别。
	oldView, err := b.EffectiveVersionAt("seat", selectBeforeHandoff)
	if err != nil {
		t.Fatalf("3 月 9 日旧版应生效: %v", err)
	}
	if oldView.VersionID != "seat-v1" || oldView.UnitPrice != 150 {
		t.Fatalf("3 月 9 日应选旧版: %+v", oldView)
	}
	if oldView.End == nil || !oldView.End.Equal(effV1End) {
		t.Fatalf("旧版登记结束应为 3 月 31 日: %v", oldView.End)
	}
	if oldView.EffectiveEnd == nil || !oldView.EffectiveEnd.Equal(effV2Start) {
		t.Fatalf("旧版实际结束应已截到 3 月 10 日: %v", oldView.EffectiveEnd)
	}
	if oldView.End.Equal(*oldView.EffectiveEnd) {
		t.Fatalf("登记结束与实际结束必须能区分: 登记结束=%v 实际结束=%v",
			oldView.End, oldView.EffectiveEnd)
	}
	if oldView.SupersededBy != "seat-v2" {
		t.Fatalf("旧版应显示被新版替代: %+v", oldView)
	}

	// ② 用从未使用过的请求标识引用这个旧版报价。
	//    受理时刻是 3 月 15 日 10 点（账本时钟），不是刚才查询用的 3 月 9 日：
	//    旧版实际有效期已止于 3 月 10 日，即使登记结束 3 月 31 日尚未到，
	//    仍应正常受理（err 为空）后以 version_expired 拒绝，单价、总价均为零。
	oldReq := QuoteRequest{
		RequestID: selectQuoteOldID, ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	}
	rejected, err := b.Quote(oldReq)
	if err != nil {
		t.Fatalf("引用已失效旧版是正常受理，调用错误应为空, got %v", err)
	}
	if rejected.Confirmed {
		t.Fatalf("旧版在受理时刻已失效，不能确认: %+v", rejected)
	}
	if rejected.Reason != ReasonVersionExpired {
		t.Fatalf("拒绝原因应为 version_expired, got %q", rejected.Reason)
	}
	if rejected.UnitPrice != 0 || rejected.Total != 0 {
		t.Fatalf("拒绝结果单价和总价均应为零: 单价=%d 总价=%d",
			rejected.UnitPrice, rejected.Total)
	}
	// 结果仍保留提交的费率项、旧版标识和数量，不自动改用新版确认。
	if rejected.Request != oldReq {
		t.Fatalf("拒绝结果必须原样保留提交的费率项、旧版标识与数量: %+v vs %+v",
			rejected.Request, oldReq)
	}
	// 首次受理时刻是 3 月 15 日 10 点，不能写成查询时刻 3 月 9 日。
	if !rejected.AcceptedAt.Equal(quoteAcceptedAt) {
		t.Fatalf("首次受理时刻应为 3 月 15 日 10 点, got %v", rejected.AcceptedAt)
	}
	if rejected.AcceptedAt.Equal(selectBeforeHandoff) {
		t.Fatalf("受理时刻不能取自查询时刻 3 月 9 日: %v", rejected.AcceptedAt)
	}

	// ③ 再查询 3 月 20 日零点：新版结束时刻不含在有效期内，应返回 ErrNoEffectiveVersion，
	//    旧版不会因新版到期重新生效。这次未来时刻的查询同样是只读的。
	if _, err = b.EffectiveVersionAt("seat", selectAfterNewEnded); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("3 月 20 日零点应无生效版本且旧版不恢复, got %v", err)
	}

	// ④ 查询失败后，在原来的受理时刻（时钟从未推进）用另一个新标识引用新版：
	//    3 月 15 日 10 点在新版实际有效期内，应按 180 分单价确认 720 分，
	//    拒绝原因为空，来源为新版；未来时刻的查询失败不能成为这笔报价的时间依据。
	newReq := QuoteRequest{
		RequestID: selectQuoteNewID, ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	}
	confirmed, err := b.Quote(newReq)
	if err != nil {
		t.Fatalf("引用有效新版是正常受理，调用错误应为空, got %v", err)
	}
	if !confirmed.Confirmed {
		t.Fatalf("新版在受理时刻有效，应确认, got %+v", confirmed)
	}
	if confirmed.UnitPrice != 180 || confirmed.Total != 720 {
		t.Fatalf("应按 180 分单价确认 720 分: 单价=%d 总价=%d",
			confirmed.UnitPrice, confirmed.Total)
	}
	if confirmed.Reason != ReasonNone {
		t.Fatalf("确认结果拒绝原因应为空, got %q", confirmed.Reason)
	}
	if confirmed.Request != newReq {
		t.Fatalf("确认结果来源必须是提交的新版请求: %+v vs %+v",
			confirmed.Request, newReq)
	}
	if !confirmed.AcceptedAt.Equal(quoteAcceptedAt) {
		t.Fatalf("首次受理时刻仍应是 3 月 15 日 10 点, got %v", confirmed.AcceptedAt)
	}

	// ⑤ 两笔报价都应能按各自标识查到，且与首次返回的结果逐字段一致。
	gotRejected, err := b.Lookup(selectQuoteOldID)
	if err != nil {
		t.Fatalf("首次拒绝也应可按标识查询: %v", err)
	}
	if gotRejected != rejected {
		t.Fatalf("旧版拒绝结果 Lookup 与首次返回不一致: 首次=%+v 取回=%+v",
			rejected, gotRejected)
	}
	gotConfirmed, err := b.Lookup(selectQuoteNewID)
	if err != nil {
		t.Fatalf("新版确认结果应可按标识查询: %v", err)
	}
	if gotConfirmed != confirmed {
		t.Fatalf("新版确认结果 Lookup 与首次返回不一致: 首次=%+v 取回=%+v",
			confirmed, gotConfirmed)
	}
}
