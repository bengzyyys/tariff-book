package tariff

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// 版本可用性优先于金额溢出的回归测试。
//
// 保护的规则：用户首次引用某个费率版本时，即使 单价×数量 在数学上超出
// 有符号 64 位整数上限，只要该版本在受理时刻尚未生效或已经失效，结果就必须
// 说明版本不可用（version_not_yet_effective / version_expired），
// 不能先报 total_overflow。只有引用的版本当时有效，同样的超大金额请求
// 才应因 total_overflow 被拒绝。
//
// 固定时间线（均为 UTC，结束时刻不含）：
//
//	旧版 seat-v1：2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00
//	新版 seat-v2：2026-03-10 00:00 起替代旧版，持续有效
//
// 两版单价均为 math.MaxInt64 分（有符号 64 位整数最大值），数量为 2，
// 数学总价 2×MaxInt64 超出该类型上限——任何确认路径都无法表示该金额。
var (
	overflowOldStart = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	overflowOldEnd   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	overflowHandoff  = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
)

const overflowQuantity = int64(2)

// newOverflowHandoffBook 返回一本已按上述时间线登记交接关系的账本及其时钟。
func newOverflowHandoffBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: math.MaxInt64,
		Start: overflowOldStart,
		End:   &overflowOldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: math.MaxInt64,
		Start:    overflowHandoff,
		Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// 交接点两侧的首次报价：版本不可用时原因必须是版本问题而非 total_overflow，
// 版本有效时同一超大金额请求才因 total_overflow 被拒绝。
// 开始时刻包含在有效期内，实际结束时刻（交接点）不包含在内。
func TestOverflowRejectionRequiresEffectiveVersion(t *testing.T) {
	b, setNow := newOverflowHandoffBook(t, overflowOldStart)

	cases := []struct {
		name      string
		instant   time.Time
		versionID string
		reason    RejectReason
	}{
		{"旧版生效起点本身-旧版有效-溢出", overflowOldStart, "seat-v1", ReasonTotalOverflow},
		{"交接前-旧版有效-溢出", overflowHandoff.Add(-time.Nanosecond), "seat-v1", ReasonTotalOverflow},
		{"交接前-新版尚未生效-不能报溢出", overflowHandoff.Add(-time.Nanosecond), "seat-v2", ReasonVersionNotYetEffective},
		{"交接点本身-旧版已按实际结束失效-不能报溢出", overflowHandoff, "seat-v1", ReasonVersionExpired},
		{"交接点本身-新版生效起点包含在内-溢出", overflowHandoff, "seat-v2", ReasonTotalOverflow},
		{"交接后-旧版仍失效-不能报溢出", overflowHandoff.Add(time.Nanosecond), "seat-v1", ReasonVersionExpired},
		{"交接后-新版有效-溢出", overflowHandoff.Add(24 * time.Hour), "seat-v2", ReasonTotalOverflow},
	}
	for i, c := range cases {
		setNow(c.instant)
		req := QuoteRequest{
			RequestID: fmt.Sprintf("overflow-%02d", i),
			ItemID:    "seat",
			VersionID: c.versionID,
			Quantity:  overflowQuantity,
		}
		out, err := b.Quote(req)
		// 无论哪种拒绝都属于报价被正常受理后的结果：调用本身不返回错误。
		if err != nil {
			t.Fatalf("%s: 受理后的拒绝不应返回 error: %v", c.name, err)
		}
		if out.Confirmed {
			t.Fatalf("%s: 总价不可表示，不能确认: %+v", c.name, out)
		}
		if out.Reason != c.reason {
			t.Fatalf("%s: 拒绝原因=%q, want %q", c.name, out.Reason, c.reason)
		}
		// 拒绝结果单价和总价均为零：不能出现溢出后的负数或截断金额。
		if out.UnitPrice != 0 || out.Total != 0 {
			t.Fatalf("%s: 拒绝结果金额应为零: 单价=%d 总价=%d", c.name, out.UnitPrice, out.Total)
		}
		// 保留用户提交的费率项、版本、数量及首次受理时刻。
		if out.Request != req {
			t.Fatalf("%s: 请求来源未保留: %+v vs %+v", c.name, out.Request, req)
		}
		if !out.AcceptedAt.Equal(c.instant) {
			t.Fatalf("%s: 受理时刻=%v, want %v", c.name, out.AcceptedAt, c.instant)
		}

		// 按请求标识查询这笔结果，应看到相同的拒绝原因与来源，
		// 从而区分“版本不能使用”和“版本有效但金额不能表示”。
		got, err := b.Lookup(req.RequestID)
		if err != nil {
			t.Fatalf("%s: 查询首次结果: %v", c.name, err)
		}
		if got != out {
			t.Fatalf("%s: 查询结果与首次受理不一致: %+v vs %+v", c.name, got, out)
		}
		if got.Reason != c.reason {
			t.Fatalf("%s: 查询到的拒绝原因=%q, want %q", c.name, got.Reason, c.reason)
		}
	}
}

