package tariff

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// 本文件保护一条直接影响核对结论的判定顺序：
//
//	版本在受理时刻是否可用，必须先于“单价乘数量是否超出 int64 上限”判断。
//
// 即用户首次引用某个费率版本时，即使数量乘单价在数学上已经溢出，只要该版本
// 尚未生效或已经失效，结果都必须说明版本不可用（version_not_yet_effective /
// version_expired），不能先报 total_overflow；只有引用的版本在受理时刻有效时，
// 同样的超大金额请求才应因 total_overflow 被拒绝。
//
// 围绕同一费率项的一次替代交接构造场景（时间均为 UTC，开始时刻含、结束时刻不含）：
//
//	旧版本 seat-v1：单价 math.MaxInt64（9223372036854775807）分，
//	               2026-03-01 00:00 生效，登记结束 2026-03-31 00:00
//	新版本 seat-v2：单价同为 math.MaxInt64，2026-03-10 00:00 起替代 seat-v1，持续有效
//
// 两版单价都取有符号 64 位整数最大值，数量固定为合法正整数 2：
// 数学总价为 2*MaxInt64，超出 int64 表示范围（若直接相乘会回绕成 -2）。
// 因此“有效版本 + 该数量”必然走到 total_overflow，从而能干净地检验：
// 版本不可用时，原因绝不能被 total_overflow 顶替。
var (
	ovfItem          = "seat"
	ovfOldVersion    = "seat-v1"
	ovfNewVersion    = "seat-v2"
	ovfUnitPrice     = int64(math.MaxInt64)
	ovfQuantity      = int64(2)
	ovfOldStart      = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ovfRegisteredEnd = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 旧版登记结束（不含）
	ovfHandoff       = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 新版生效、旧版实际结束（不含）
	ovfBeforeHandoff = time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
)

// newOverflowHandoffBook 返回一本已登记 seat-v1 / seat-v2 替代关系的账本，
// 两版单价均为 math.MaxInt64，并返回可手动推进的受理时钟。
func newOverflowHandoffBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	oldEnd := ovfRegisteredEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: ovfItem, VersionID: ovfOldVersion, UnitPrice: ovfUnitPrice,
		Start: ovfOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register %s: %v", ovfOldVersion, err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: ovfItem, VersionID: ovfNewVersion, UnitPrice: ovfUnitPrice,
		Start: ovfHandoff, Replaces: ovfOldVersion,
	}); err != nil {
		t.Fatalf("register %s: %v", ovfNewVersion, err)
	}
	return b, setNow
}

// assertRejectedWithoutOverflow 校验一次“受理后的拒绝”：
// 调用本身不返回错误、结果未确认、原因为 want、单价与总价均为零
// （绝不能留下溢出回绕后的负数或截断金额）、请求来源与数量原样保留、
// 受理时刻取自本次首次接收的时刻。拒绝结果仍指向用户指定的版本，
// 账本不会因为该版本不可用就自动改用同项另一个版本。
func assertRejectedWithoutOverflow(t *testing.T, out Outcome, err error, req QuoteRequest, acceptedAt time.Time, want RejectReason) {
	t.Helper()
	if err != nil {
		t.Fatalf("引用不可用版本或金额溢出都是正常受理，err 应为空: %v", err)
	}
	if out.Confirmed {
		t.Fatalf("结果必须未确认: %+v", out)
	}
	if out.Reason != want {
		t.Fatalf("拒绝原因=%q, want %q（版本可用性必须先于金额溢出判断）: %+v",
			out.Reason, want, out)
	}
	if out.UnitPrice != 0 || out.Total != 0 {
		t.Fatalf("拒绝结果单价/总价必须为零，不能留下计算金额: 单价=%d 总价=%d",
			out.UnitPrice, out.Total)
	}
	// MaxInt64*2 在 int64 中会回绕成 -2；显式拦住“先相乘再拒绝”的错误实现。
	if wrapped := ovfUnitPrice * ovfQuantity; wrapped != -2 {
		t.Fatalf("测试前提不成立：MaxInt64*2 应回绕为 -2，got %d", wrapped)
	}
	if out.Total == -2 {
		t.Fatalf("总价不能是溢出回绕后的负数 -2: %+v", out)
	}
	if out.Request != req {
		t.Fatalf("拒绝结果必须原样保留费率项、版本与数量，不能换版: %+v vs %+v",
			out.Request, req)
	}
	if !out.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("首次受理时刻=%v, want %v", out.AcceptedAt, acceptedAt)
	}
}

