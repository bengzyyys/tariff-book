package tariff

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// 亚秒级有效期回归场景的固定时间线（全部为 UTC，结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00:00.100 起，登记结束 00:00:00.900
//	新版 seat-v2：单价 180 分，2026-03-01 00:00:00.500 起替代旧版，结束 00:00:00.700
//
// 开始、交接、到期全部落在同一秒内，且都带有非零的小数部分。账本必须保留
// 完整时刻：仍按开始包含、结束不包含判断，正长度区间（哪怕只有几百毫秒）
// 可以登记，交接点不能提前或取整到整秒。所有时刻写死并注入固定时钟，
// 结论不依赖运行当天的真实日期或时间流逝。
var (
	subBase     = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	subOldStart = subBase.Add(100 * time.Millisecond) // 旧版开始（含）
	subOldEnd   = subBase.Add(900 * time.Millisecond) // 旧版登记结束（不含）
	subHandoff  = subBase.Add(500 * time.Millisecond) // 新版开始（含），即旧版实际结束（不含）
	subNewEnd   = subBase.Add(700 * time.Millisecond) // 新版结束（不含）
)

// newSubsecondBook 返回已登记 seat-v1/seat-v2 亚秒级交接关系的账本及其时钟。
// 两次登记都必须成功：旧版区间 [100ms, 900ms) 与新版区间 [500ms, 700ms)
// 都是正长度区间，不能因不足一秒而被误判无效或取整。
func newSubsecondBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	oldEnd := subOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: subOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	newEnd := subNewEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: subHandoff, End: &newEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// 版本视图：旧版登记结束仍是 900ms，实际结束被截短到 500ms 的交接点；
// 新版保留自己的开始、结束和替代来源。所有时刻的毫秒小数部分不能丢失，
// 交接点尤其不能提前或取整到整秒。
func TestSubsecondVersionViewKeepsFractions(t *testing.T) {
	b, _ := newSubsecondBook(t, subBase)

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("want 2 versions, got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("unexpected order: %q, %q", old.VersionID, nv.VersionID)
	}

	// 旧版：登记边界原样保留，实际结束被截短到 500ms 交接点。
	if old.UnitPrice != 150 || !old.Start.Equal(subOldStart) {
		t.Fatalf("旧版登记信息异常: %+v", old)
	}
	if old.End == nil || !old.End.Equal(subOldEnd) {
		t.Fatalf("旧版登记结束应仍为 900ms: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(subHandoff) {
		t.Fatalf("旧版实际结束应为 500ms 交接点: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 新版：保留自己的开始、结束与替代来源。
	if nv.UnitPrice != 180 || !nv.Start.Equal(subHandoff) ||
		!nv.EffectiveStart.Equal(subHandoff) {
		t.Fatalf("新版登记信息异常: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(subNewEnd) ||
		nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(subNewEnd) {
		t.Fatalf("新版有效区间应为 [500ms, 700ms): 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}

	// 小数部分不能丢失：所有边界都带非零毫秒，不能落到整秒上。
	for name, tm := range map[string]time.Time{
		"旧版开始":   old.Start,
		"旧版登记结束": *old.End,
		"旧版实际结束": *old.EffectiveEnd,
		"新版开始":   nv.Start,
		"新版结束":   *nv.End,
	} {
		if tm.Nanosecond() == 0 {
			t.Fatalf("%s 的小数部分丢失，落到了整秒: %v", name, tm)
		}
	}
}

// 按指定时刻查询：开始包含、结束不包含，亚秒边界精确到纳秒。
// 新版到期后即使还没到旧版登记的 900ms 结束时刻，也不能回退选中旧版。
func TestSubsecondEffectiveVersionAt(t *testing.T) {
	b, _ := newSubsecondBook(t, subBase)

	cases := []struct {
		name      string
		at        time.Time
		versionID string
		unitPrice int64
	}{
		{"旧版开始-含", subOldStart, "seat-v1", 150},
		{"旧版区间中段", subBase.Add(300 * time.Millisecond), "seat-v1", 150},
		{"交接点前一纳秒", subHandoff.Add(-time.Nanosecond), "seat-v1", 150},
		{"交接点本身-选新版", subHandoff, "seat-v2", 180},
		{"新版区间中段", subBase.Add(600 * time.Millisecond), "seat-v2", 180},
		{"新版结束前一纳秒", subNewEnd.Add(-time.Nanosecond), "seat-v2", 180},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view, err := b.EffectiveVersionAt("seat", c.at)
			if err != nil {
				t.Fatalf("查询应命中版本: %v", err)
			}
			if view.VersionID != c.versionID || view.UnitPrice != c.unitPrice {
				t.Fatalf("时刻 %v 应选 %s（%d 分）, got %s（%d 分）",
					c.at, c.versionID, c.unitPrice, view.VersionID, view.UnitPrice)
			}
		})
	}

	// 新版到期时刻本身（不含）及之后：没有任何生效版本。
	// 即使还没到旧版登记的 900ms 结束时刻，也不能回退选中旧版。
	for _, c := range []struct {
		name string
		at   time.Time
	}{
		{"新版结束本身", subNewEnd},
		{"旧版登记结束前", subBase.Add(800 * time.Millisecond)},
		{"旧版登记结束本身", subOldEnd},
		{"旧版登记结束之后", subOldEnd.Add(time.Millisecond)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := b.EffectiveVersionAt("seat", c.at); !errors.Is(err, ErrNoEffectiveVersion) {
				t.Fatalf("时刻 %v 应返回 ErrNoEffectiveVersion, got %v", c.at, err)
			}
		})
	}
}

// 报价与查询共用同一套亚秒级有效期判断：数量 4，有效旧版确认 600 分，
// 有效新版确认 720 分；交接前引用新版得到 version_not_yet_effective，
// 交接点起引用旧版得到 version_expired，新版结束时也得到 version_expired。
// 拒绝仍是正常受理：err 为空，单价总价为零，结果保留指定的费率项、版本、
// 数量和精确的受理时刻，不自动改用另一版。
func TestSubsecondBoundaryQuotes(t *testing.T) {
	b, setNow := newSubsecondBook(t, subBase)

	cases := []struct {
		name      string
		instant   time.Time
		versionID string
		confirmed bool
		unitPrice int64
		total     int64
		reason    RejectReason
	}{
		{"旧版开始-旧版确认600", subOldStart, "seat-v1", true, 150, 600, ReasonNone},
		{"旧版开始-新版尚未生效", subOldStart, "seat-v2", false, 0, 0, ReasonVersionNotYetEffective},
		{"交接点前一纳秒-旧版确认600", subHandoff.Add(-time.Nanosecond), "seat-v1", true, 150, 600, ReasonNone},
		{"交接点前一纳秒-新版尚未生效", subHandoff.Add(-time.Nanosecond), "seat-v2", false, 0, 0, ReasonVersionNotYetEffective},
		{"交接点本身-旧版已失效", subHandoff, "seat-v1", false, 0, 0, ReasonVersionExpired},
		{"交接点本身-新版确认720", subHandoff, "seat-v2", true, 180, 720, ReasonNone},
		{"新版结束前一纳秒-新版确认720", subNewEnd.Add(-time.Nanosecond), "seat-v2", true, 180, 720, ReasonNone},
		{"新版结束本身-新版已失效", subNewEnd, "seat-v2", false, 0, 0, ReasonVersionExpired},
		{"旧版登记结束前-旧版不回退", subBase.Add(800 * time.Millisecond), "seat-v1", false, 0, 0, ReasonVersionExpired},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setNow(c.instant)
			req := QuoteRequest{
				RequestID: fmt.Sprintf("sub-%02d", i),
				ItemID:    "seat",
				VersionID: c.versionID,
				Quantity:  4,
			}
			out, err := b.Quote(req)
			if err != nil {
				t.Fatalf("拒绝/确认都是正常受理，err 应为空，got %v", err)
			}
			if out.Confirmed != c.confirmed {
				t.Fatalf("Confirmed=%v, want %v (%+v)", out.Confirmed, c.confirmed, out)
			}
			if out.UnitPrice != c.unitPrice || out.Total != c.total {
				t.Fatalf("单价/总价=%d/%d, want %d/%d",
					out.UnitPrice, out.Total, c.unitPrice, c.total)
			}
			if out.Reason != c.reason {
				t.Fatalf("原因=%q, want %q", out.Reason, c.reason)
			}
			// 结果必须保留请求中的费率项、版本和数量，不自动改用另一版，
			// 并记录精确的受理时刻（含毫秒小数部分）。
			if out.Request != req {
				t.Fatalf("请求来源未保留: %+v vs %+v", out.Request, req)
			}
			if !out.AcceptedAt.Equal(c.instant) {
				t.Fatalf("受理时刻=%v, want %v", out.AcceptedAt, c.instant)
			}
		})
	}
}

// 同一秒内结束恰好等于开始或早于开始时，登记必须返回 ErrInvalidInterval，
// 不留下该版本，也不改动已有版本的时间和替代关系。
func TestSubsecondInvalidIntervalRejected(t *testing.T) {
	cases := []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{"结束等于开始", subBase.Add(300 * time.Millisecond), subBase.Add(300 * time.Millisecond)},
		{"结束早于开始一纳秒", subBase.Add(300 * time.Millisecond), subBase.Add(300*time.Millisecond - time.Nanosecond)},
		{"结束早于开始一毫秒", subBase.Add(300 * time.Millisecond), subBase.Add(299 * time.Millisecond)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := newSubsecondBook(t, subBase)

			err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
				Start: c.start, End: &c.end,
			})
			if !errors.Is(err, ErrInvalidInterval) {
				t.Fatalf("结束不晚于开始的登记应返回 ErrInvalidInterval, got %v", err)
			}

			// 失败不留痕迹：v3 不存在，v1/v2 的时间与替代关系保持原样。
			views, err := b.ItemVersions("seat")
			if err != nil {
				t.Fatal(err)
			}
			if len(views) != 2 {
				t.Fatalf("失败登记后应仍只有两个版本, got %d: %+v", len(views), views)
			}
			old, nv := views[0], views[1]
			if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
				t.Fatalf("失败登记后版本集合异常: %q, %q", old.VersionID, nv.VersionID)
			}
			if old.End == nil || !old.End.Equal(subOldEnd) ||
				old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(subHandoff) ||
				old.SupersededBy != "seat-v2" {
				t.Fatalf("旧版被失败登记改写: %+v", old)
			}
			if !nv.Start.Equal(subHandoff) ||
				nv.End == nil || !nv.End.Equal(subNewEnd) ||
				nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(subNewEnd) ||
				nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
				t.Fatalf("新版被失败登记改写: %+v", nv)
			}
		})
	}
}
