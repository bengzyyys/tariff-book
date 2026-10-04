package tariff

import (
	"errors"
	"testing"
	"time"
)

// “后来补登过去生效的替代版本”场景的固定时间线（全部按 UTC 解释，结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00
//	新版 seat-v2：单价 180 分，2026-03-10 00:00 起替代旧版，不填写结束时间
//	首次报价受理时刻：2026-03-15 10:00——此时旧版尚未被替代，报价按旧版实际有效期判定合法
//
// 关键在于登记顺序与生效顺序相反：报价先于替代关系确认，
// 而后来成功登记的新版把旧版的实际结束提前到了该笔报价受理时刻之前（3 月 10 日）。
// 所有时刻在代码中写死并注入固定时钟，结论不依赖运行当天或真实时间等待。
var (
	retroOldStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	retroOldEnd     = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	retroHandoff    = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 新版生效点（不含旧版）
	retroAcceptedAt = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
)

// TestRetroactivelyRegisteredReplacement 完整回归：
// 先在旧版有效期内取得确认报价，再在同一受理时刻补登一个早已生效的替代版本，
// 历史确认结果必须原样保留，新报价按补登后的版本现状判定。
func TestRetroactivelyRegisteredReplacement(t *testing.T) {
	now, setNow := fixedClock(retroOldStart)
	b := NewBook(WithClock(now))

	// 登记旧版 seat-v1：单价 150 分，[2026-03-01, 2026-03-31)。
	oldEnd := retroOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: retroOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}

	// 把受理时刻推进到 3 月 15 日 10 点：旧版尚未被替代，
	// 150 分单价、数量 4 的报价落在旧版实际有效期 [03-01, 03-31) 内，确认 600 分。
	setNow(retroAcceptedAt)
	origReq := QuoteRequest{
		RequestID: "retro-original-600",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	}
	first, err := b.Quote(origReq)
	if err != nil {
		t.Fatalf("旧版有效期内首次报价是正常受理，err 应为空: %v", err)
	}
	if !first.Confirmed {
		t.Fatalf("旧版尚未被替代，报价应确认: %+v", first)
	}
	if first.UnitPrice != 150 || first.Total != 600 {
		t.Fatalf("首次报价应为单价 150、总价 600: 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	if first.Reason != ReasonNone {
		t.Fatalf("确认结果拒绝原因应为空: %q", first.Reason)
	}
	if first.Request != origReq {
		t.Fatalf("确认结果必须保留原请求内容（来源仍为旧版）: %+v vs %+v", first.Request, origReq)
	}
	if !first.AcceptedAt.Equal(retroAcceptedAt) {
		t.Fatalf("首次受理时刻应为 3 月 15 日 10 点: %v", first.AcceptedAt)
	}

	// 仍在同一个受理时刻，补登新版 seat-v2：单价 180 分，
	// 从 3 月 10 日零点起替代旧版，不填写结束时间。
	// 登记允许交接点早于登记/受理当天；该项没有其他版本占用新版的有效时间，登记应成功。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: retroHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("补登过去生效、无结束时间的替代版本应成功（无其他版本与之重叠）: %v", err)
	}

	// 版本视图：旧版登记结束仍是 3 月 31 日（永不因后续登记改变），
	// 实际结束被提前到 3 月 10 日；新旧版本的替代关系双向可见。
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("补登成功后应有两个版本, got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("版本应按生效时刻排列为 seat-v1/seat-v2: %q, %q",
			old.VersionID, nv.VersionID)
	}
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价被改写: %d", old.UnitPrice)
	}
	if old.End == nil || !old.End.Equal(retroOldEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日, got %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(retroHandoff) {
		t.Fatalf("旧版实际结束应被提前到 3 月 10 日, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版应显示被 seat-v2 替代: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}
	if nv.UnitPrice != 180 || !nv.Start.Equal(retroHandoff) {
		t.Fatalf("新版登记信息异常: %+v", nv)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("新版未填结束时间，两种结束都应为 nil: 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版应显示替代 seat-v1: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}

	// 历史报价保留首次确认结果：按原标识 Lookup 取回的仍是 3 月 15 日 10 点的 600 分，
	// 来源仍是旧版；补登不能撤销确认，也不能换用新版价格。
	looked, err := b.Lookup("retro-original-600")
	if err != nil {
		t.Fatalf("已确认的原标识应可查询: %v", err)
	}
	if looked != first {
		t.Fatalf("补登替代版本改写了历史确认记录: %+v -> %+v", first, looked)
	}
	if looked.Request != origReq || !looked.Confirmed ||
		looked.UnitPrice != 150 || looked.Total != 600 ||
		!looked.AcceptedAt.Equal(retroAcceptedAt) || looked.Reason != ReasonNone {
		t.Fatalf("Lookup 未保留首次结果（旧版/数量4/150/600/首次受理时刻/原因为空）: %+v", looked)
	}

	// 保持费率项、版本和数量不变再次报价，同样原样返回首次结果，不按当前费率重算。
	replay, err := b.Quote(origReq)
	if err != nil {
		t.Fatalf("原样重试不应返回错误: %v", err)
	}
	if replay != first {
		t.Fatalf("原样重试应返回首次结果，而非重算: %+v -> %+v", first, replay)
	}

	// 同一时刻（3 月 15 日 10 点，已在补登后的交接点之后）改用新的请求标识引用旧版：
	// 正常受理后拒绝，原因 version_expired，单价和总价为零；拒绝不是调用错误。
	expiredReq := QuoteRequest{
		RequestID: "retro-old-now-expired",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	}
	expired, err := b.Quote(expiredReq)
	if err != nil {
		t.Fatalf("引用已失效旧版是正常受理，err 应为空: %v", err)
	}
	if expired.Confirmed {
		t.Fatalf("补登后旧版实际有效期只到 3 月 10 日，3 月 15 日引用旧版必须拒绝: %+v", expired)
	}
	if expired.Reason != ReasonVersionExpired {
		t.Fatalf("拒绝原因应为 version_expired, got %q", expired.Reason)
	}
	if expired.UnitPrice != 0 || expired.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: 单价=%d 总价=%d", expired.UnitPrice, expired.Total)
	}
	if expired.Request != expiredReq {
		t.Fatalf("拒绝结果必须保留调用者指定的旧版来源: %+v vs %+v",
			expired.Request, expiredReq)
	}
	if !expired.AcceptedAt.Equal(retroAcceptedAt) {
		t.Fatalf("新报价受理时刻应为当前时钟 3 月 15 日 10 点: %v", expired.AcceptedAt)
	}

	// 用另一个新标识引用新版、数量仍为 4：按 180 分确认 720 分，来源指向新版。
	newReq := QuoteRequest{
		RequestID: "retro-new-720",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	}
	confirmed, err := b.Quote(newReq)
	if err != nil {
		t.Fatalf("引用新版是正常受理，err 应为空: %v", err)
	}
	if !confirmed.Confirmed {
		t.Fatalf("3 月 15 日新版已生效，应确认: %+v", confirmed)
	}
	if confirmed.UnitPrice != 180 || confirmed.Total != 720 {
		t.Fatalf("新版报价应为单价 180、总价 720: 单价=%d 总价=%d",
			confirmed.UnitPrice, confirmed.Total)
	}
	if confirmed.Request != newReq {
		t.Fatalf("确认结果来源应指向新版: %+v vs %+v", confirmed.Request, newReq)
	}
	if !confirmed.AcceptedAt.Equal(retroAcceptedAt) {
		t.Fatalf("新版报价受理时刻应为当前时钟 3 月 15 日 10 点: %v", confirmed.AcceptedAt)
	}

	// 新确认的 720 分不能覆盖原来的 600 分记录：两条记录各自独立可查。
	if got, err := b.Lookup("retro-original-600"); err != nil || got != first {
		t.Fatalf("新报价不能覆盖原 600 分记录: %v %+v", err, got)
	}
	if got, err := b.Lookup("retro-new-720"); err != nil || got != confirmed {
		t.Fatalf("新的 720 分记录应独立保存: %v %+v", err, got)
	}

	// 沿用原标识、只把版本改成新版：请求内容冲突，返回 ErrRequestIDConflict，
	// 不产生新的确认价，也不改写原 600 分记录。
	conflictReq := origReq
	conflictReq.VersionID = "seat-v2"
	if _, err := b.Quote(conflictReq); !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("同一标识改动版本应返回 ErrRequestIDConflict, got %v", err)
	}
	unchanged, err := b.Lookup("retro-original-600")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged != first {
		t.Fatalf("冲突调用不能改写原 600 分记录: %+v -> %+v", first, unchanged)
	}
	if unchanged.Request != origReq || !unchanged.Confirmed ||
		unchanged.UnitPrice != 150 || unchanged.Total != 600 ||
		!unchanged.AcceptedAt.Equal(retroAcceptedAt) {
		t.Fatalf("冲突后原记录应仍是旧版/数量4/150/600/首次受理时刻: %+v", unchanged)
	}
}

// TestRetroactivelyRegisteredReplacementAcceptanceOrder 单独锁定本场景的登记语义：
// 登记按版本的实际有效期判断交接是否合法，允许交接点早于当前受理时刻，
// 且不要求引用旧版的历史报价先被撤销——补登本身直接成功。
func TestRetroactivelyRegisteredReplacementAcceptanceOrder(t *testing.T) {
	now, _ := fixedClock(retroAcceptedAt)
	b := NewBook(WithClock(now))

	oldEnd := retroOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: retroOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	// 3 月 15 日 10 点先取得旧版确认。
	if out, err := b.Quote(QuoteRequest{
		RequestID: "retro-order-600", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	}); err != nil || !out.Confirmed || out.Total != 600 {
		t.Fatalf("补登前的旧版报价应确认 600 分: %v %+v", err, out)
	}

	// 时钟停在 3 月 15 日 10 点，登记一个 3 月 10 日零点就已生效的替代版本：
	// 交接点（3 月 10 日）晚于旧版开始、且登记校验看的是旧版被截断前的实际有效期，
	// 不因“当前已是 3 月 15 日、甚至已有历史确认”而拒绝。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: retroHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("交接点早于受理时刻的补登应成功: %v", err)
	}

	// 时钟从未推进，补登立即改变版本现状：同一受理时刻再引用旧版已被判定失效。
	out, err := b.Quote(QuoteRequest{
		RequestID: "retro-order-expired", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("引用旧版是正常受理，err 应为空: %v", err)
	}
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("补登后同一时刻引用旧版应 version_expired: %+v", out)
	}
	// 历史确认不受影响。
	got, err := b.Lookup("retro-order-600")
	if err != nil || !got.Confirmed || got.Total != 600 ||
		got.Request.VersionID != "seat-v1" || !got.AcceptedAt.Equal(retroAcceptedAt) {
		t.Fatalf("补登不能撤销或改写历史确认: %v %+v", err, got)
	}
}