// TestVersionAvailabilityCheckedBeforeTotalOverflow 是核心回归：
// 交接点两侧、四个受理时刻分别引用旧版与新版，版本不可用的原因
// （未生效 / 已失效）绝不能被 total_overflow 替代。
func TestVersionAvailabilityCheckedBeforeTotalOverflow(t *testing.T) {
	b, setNow := newOverflowHandoffBook(t, ovfBeforeHandoff)

	cases := []struct {
		name      string
		instant   time.Time
		versionID string
		want      RejectReason
	}{
		// 交接前：旧版有效（开始含、实际结束未到），同样的超大金额只能报 total_overflow。
		{"交接前-旧版有效-金额溢出", ovfBeforeHandoff, ovfOldVersion, ReasonTotalOverflow},
		// 交接前：新版 start 未到，必须报尚未生效，不能先报金额溢出。
		{"交接前-新版未生效-不得报溢出", ovfBeforeHandoff, ovfNewVersion, ReasonVersionNotYetEffective},
		// 交接点前 1 纳秒：半开区间边界，结论与交接前一致。
		{"交接点前1纳秒-旧版有效-金额溢出", ovfHandoff.Add(-time.Nanosecond), ovfOldVersion, ReasonTotalOverflow},
		{"交接点前1纳秒-新版未生效-不得报溢出", ovfHandoff.Add(-time.Nanosecond), ovfNewVersion, ReasonVersionNotYetEffective},
		// 交接点本身：旧版实际有效结束不含该点 → 已失效；新版起点含在有效期内 → 金额溢出。
		{"交接点-旧版已失效-不得报溢出", ovfHandoff, ovfOldVersion, ReasonVersionExpired},
		{"交接点-新版生效-金额溢出", ovfHandoff, ovfNewVersion, ReasonTotalOverflow},
		// 交接点后 1 纳秒：与交接点同侧。
		{"交接点后1纳秒-旧版已失效-不得报溢出", ovfHandoff.Add(time.Nanosecond), ovfOldVersion, ReasonVersionExpired},
		{"交接点后1纳秒-新版有效-金额溢出", ovfHandoff.Add(time.Nanosecond), ovfNewVersion, ReasonTotalOverflow},
	}
	for i, c := range cases {
		setNow(c.instant)
		req := QuoteRequest{
			RequestID: fmt.Sprintf("ovf-matrix-%02d", i),
			ItemID:    ovfItem,
			VersionID: c.versionID,
			Quantity:  ovfQuantity,
		}
		out, err := b.Quote(req)
		assertRejectedWithoutOverflow(t, out, err, req, c.instant, c.want)

		// 非空标识的首次拒绝已保存：按标识查询得到相同的拒绝原因、来源与首次受理时刻，
		// 不会因为时钟已经推进而按当前费率或当前版本状态重算。
		got, lookupErr := b.Lookup(req.RequestID)
		if lookupErr != nil {
			t.Fatalf("%s: 首次拒绝应可按标识查询: %v", c.name, lookupErr)
		}
		if got != out {
			t.Fatalf("%s: 查询结果与首次拒绝不一致: %+v vs %+v", c.name, got, out)
		}
	}
}

