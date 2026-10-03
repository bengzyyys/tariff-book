package tariff

import (
	"fmt"
	"testing"
	"time"
)

// 交接场景的固定时间线（费率时间以东八区表示）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00 (+08:00) 起生效，登记结束 2026-03-31 00:00 (+08:00)
//	新版 seat-v2：单价 180 分，2026-03-10 00:00 (+08:00) 起替代旧版，无结束时间
//
// 东八区 2026-03-10 00:00 即 UTC 2026-03-09 16:00，是同一实际时刻。
var (
	east8           = time.FixedZone("UTC+8", 8*3600)
	handoffOldEnd   = time.Date(2026, 3, 31, 0, 0, 0, 0, east8)
	handoffPoint    = time.Date(2026, 3, 10, 0, 0, 0, 0, east8)
	handoffPointUTC = time.Date(2026, 3, 9, 16, 0, 0, 0, time.UTC)
)

// newHandoffBook 返回一本已登记 seat-v1/seat-v2 交接关系的账本及其时钟。
func newHandoffBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	if !handoffPoint.Equal(handoffPointUTC) {
		t.Fatal("测试前提不成立：东八区 3 月 10 日零点应等于 UTC 3 月 9 日 16 点")
	}
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, east8),
		End:   &handoffOldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start:    handoffPoint,
		Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// 版本视图：旧版登记结束仍显示月底，实际有效结束显示交接点；新版保留替代来源。
func TestHandoffVersionView(t *testing.T) {
	b, _ := newHandoffBook(t, time.Date(2026, 3, 1, 0, 0, 0, 0, east8))

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("want 2 versions, got %d", len(views))
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("unexpected order: %v, %v", old.VersionID, nv.VersionID)
	}

	// 旧版：登记结束仍是 3 月 31 日零点，实际有效结束被截断到交接点。
	if old.End == nil || !old.End.Equal(handoffOldEnd) {
		t.Fatalf("registered end must stay at month end: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(handoffPoint) {
		t.Fatalf("effective end must be the handoff point: %v", old.EffectiveEnd)
	}
	if old.SupersededBy != "seat-v2" {
		t.Fatalf("old superseded-by: %q", old.SupersededBy)
	}

	// 新版：保留替代旧版的来源，未设结束时间。
	if nv.Replaces != "seat-v1" {
		t.Fatalf("new version must keep replacement source: %q", nv.Replaces)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("new version should be open-ended: %+v", nv)
	}
	if nv.UnitPrice != 180 {
		t.Fatalf("new unit price: %d", nv.UnitPrice)
	}
}

// 交接点前后一纳秒及交接点本身的报价边界。
func TestHandoffBoundaryQuotes(t *testing.T) {
	b, setNow := newHandoffBook(t, handoffPoint.Add(-time.Nanosecond))

	cases := []struct {
		name      string
		instant   time.Time
		versionID string
		confirmed bool
		unitPrice int64
		total     int64
		reason    RejectReason
	}{
		{"交接点前1纳秒-旧版确认", handoffPoint.Add(-time.Nanosecond), "seat-v1", true, 150, 600, ReasonNone},
		{"交接点前1纳秒-新版尚未生效", handoffPoint.Add(-time.Nanosecond), "seat-v2", false, 0, 0, ReasonVersionNotYetEffective},
		{"交接点本身-旧版已失效", handoffPoint, "seat-v1", false, 0, 0, ReasonVersionExpired},
		{"交接点本身-新版确认", handoffPoint, "seat-v2", true, 180, 720, ReasonNone},
		{"交接点后1纳秒-旧版已失效", handoffPoint.Add(time.Nanosecond), "seat-v1", false, 0, 0, ReasonVersionExpired},
		{"交接点后1纳秒-新版确认", handoffPoint.Add(time.Nanosecond), "seat-v2", true, 180, 720, ReasonNone},
	}
	for i, c := range cases {
		setNow(c.instant)
		req := QuoteRequest{
			RequestID: fmt.Sprintf("handoff-%02d", i),
			ItemID:    "seat",
			VersionID: c.versionID,
			Quantity:  4,
		}
		out, err := b.Quote(req)
		if err != nil {
			t.Fatalf("%s: 拒绝/确认都是正常受理，err 应为空，got %v", c.name, err)
		}
		if out.Confirmed != c.confirmed {
			t.Fatalf("%s: Confirmed=%v, want %v (%+v)", c.name, out.Confirmed, c.confirmed, out)
		}
		if out.UnitPrice != c.unitPrice || out.Total != c.total {
			t.Fatalf("%s: 单价/总价=%d/%d, want %d/%d", c.name, out.UnitPrice, out.Total, c.unitPrice, c.total)
		}
		if out.Reason != c.reason {
			t.Fatalf("%s: 原因=%q, want %q", c.name, out.Reason, c.reason)
		}
		// 结果必须保留请求中的费率项、版本和数量，并记录实际受理时刻。
		if out.Request != req {
			t.Fatalf("%s: 请求来源未保留: %+v vs %+v", c.name, out.Request, req)
		}
		if !out.AcceptedAt.Equal(c.instant) {
			t.Fatalf("%s: 受理时刻=%v, want %v", c.name, out.AcceptedAt, c.instant)
		}
	}
}