// 旧版登记结束在月底，但交接点起必须按被替代后的实际结束时间拒绝：
// 即使登记结束尚未到达，引用旧版也得到 version_expired 而非 total_overflow。
func TestOverflowOldVersionRejectedByEffectiveEndNotRegisteredEnd(t *testing.T) {
	b, setNow := newOverflowHandoffBook(t, overflowOldStart)

	// 测试前提：旧版登记结束仍在交接点之后，拒绝只能来自实际有效结束。
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	var old VersionView
	for _, v := range views {
		if v.VersionID == "seat-v1" {
			old = v
		}
	}
	if old.End == nil || !old.End.Equal(overflowOldEnd) {
		t.Fatalf("旧版登记结束应仍为月底: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(overflowHandoff) {
		t.Fatalf("旧版实际有效结束应被截断到交接点: %v", old.EffectiveEnd)
	}

	// 交接点之后、登记结束（3 月 31 日）之前：按登记结束看似仍在有效期内，
	// 但必须按实际结束时间拒绝。
	setNow(time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC))
	req := QuoteRequest{
		RequestID: "overflow-old-after-handoff",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  overflowQuantity,
	}
	out, err := b.Quote(req)
	if err != nil {
		t.Fatalf("受理后的拒绝不应返回 error: %v", err)
	}
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("登记结束未到达也必须按实际结束拒绝: %+v", out)
	}
	if out.UnitPrice != 0 || out.Total != 0 {
		t.Fatalf("拒绝结果金额应为零: 单价=%d 总价=%d", out.UnitPrice, out.Total)
	}
}

// 指定版本不可用时不能自动改用同项另一个版本报价：
// 拒绝结果的来源必须是用户指定的版本，金额为零。
func TestOverflowNoAutoSwitchToEffectiveVersion(t *testing.T) {
	b, setNow := newOverflowHandoffBook(t, overflowOldStart)

	cases := []struct {
		name      string
		instant   time.Time
		versionID string
		reason    RejectReason
	}{
		// 交接前引用尚未生效的新版：不能改用当时有效的旧版。
		{"交接前引用新版不得改用旧版", overflowHandoff.Add(-time.Hour), "seat-v2", ReasonVersionNotYetEffective},
		// 交接后引用已失效的旧版：不能改用当时有效的新版。
		{"交接后引用旧版不得改用新版", overflowHandoff.Add(time.Hour), "seat-v1", ReasonVersionExpired},
	}
	for i, c := range cases {
		setNow(c.instant)
		req := QuoteRequest{
			RequestID: fmt.Sprintf("no-switch-overflow-%d", i),
			ItemID:    "seat",
			VersionID: c.versionID,
			Quantity:  overflowQuantity,
		}
		out, err := b.Quote(req)
		if err != nil {
			t.Fatalf("%s: 受理后的拒绝不应返回 error: %v", c.name, err)
		}
		if out.Confirmed {
			t.Fatalf("%s: 不能换用其他版本确认: %+v", c.name, out)
		}
		if out.Reason != c.reason {
			t.Fatalf("%s: 拒绝原因=%q, want %q", c.name, out.Reason, c.reason)
		}
		if out.Request.VersionID != c.versionID {
			t.Fatalf("%s: 来源必须保留指定版本 %q: %+v", c.name, c.versionID, out.Request)
		}
		if out.UnitPrice != 0 || out.Total != 0 {
			t.Fatalf("%s: 拒绝结果金额应为零: 单价=%d 总价=%d", c.name, out.UnitPrice, out.Total)
		}
	}
}
