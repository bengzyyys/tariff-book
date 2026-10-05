package tariff

import (
	"errors"
	"testing"
	"time"
)

// “替代已发生后，又以同一版本标识重复登记”场景的固定时间线
// （全部为 UTC 零点，开始含、结束不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 起，登记结束 2026-03-31
//	新版 seat-v2：单价 200 分，2026-03-20 起替代 v1，登记结束 2026-03-25
//
// v2 登记成功后，v1 的实际结束已提前到 3 月 20 日（登记结束仍是 3 月 31 日），
// v2 保留替代 v1 的关系。此时再以 seat-v2 标识重复登记——无论改填内容
// （单价 180 分、[03-10, 03-20)，交接点本身落在 v1 当前实际有效区间内）
// 还是照抄最初成功的内容——都必须返回 ErrVersionExists，且账本保持原值：
// 不能把 v1 再截短到 3 月 10 日，也不能把已有 v2 的生效时间提前。
// 所有时刻写死并注入固定时钟，结论不依赖运行当天的真实日期或时间流逝。
var (
	dupOldStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	dupOldEnd     = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	dupHandoff    = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	dupNewEnd     = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	dupEarlyStart = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
)

// newDuplicateBaseBook 准备已发生替代的账本：v1 自 3 月 1 日起、登记结束
// 3 月 31 日；v2 自 3 月 20 日起替代 v1、登记结束 3 月 25 日。
// 返回账本与可调时钟，供报价用例把受理时刻推进到指定瞬间。
func newDuplicateBaseBook(t *testing.T) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(dupOldStart)
	b := NewBook(WithClock(now))
	oldEnd := dupOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: dupOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	newEnd := dupNewEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 200,
		Start: dupHandoff, End: &newEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// assertDuplicateBaseUnchanged 校验重复登记被拒后账本保持替代发生后的原值：
// 仍只有 v1、v2 两个版本；v1 单价 150 分、登记结束 3 月 31 日、实际结束
// 保持被 v2 截短后的 3 月 20 日（不能被重复登记再提前到 3 月 10 日）、
// 由 v2 替代；v2 单价 200 分、区间 [03-20, 03-25) 不变（生效时间不能被
// 提前到 3 月 10 日）、替代来源仍是 v1。
func assertDuplicateBaseUnchanged(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("重复登记被拒后应仍只有两个版本，got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("重复登记被拒后版本集合/顺序异常: %q, %q", old.VersionID, nv.VersionID)
	}

	// 旧版：登记信息不变，实际结束保持被 v2 截短后的 3 月 20 日。
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价被改写: %d", old.UnitPrice)
	}
	if !old.Start.Equal(dupOldStart) || !old.EffectiveStart.Equal(dupOldStart) {
		t.Fatalf("旧版开始时刻被改写: 登记开始=%v 实际开始=%v", old.Start, old.EffectiveStart)
	}
	if old.End == nil || !old.End.Equal(dupOldEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日, got %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(dupHandoff) {
		t.Fatalf("旧版实际结束应保持被 v2 截短后的 3 月 20 日, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系被改写: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 新版：单价、起止时间与替代关系都保持最初登记成功的内容。
	if nv.UnitPrice != 200 {
		t.Fatalf("新版单价被改写: %d", nv.UnitPrice)
	}
	if !nv.Start.Equal(dupHandoff) || !nv.EffectiveStart.Equal(dupHandoff) {
		t.Fatalf("新版生效时间不能被提前到 3 月 10 日: 登记开始=%v 实际开始=%v",
			nv.Start, nv.EffectiveStart)
	}
	if nv.End == nil || !nv.End.Equal(dupNewEnd) ||
		nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(dupNewEnd) {
		t.Fatalf("新版有效区间应为 [03-20, 03-25): 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系被改写: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}
}

// 重复登记同一版本标识必须返回可识别的 ErrVersionExists，即使请求中的
// 单价与起止时间本身合法、交接点也落在 v1 当前的实际有效区间内；
// 拒绝不是一次新的替代操作，账本保持替代发生后的原值。
func TestDuplicateVersionRegistrationRejected(t *testing.T) {
	b, _ := newDuplicateBaseBook(t)

	dupEnd := dupHandoff
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dupEarlyStart, End: &dupEnd, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrVersionExists) {
		t.Fatalf("版本标识已被占用，重复登记应返回 ErrVersionExists, got %v", err)
	}

	assertDuplicateBaseUnchanged(t, b)
}

// 按最初登记成功的内容再次登记同一版本标识，也仍应返回 ErrVersionExists
// 并保留账本，不能改成成功的空操作。
func TestDuplicateVersionWithOriginalContentRejected(t *testing.T) {
	b, _ := newDuplicateBaseBook(t)

	newEnd := dupNewEnd
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 200,
		Start: dupHandoff, End: &newEnd, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrVersionExists) {
		t.Fatalf("照抄原始内容的重复登记也应返回 ErrVersionExists, got %v", err)
	}

	assertDuplicateBaseUnchanged(t, b)
}

// 重复登记被拒后按时刻选版：3 月 15 日仍选中 v1，3 月 20 日交接点（含）
// 仍选中原来的 v2，3 月 25 日 v2 到期后返回 ErrNoEffectiveVersion，
// 不能恢复旧版，也不能因重复登记而出现第三个版本。
func TestEffectiveVersionAtAfterDuplicateRejection(t *testing.T) {
	b, _ := newDuplicateBaseBook(t)

	dupEnd := dupHandoff
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dupEarlyStart, End: &dupEnd, Replaces: "seat-v1",
	}); !errors.Is(err, ErrVersionExists) {
		t.Fatalf("重复登记应返回 ErrVersionExists, got %v", err)
	}

	cases := []struct {
		name      string
		at        time.Time
		versionID string
		unitPrice int64
	}{
		{"v1 区间中段", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), "seat-v1", 150},
		{"v2 交接点前一纳秒", dupHandoff.Add(-time.Nanosecond), "seat-v1", 150},
		{"v2 交接点-含", dupHandoff, "seat-v2", 200},
		{"v2 到期前一纳秒", dupNewEnd.Add(-time.Nanosecond), "seat-v2", 200},
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

	// v2 到期后没有任何生效版本，不回退到登记结束仍在将来的 v1。
	if _, err := b.EffectiveVersionAt("seat", dupNewEnd); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("v2 到期后应返回 ErrNoEffectiveVersion, got %v", err)
	}

	assertDuplicateBaseUnchanged(t, b)
}