// 同一个实际时刻换成另一种时区表示，确认状态、金额和拒绝原因都应一致。
func TestHandoffSameInstantAnyTimezone(t *testing.T) {
	b, setNow := newHandoffBook(t, handoffPointUTC)

	type want struct {
		confirmed bool
		unitPrice int64
		total     int64
		reason    RejectReason
	}
	instants := []struct {
		name    string
		instant time.Time
		oldV    want
		newV    want
	}{
		{
			"交接点前1纳秒",
			handoffPoint.Add(-time.Nanosecond),
			want{true, 150, 600, ReasonNone},
			want{false, 0, 0, ReasonVersionNotYetEffective},
		},
		{
			"交接点本身",
			handoffPoint,
			want{false, 0, 0, ReasonVersionExpired},
			want{true, 180, 720, ReasonNone},
		},
		{
			"交接点后1纳秒",
			handoffPoint.Add(time.Nanosecond),
			want{false, 0, 0, ReasonVersionExpired},
			want{true, 180, 720, ReasonNone},
		},
	}
	// 同一实际时刻的三种表示：东八区、UTC、零时区偏移的另一命名时区。
	repr := func(t time.Time) []time.Time {
		return []time.Time{
			t,
			t.UTC(),
			t.In(time.FixedZone("UTC+0", 0)),
		}
	}
	for _, c := range instants {
		reprs := repr(c.instant)
		for i := 1; i < len(reprs); i++ {
			// 测试前提：表示不同时刻相同
			if !reprs[0].Equal(reprs[i]) {
				t.Fatalf("%s: 测试前提不成立，表示不代表同一时刻", c.name)
			}
		}
		var firstOld, firstNew Outcome
		for r, instant := range reprs {
			setNow(instant)
			outOld, err := b.Quote(QuoteRequest{
				RequestID: fmt.Sprintf("tz-%s-old-%d", c.name, r),
				ItemID:    "seat", VersionID: "seat-v1", Quantity: 4,
			})
			if err != nil {
				t.Fatalf("%s 表示%d 旧版: %v", c.name, r, err)
			}
			outNew, err := b.Quote(QuoteRequest{
				RequestID: fmt.Sprintf("tz-%s-new-%d", c.name, r),
				ItemID:    "seat", VersionID: "seat-v2", Quantity: 4,
			})
			if err != nil {
				t.Fatalf("%s 表示%d 新版: %v", c.name, r, err)
			}
			check := func(out Outcome, w want, who string) {
				t.Helper()
				if out.Confirmed != w.confirmed || out.UnitPrice != w.unitPrice ||
					out.Total != w.total || out.Reason != w.reason {
					t.Fatalf("%s 表示%d %s: got %+v, want confirmed=%v price=%d total=%d reason=%q",
						c.name, r, who, out, w.confirmed, w.unitPrice, w.total, w.reason)
				}
			}
			check(outOld, c.oldV, "旧版")
			check(outNew, c.newV, "新版")
			if r == 0 {
				firstOld, firstNew = outOld, outNew
				continue
			}
			// 确认状态、金额、拒绝原因与时区表示无关
			if outOld.Confirmed != firstOld.Confirmed || outOld.UnitPrice != firstOld.UnitPrice ||
				outOld.Total != firstOld.Total || outOld.Reason != firstOld.Reason {
				t.Fatalf("%s: 旧版结果随时区表示变化: %+v vs %+v", c.name, firstOld, outOld)
			}
			if outNew.Confirmed != firstNew.Confirmed || outNew.UnitPrice != firstNew.UnitPrice ||
				outNew.Total != firstNew.Total || outNew.Reason != firstNew.Reason {
				t.Fatalf("%s: 新版结果随时区表示变化: %+v vs %+v", c.name, firstNew, outNew)
			}
		}
	}
}

