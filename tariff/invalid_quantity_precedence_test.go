package tariff

import (
	"fmt"
	"testing"
	"time"
)

// “数量不合法时先说明数量问题”的回归测试：数量错误与版本不可用同时出现时，
// 拒绝原因必须是 invalid_quantity，而不是版本尚未生效、已经失效或版本不存在。
//
// 时间线（均为 UTC 零点，注入时钟推进，不依赖运行当天、真实时间经过或本机时区）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00Z 起生效（未登记结束时间）
//	新版 seat-v2：单价 180 分，2026-03-10 00:00Z 起替代旧版
//	交接前受理点 iqBefore = 2026-03-05 00:00Z：旧版有效，新版尚未生效
//	交接后受理点 iqAfter  = 2026-03-15 00:00Z：旧版已被替代而失效，新版有效
//
// 约定：非空、尚未使用的请求标识下，数量为零或负数的首次报价一律以
// invalid_quantity 拒绝——即使引用的版本当时也不可用。拒绝属于正常受理：
// 调用不返回错误，单价和总价均为零，结果完整保留提交的费率项、版本和原始
// 数量（负数不改为零、不换用当时有效的另一版），受理时刻为本次首次报价的
// 时刻，且按标识可查询到同一份拒绝结果。
var (
	iqOldStart = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	iqHandoff  = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	iqBefore   = time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	iqAfter    = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
)

// newQuantityPrecedenceBook 返回一本已登记 seat-v1/seat-v2 交接关系的账本
// 及其可手动推进的时钟，时钟初始停在交接前受理点。
func newQuantityPrecedenceBook(t *testing.T) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(iqBefore)
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: iqOldStart,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: iqHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// checkInvalidQuantityRejection 断言一次首次报价以 invalid_quantity 被拒绝，
// 并验证拒绝结果的完整内容：正常受理（err 为空）、未确认、单价总价为零、
// 原样保留请求内容（含负数量）、受理时刻为本次首次报价时刻；
// 随后按标识查询必须取回同一份拒绝结果，不能查不到，也不能变成确认。
func checkInvalidQuantityRejection(t *testing.T, b *Book, req QuoteRequest, acceptedAt time.Time) Outcome {
	t.Helper()

	out, err := b.Quote(req)
	if err != nil {
		t.Fatalf("%s: 数量问题的拒绝是正常受理，err 应为空: %v", req.RequestID, err)
	}
	if out.Confirmed {
		t.Fatalf("%s: 非法数量不能确认: %+v", req.RequestID, out)
	}
	if out.Reason != ReasonInvalidQuantity {
		t.Fatalf("%s: 数量问题应优先说明，拒绝原因=%q, want %q（不能是版本问题）",
			req.RequestID, out.Reason, ReasonInvalidQuantity)
	}
	if out.UnitPrice != 0 || out.Total != 0 {
		t.Fatalf("%s: 拒绝结果单价和总价都应为零: 单价=%d 总价=%d",
			req.RequestID, out.UnitPrice, out.Total)
	}
	// 完整保留提交的费率项、版本和原始数量：负数不能改为零，
	// 版本不能换成当时有效的另一版。
	if out.Request != req {
		t.Fatalf("%s: 拒绝结果未原样保留请求内容: %+v vs %+v", req.RequestID, out.Request, req)
	}
	if !out.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("%s: 受理时刻=%v, want 首次报价时刻 %v", req.RequestID, out.AcceptedAt, acceptedAt)
	}

	// 首次拒绝被保存：按标识查询得到同一份拒绝结果。
	got, err := b.Lookup(req.RequestID)
	if err != nil {
		t.Fatalf("%s: 首次拒绝应可按标识查询: %v", req.RequestID, err)
	}
	if got != out {
		t.Fatalf("%s: 查询结果与首次拒绝不一致: %+v vs %+v", req.RequestID, got, out)
	}
	if got.Confirmed {
		t.Fatalf("%s: 首次结果为拒绝，查询不能出现确认金额: %+v", req.RequestID, got)
	}
	return out
}

// checkValidQuantityControl 断言同一版本状态下合法正数量的对照结果：
// 版本问题此时才成为拒绝原因（或版本有效时正常确认），证明账本能区分
// 数量输入错误与版本无法使用，而不是所有请求都固定返回数量错误。
func checkValidQuantityControl(t *testing.T, b *Book, req QuoteRequest, acceptedAt time.Time, wantReason RejectReason) {
	t.Helper()

	out, err := b.Quote(req)
	if err != nil {
		t.Fatalf("%s: 对照报价 err 应为空: %v", req.RequestID, err)
	}
	if out.Confirmed {
		t.Fatalf("%s: 版本不可用时合法数量也不能确认: %+v", req.RequestID, out)
	}
	if out.Reason != wantReason {
		t.Fatalf("%s: 合法数量的拒绝原因=%q, want %q", req.RequestID, out.Reason, wantReason)
	}
	if out.Reason == ReasonInvalidQuantity {
		t.Fatalf("%s: 数量合法时不应返回数量错误", req.RequestID)
	}
	if !out.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("%s: 受理时刻=%v, want %v", req.RequestID, out.AcceptedAt, acceptedAt)
	}
}