// TestExpiredJudgedByEffectiveEndNotRegisteredEnd 锁定替代交接对旧版有效期的截断：
// 旧版登记结束仍在 3 月 31 日，但实际有效结束已被截到 3 月 10 日交接点；
// 交接时刻引用旧版必须得到 version_expired，而不能沿用登记的月底结束，
// 也不能被金额溢出抢先。
func TestExpiredJudgedByEffectiveEndNotRegisteredEnd(t *testing.T) {
	b, setNow := newOverflowHandoffBook(t, ovfHandoff)

	views, err := b.ItemVersions(ovfItem)
	if err != nil {
		t.Fatal(err)
	}
	var oldView, newView VersionView
	for _, v := range views {
		switch v.VersionID {
		case ovfOldVersion:
			oldView = v
		case ovfNewVersion:
			newView = v
		}
	}
	// 旧版：登记结束仍是月底，实际有效结束显示交接点及替代来源。
	if oldView.End == nil || !oldView.End.Equal(ovfRegisteredEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日: %v", oldView.End)
	}
	if oldView.EffectiveEnd == nil || !oldView.EffectiveEnd.Equal(ovfHandoff) {
		t.Fatalf("旧版实际有效结束应被截断到交接点 3 月 10 日: %v", oldView.EffectiveEnd)
	}
	if oldView.SupersededBy != ovfNewVersion {
		t.Fatalf("旧版应记录由 %s 替代: %q", ovfNewVersion, oldView.SupersededBy)
	}
	// 新版：自交接点起持续有效并保留替代来源。
	if newView.End != nil || newView.EffectiveEnd != nil {
		t.Fatalf("新版应持续有效: %+v", newView)
	}
	if newView.Replaces != ovfOldVersion {
		t.Fatalf("新版必须保留替代来源: %q", newView.Replaces)
	}

	// 交接时刻（实际结束时刻，不含在有效期内）引用旧版：
	// 即使登记结束还在月底、即使金额数学上溢出，也只能是 version_expired。
	setNow(ovfHandoff)
	req := QuoteRequest{
		RequestID: "ovf-old-at-handoff",
		ItemID:    ovfItem,
		VersionID: ovfOldVersion,
		Quantity:  ovfQuantity,
	}
	out, err := b.Quote(req)
	assertRejectedWithoutOverflow(t, out, err, req, ovfHandoff, ReasonVersionExpired)
}