// 新版已有效时，指定旧版的报价必须保留旧版来源并拒绝，不能自动换版计算。
func TestHandoffNoAutoVersionSwitch(t *testing.T) {
	b, _ := newHandoffBook(t, handoffPoint.Add(time.Hour))

	out, err := b.Quote(QuoteRequest{
		RequestID: "no-switch", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("拒绝是正常受理，err 应为空: %v", err)
	}
	if out.Confirmed {
		t.Fatalf("旧版已失效，不能确认: %+v", out)
	}
	if out.Reason != ReasonVersionExpired {
		t.Fatalf("拒绝原因=%q, want %q", out.Reason, ReasonVersionExpired)
	}
	// 拒绝结果单价、总价均为 0，且来源仍是指定的旧版，未被换成新版。
	if out.UnitPrice != 0 || out.Total != 0 {
		t.Fatalf("拒绝结果金额应为 0: 单价=%d 总价=%d", out.UnitPrice, out.Total)
	}
	if out.Request.VersionID != "seat-v1" || out.Request.ItemID != "seat" || out.Request.Quantity != 4 {
		t.Fatalf("拒绝结果必须保留旧版来源: %+v", out.Request)
	}
}

// 交接前确认的 600 分报价，交接后按原标识查询仍保留首次结果，不按当前费率重算。
func TestHandoffConfirmedQuoteSurvives(t *testing.T) {
	acceptedAt := time.Date(2026, 3, 2, 10, 0, 0, 0, east8)
	b, setNow := newHandoffBook(t, acceptedAt)

	req := QuoteRequest{RequestID: "keep-600", ItemID: "seat", VersionID: "seat-v1", Quantity: 4}
	first, err := b.Quote(req)
	if err != nil || !first.Confirmed {
		t.Fatalf("交接前报价应确认: %v %+v", err, first)
	}
	if first.UnitPrice != 150 || first.Total != 600 {
		t.Fatalf("交接前金额: 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	if !first.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("首次受理时刻: %v", first.AcceptedAt)
	}

	// 推进到交接点之后：旧版已失效，新版 180 分已生效。
	setNow(handoffPoint.Add(24 * time.Hour))

	got, err := b.Lookup("keep-600")
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("历史报价被重算: %+v -> %+v", first, got)
	}
	if got.Request.VersionID != "seat-v1" || got.Request.Quantity != 4 ||
		got.UnitPrice != 150 || got.Total != 600 || !got.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("历史报价未保留原版本/数量/金额/受理时刻: %+v", got)
	}

	// 原样重试同样返回首次结果，而不是按当前费率重算。
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatal(err)
	}
	if replay != first {
		t.Fatalf("重试结果被重算: %+v -> %+v", first, replay)
	}
}