// checkConfirmed 断言版本有效时数量 4 的报价按所引用版本的单价确认。
func checkConfirmed(t *testing.T, b *Book, req QuoteRequest, acceptedAt time.Time, wantPrice, wantTotal int64) {
	t.Helper()

	out, err := b.Quote(req)
	if err != nil {
		t.Fatalf("%s: 确认报价 err 应为空: %v", req.RequestID, err)
	}
	if !out.Confirmed || out.Reason != ReasonNone {
		t.Fatalf("%s: 版本有效、数量合法，应确认: %+v", req.RequestID, out)
	}
	if out.UnitPrice != wantPrice || out.Total != wantTotal {
		t.Fatalf("%s: 单价/总价=%d/%d, want %d/%d（单价须来自所引用版本）",
			req.RequestID, out.UnitPrice, out.Total, wantPrice, wantTotal)
	}
	if !out.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("%s: 受理时刻=%v, want %v", req.RequestID, out.AcceptedAt, acceptedAt)
	}
}

// TestInvalidQuantityPrecedesVersionUnavailability 覆盖数量错误与各类版本
// 不可用同时出现时的拒绝原因，并以合法正数量形成对照。整个场景用全新账本
// 重复执行两遍，验证结论确定、可重复，不依赖运行当天或本机时区。
func TestInvalidQuantityPrecedesVersionUnavailability(t *testing.T) {
	for run := 0; run < 2; run++ {
		t.Run(fmt.Sprintf("第%d遍", run+1), func(t *testing.T) {
			b, setNow := newQuantityPrecedenceBook(t)
			id := func(name string) string { return fmt.Sprintf("iq-%d-%s", run, name) }

			// ---- 交接前（2026-03-05 00:00Z）：旧版有效，新版尚未生效 ----

			// 数量为零或负，同时引用尚未生效的新版：先说明数量问题。
			for _, qty := range []int64{0, -3} {
				checkInvalidQuantityRejection(t, b, QuoteRequest{
					RequestID: id(fmt.Sprintf("new-not-yet-effective-qty%d", qty)),
					ItemID:    "seat", VersionID: "seat-v2", Quantity: qty,
				}, iqBefore)
			}
			// 数量为零或负，同时引用未登记的版本：同样按数量问题拒绝。
			for _, qty := range []int64{0, -7} {
				checkInvalidQuantityRejection(t, b, QuoteRequest{
					RequestID: id(fmt.Sprintf("unregistered-qty%d", qty)),
					ItemID:    "seat", VersionID: "seat-v9", Quantity: qty,
				}, iqBefore)
			}
			// 数量为零或负，费率项根本不存在：相同结果。
			for _, qty := range []int64{0, -1} {
				checkInvalidQuantityRejection(t, b, QuoteRequest{
					RequestID: id(fmt.Sprintf("missing-item-qty%d", qty)),
					ItemID:    "nope", VersionID: "seat-v1", Quantity: qty,
				}, iqBefore)
			}

			// 对照：同一时刻、同一版本状态，合法正数量才暴露版本问题。
			checkValidQuantityControl(t, b, QuoteRequest{
				RequestID: id("ctrl-not-yet-effective"),
				ItemID:    "seat", VersionID: "seat-v2", Quantity: 4,
			}, iqBefore, ReasonVersionNotYetEffective)
			checkValidQuantityControl(t, b, QuoteRequest{
				RequestID: id("ctrl-unregistered"),
				ItemID:    "seat", VersionID: "seat-v9", Quantity: 4,
			}, iqBefore, ReasonVersionNotFound)
			checkValidQuantityControl(t, b, QuoteRequest{
				RequestID: id("ctrl-missing-item"),
				ItemID:    "nope", VersionID: "seat-v1", Quantity: 4,
			}, iqBefore, ReasonVersionNotFound)

			// 版本实际有效：数量 4 引用旧版，按旧版单价 150 分确认 600 分。
			checkConfirmed(t, b, QuoteRequest{
				RequestID: id("confirm-old"),
				ItemID:    "seat", VersionID: "seat-v1", Quantity: 4,
			}, iqBefore, 150, 600)

			// ---- 交接后（2026-03-15 00:00Z）：旧版已被替代而失效，新版有效 ----
			setNow(iqAfter)

			// 数量为零或负，同时引用已经失效的旧版：仍是数量问题，
			// 不能改报版本已经失效。
			for _, qty := range []int64{0, -5} {
				checkInvalidQuantityRejection(t, b, QuoteRequest{
					RequestID: id(fmt.Sprintf("old-expired-qty%d", qty)),
					ItemID:    "seat", VersionID: "seat-v1", Quantity: qty,
				}, iqAfter)
			}

			// 对照：合法正数量引用旧版，拒绝原因是 version_expired。
			checkValidQuantityControl(t, b, QuoteRequest{
				RequestID: id("ctrl-expired"),
				ItemID:    "seat", VersionID: "seat-v1", Quantity: 4,
			}, iqAfter, ReasonVersionExpired)

			// 版本实际有效：数量 4 引用新版，按新版单价 180 分确认 720 分。
			checkConfirmed(t, b, QuoteRequest{
				RequestID: id("confirm-new"),
				ItemID:    "seat", VersionID: "seat-v2", Quantity: 4,
			}, iqAfter, 180, 720)

			// 交接前留下的数量问题拒绝不受时间推进影响：
			// 按标识查询仍是最初那份 invalid_quantity 拒绝。
			got, err := b.Lookup(id("new-not-yet-effective-qty-3"))
			if err != nil {
				t.Fatalf("交接后查询交接前的数量问题拒绝: %v", err)
			}
			if got.Confirmed || got.Reason != ReasonInvalidQuantity {
				t.Fatalf("交接后查询到的仍是数量问题拒绝: %+v", got)
			}
			if got.Request.Quantity != -3 {
				t.Fatalf("负数量必须原样保留，不能改为零: %+v", got.Request)
			}
			if !got.AcceptedAt.Equal(iqBefore) {
				t.Fatalf("受理时刻仍是首次报价时刻: %v", got.AcceptedAt)
			}
		})
	}
}