// 重复登记被拒后报价口径不变：用未使用过的请求标识，在 3 月 15 日引用
// 当时有效的 v1、数量 4，按 150 分确认总价 600 分；在 3 月 20 日引用
// 当时有效的 v2、数量 4，按 200 分确认总价 800 分；来源保持对应版本。
func TestQuoteAfterDuplicateRejection(t *testing.T) {
	b, setNow := newDuplicateBaseBook(t)

	dupEnd := dupHandoff
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dupEarlyStart, End: &dupEnd, Replaces: "seat-v1",
	}); !errors.Is(err, ErrVersionExists) {
		t.Fatalf("重复登记应返回 ErrVersionExists, got %v", err)
	}

	cases := []struct {
		name       string
		acceptedAt time.Time
		requestID  string
		versionID  string
		unitPrice  int64
		total      int64
	}{
		{"v1 有效区间内引用 v1", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
			"dup-q-v1", "seat-v1", 150, 600},
		{"v2 交接点引用 v2", dupHandoff,
			"dup-q-v2", "seat-v2", 200, 800},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setNow(c.acceptedAt)
			out, err := b.Quote(QuoteRequest{
				RequestID: c.requestID, ItemID: "seat",
				VersionID: c.versionID, Quantity: 4,
			})
			if err != nil {
				t.Fatalf("正常受理 err 应为空: %v", err)
			}
			if !out.Confirmed || out.UnitPrice != c.unitPrice || out.Total != c.total {
				t.Fatalf("应按 %d 分确认总价 %d 分: %+v", c.unitPrice, c.total, out)
			}
			if out.Request.VersionID != c.versionID {
				t.Fatalf("报价来源应保持 %s, got %s", c.versionID, out.Request.VersionID)
			}
			if !out.AcceptedAt.Equal(c.acceptedAt) || out.Reason != ReasonNone {
				t.Fatalf("受理时刻/拒绝原因异常: %+v", out)
			}
		})
	}

	assertDuplicateBaseUnchanged(t, b)
}