// TestUnavailableVersionAndOverflowRejectionsRemainDistinguishable 保证两类首次拒绝
// 都被保存且可通过请求标识稳定区分：版本不可用（未生效 / 已失效）与版本有效但
// 金额不可表示（total_overflow）的原因、来源、首次受理时刻各自原样可查。
func TestUnavailableVersionAndOverflowRejectionsRemainDistinguishable(t *testing.T) {
	b, setNow := newOverflowHandoffBook(t, ovfBeforeHandoff)

	// 交接前：新版尚未生效 → version_not_yet_effective。
	setNow(ovfBeforeHandoff)
	earlyNewReq := QuoteRequest{
		RequestID: "ovf-early-new", ItemID: ovfItem,
		VersionID: ovfNewVersion, Quantity: ovfQuantity,
	}
	earlyNew, err := b.Quote(earlyNewReq)
	assertRejectedWithoutOverflow(t, earlyNew, err, earlyNewReq, ovfBeforeHandoff, ReasonVersionNotYetEffective)

	// 交接前：旧版有效 → total_overflow。
	earlyOldReq := QuoteRequest{
		RequestID: "ovf-early-old", ItemID: ovfItem,
		VersionID: ovfOldVersion, Quantity: ovfQuantity,
	}
	earlyOld, err := b.Quote(earlyOldReq)
	assertRejectedWithoutOverflow(t, earlyOld, err, earlyOldReq, ovfBeforeHandoff, ReasonTotalOverflow)

	// 推进到交接点之后：旧版已失效、新版已生效。
	setNow(ovfHandoff)
	atHandoffOldReq := QuoteRequest{
		RequestID: "ovf-handoff-old", ItemID: ovfItem,
		VersionID: ovfOldVersion, Quantity: ovfQuantity,
	}
	atHandoffOld, err := b.Quote(atHandoffOldReq)
	assertRejectedWithoutOverflow(t, atHandoffOld, err, atHandoffOldReq, ovfHandoff, ReasonVersionExpired)

	atHandoffNewReq := QuoteRequest{
		RequestID: "ovf-handoff-new", ItemID: ovfItem,
		VersionID: ovfNewVersion, Quantity: ovfQuantity,
	}
	atHandoffNew, err := b.Quote(atHandoffNewReq)
	assertRejectedWithoutOverflow(t, atHandoffNew, err, atHandoffNewReq, ovfHandoff, ReasonTotalOverflow)

	saved := []struct {
		name string
		req  QuoteRequest
		want Outcome
	}{
		{"交接前新版未生效", earlyNewReq, earlyNew},
		{"交接前旧版金额溢出", earlyOldReq, earlyOld},
		{"交接点旧版已失效", atHandoffOldReq, atHandoffOld},
		{"交接点新版金额溢出", atHandoffNewReq, atHandoffNew},
	}
	for _, c := range saved {
		// Lookup 取回完全相同的首次结果。
		got, err := b.Lookup(c.req.RequestID)
		if err != nil {
			t.Fatalf("%s: 查询首次拒绝: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s: 查询结果与首次拒绝不一致: %+v vs %+v", c.name, got, c.want)
		}
		// 时钟继续推进后原样重试，仍返回那次首次拒绝，不按当前状态重算。
		setNow(ovfHandoff.Add(24 * time.Hour))
		replay, err := b.Quote(c.req)
		if err != nil {
			t.Fatalf("%s: 原样重试: %v", c.name, err)
		}
		if replay != c.want {
			t.Fatalf("%s: 重试未原样返回首次拒绝: %+v vs %+v", c.name, replay, c.want)
		}
		if !replay.AcceptedAt.Equal(c.want.AcceptedAt) {
			t.Fatalf("%s: 重试改写了首次受理时刻: %v vs %v",
				c.name, replay.AcceptedAt, c.want.AcceptedAt)
		}
	}

	// 同一版本（新版）在交接前是“不可用”、交接点起是“金额不可表示”：
	// 两份记录原因不同、各自可查，不能互相覆盖或混淆。
	if earlyNew.Reason == atHandoffNew.Reason {
		t.Fatalf("新版交接前后的拒绝原因必须可区分，均为 %q", earlyNew.Reason)
	}
	if earlyNew.Reason != ReasonVersionNotYetEffective || atHandoffNew.Reason != ReasonTotalOverflow {
		t.Fatalf("新版原因异常: 交接前=%q 交接点=%q", earlyNew.Reason, atHandoffNew.Reason)
	}
	if earlyNew.Request.VersionID != ovfNewVersion || atHandoffNew.Request.VersionID != ovfNewVersion {
		t.Fatalf("两份新版记录都必须保留新版来源: %q / %q",
			earlyNew.Request.VersionID, atHandoffNew.Request.VersionID)
	}
	// 旧版同理：交接前金额溢出、交接点已失效，原因随受理时刻不同而不同。
	if earlyOld.Reason != ReasonTotalOverflow || atHandoffOld.Reason != ReasonVersionExpired {
		t.Fatalf("旧版原因异常: 交接前=%q 交接点=%q", earlyOld.Reason, atHandoffOld.Reason)
	}
	// 所有拒绝记录都保留数量 2 且金额为零——数量是合法正整数，从未走到确认金额。
	for _, c := range saved {
		o := c.want
		if o.Request.Quantity != ovfQuantity {
			t.Fatalf("%s: 数量必须原样保留为 %d, got %d", c.name, ovfQuantity, o.Request.Quantity)
		}
		if o.UnitPrice != 0 || o.Total != 0 {
			t.Fatalf("%s: 拒绝记录金额必须为零: 单价=%d 总价=%d", c.name, o.UnitPrice, o.Total)
		}
	}
}
