package tariff

import (
	"errors"
	"testing"
	"time"
)

// “为同一费率项重复登记已有版本”场景的固定时间线（全部为 UTC 零点，
// 开始时刻含在有效期内、结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 起，登记结束 2026-03-31
//	新版 seat-v2：单价 200 分，2026-03-20 起、2026-03-25 结束，并替代 v1
//
// v2 登记后，v1 的实际结束被截短到 3 月 20 日，登记结束仍是 3 月 31 日，
// v2 保留替代 v1 的关系。重复登记占用中的版本标识必须返回 ErrVersionExists，
// 本次回归重点保护拒绝登记之后的账本状态：不能靠再次提交同一标识覆盖既有版本，
// 也不能把重复登记当成一次新的替代操作。所有时刻在代码中写死并注入固定时钟，
// 结论不依赖运行当天的真实日期或时间流逝。
var (
	dvV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	dvV1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // v1 登记结束（不含）
	dvV2Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // v2 生效并替代 v1 的交接点
	dvV2End   = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC) // v2 登记结束（不含）
)

// newDuplicateVersionBaseBook 准备已完成一次替代的账本：
// v1 单价 150 分、登记结束 3 月 31 日，实际结束已被 v2 截短到 3 月 20 日；
// v2 单价 200 分、实际有效区间 [03-20, 03-25)，并保留替代 v1 的关系。
func newDuplicateVersionBaseBook(t *testing.T) *Book {
	t.Helper()
	now, _ := fixedClock(dvV1Start)
	b := NewBook(WithClock(now))
	v1End := dvV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: dvV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	v2End := dvV2End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 200,
		Start: dvV2Start, End: &v2End, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b
}

// assertDuplicateVersionBaseUnchanged 校验重复登记被拒后账本保持原状：
// 仍只有 v1、v2 两个版本；v1 的单价、登记结束、被截短后的实际区间和
// “由 v2 替代”的关系不变，v2 的单价、登记时间、实际区间和替代来源不变。
// 不能把 v1 再截短，也不能把已有 v2 的生效时间提前或改写其单价。
func assertDuplicateVersionBaseUnchanged(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("拒绝重复登记后应仍只有两个版本，got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("拒绝重复登记后版本集合/顺序异常: %q, %q", old.VersionID, nv.VersionID)
	}

	// 旧版：单价 150 分；登记结束仍是 3 月 31 日，实际结束保持被 v2
	// 截短后的 3 月 20 日，不能被重复登记再提前；替代关系仍指向 v2。
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价被改写: %d", old.UnitPrice)
	}
	if !old.Start.Equal(dvV1Start) || !old.EffectiveStart.Equal(dvV1Start) {
		t.Fatalf("旧版开始时刻被改写: %+v", old)
	}
	if old.End == nil || !old.End.Equal(dvV1End) {
		t.Fatalf("旧版登记结束被改写: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(dvV2Start) {
		t.Fatalf("旧版实际结束应保持 3 月 20 日, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系被改写: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 新版：单价 200 分；实际有效区间仍是 [03-20, 03-25)，
	// 不能被重复登记提前到 3 月 10 日，单价也不能改成 180 分；
	// 替代来源仍是 v1。
	if nv.UnitPrice != 200 {
		t.Fatalf("新版单价被重复登记覆盖: %d", nv.UnitPrice)
	}
	if !nv.Start.Equal(dvV2Start) || !nv.EffectiveStart.Equal(dvV2Start) {
		t.Fatalf("新版生效时间被提前或改写: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(dvV2End) ||
		nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(dvV2End) {
		t.Fatalf("新版有效区间应保持 [03-20, 03-25): 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系被改写: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}
}

// 重复登记已有版本标识必须返回可识别的 ErrVersionExists，且整次登记不留变更：
// 请求中的单价 180 分、起止 [03-10, 03-20) 本身合法，交接点 3 月 10 日也落在
// v1 当前实际有效期 [03-01, 03-20) 内，但版本标识 seat-v2 已被占用——
// 标识占用的判断先于替代与重叠校验，不能当成一次新的替代操作接受，
// 也不能先把 v1 截短到 3 月 10 日再报错。
func TestDuplicateVersionRegistrationRejectedAndLedgerUnchanged(t *testing.T) {
	b := newDuplicateVersionBaseBook(t)

	dupStart := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	dupEnd := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dupStart, End: &dupEnd, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrVersionExists) {
		t.Fatalf("重复登记已有版本标识应返回 ErrVersionExists, got %v", err)
	}

	// 拒绝后账本保持原状：两版的单价、登记时间、实际区间与替代关系都不变。
	assertDuplicateVersionBaseUnchanged(t, b)
}

// 拒绝重复登记后，按时刻选版仍以原来的实际有效区间为准：
// 3 月 15 日选中 v1（150 分），3 月 20 日交接点（含）选中原来的 v2（200 分），
// 3 月 25 日 v2 已到期（结束时刻不含）且旧版不恢复，返回 ErrNoEffectiveVersion。
func TestEffectiveVersionAtAfterDuplicateRegistrationRejected(t *testing.T) {
	b := newDuplicateVersionBaseBook(t)

	dupStart := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	dupEnd := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dupStart, End: &dupEnd, Replaces: "seat-v1",
	}); !errors.Is(err, ErrVersionExists) {
		t.Fatalf("重复登记应返回 ErrVersionExists, got %v", err)
	}

	cases := []struct {
		name      string
		at        time.Time
		versionID string
		unitPrice int64
	}{
		{"v1 实际有效期内-3月15日", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), "seat-v1", 150},
		{"v2 交接点-含", dvV2Start, "seat-v2", 200},
		{"v2 区间内-3月24日", time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC), "seat-v2", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view, err := b.EffectiveVersionAt("seat", c.at)
			if err != nil {
				t.Fatalf("时刻 %v 应选中 %s: %v", c.at, c.versionID, err)
			}
			if view.VersionID != c.versionID || view.UnitPrice != c.unitPrice {
				t.Fatalf("时刻 %v 应选 %s（%d 分）, got %s（%d 分）",
					c.at, c.versionID, c.unitPrice, view.VersionID, view.UnitPrice)
			}
		})
	}

	// v2 结束时刻不含该点，且被替代的 v1 不会在 v2 到期后恢复。
	if _, err := b.EffectiveVersionAt("seat", dvV2End); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("3 月 25 日应返回 ErrNoEffectiveVersion, got %v", err)
	}
}

// 拒绝重复登记后，用各自未使用过的请求标识在 3 月 15 日与 3 月 20 日分别
// 引用当时有效的版本、数量均为 4：前者按原 v1 的 150 分确认总价 600 分，
// 后者按原 v2 的 200 分确认总价 800 分，来源保持对应版本。
func TestQuoteAfterDuplicateRegistrationRejected(t *testing.T) {
	now, setNow := fixedClock(dvV1Start)
	b := NewBook(WithClock(now))
	v1End := dvV1End
	v2End := dvV2End
	for _, req := range []RegisterRequest{
		{ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150, Start: dvV1Start, End: &v1End},
		{ItemID: "seat", VersionID: "seat-v2", UnitPrice: 200, Start: dvV2Start, End: &v2End, Replaces: "seat-v1"},
	} {
		if err := b.RegisterVersion(req); err != nil {
			t.Fatalf("register %s: %v", req.VersionID, err)
		}
	}

	dupStart := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	dupEnd := dvV2Start
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dupStart, End: &dupEnd, Replaces: "seat-v1",
	}); !errors.Is(err, ErrVersionExists) {
		t.Fatalf("重复登记应返回 ErrVersionExists, got %v", err)
	}

	// 3 月 15 日：v1 仍在被截短后的实际有效期 [03-01, 03-20) 内。
	setNow(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	oldQuote, err := b.Quote(QuoteRequest{
		RequestID: "dv-quote-v1-march15", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("引用 v1 是正常受理，err 应为空: %v", err)
	}
	if !oldQuote.Confirmed || oldQuote.UnitPrice != 150 || oldQuote.Total != 600 {
		t.Fatalf("3 月 15 日引用 v1 应按 150 分确认总价 600 分: %+v", oldQuote)
	}
	if oldQuote.Request.VersionID != "seat-v1" || oldQuote.Request.Quantity != 4 {
		t.Fatalf("v1 报价来源/数量异常: %+v", oldQuote.Request)
	}

	// 3 月 20 日交接点（含）：选中原来的 v2，按 200 分确认总价 800 分。
	setNow(dvV2Start)
	newQuote, err := b.Quote(QuoteRequest{
		RequestID: "dv-quote-v2-march20", ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("引用 v2 是正常受理，err 应为空: %v", err)
	}
	if !newQuote.Confirmed || newQuote.UnitPrice != 200 || newQuote.Total != 800 {
		t.Fatalf("3 月 20 日引用 v2 应按 200 分确认总价 800 分: %+v", newQuote)
	}
	if newQuote.Request.VersionID != "seat-v2" || newQuote.Request.Quantity != 4 {
		t.Fatalf("v2 报价来源/数量异常: %+v", newQuote.Request)
	}
}

// 即使第二次提交的内容与最初成功登记的 v2 完全一致，重复登记也仍必须返回
// ErrVersionExists：不能把内容相同的重复登记改成成功的空操作（no-op），
// 账本仍保持原来的两个版本。
func TestIdenticalDuplicateVersionRegistrationStillRejected(t *testing.T) {
	b := newDuplicateVersionBaseBook(t)

	sameEnd := dvV2End
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 200,
		Start: dvV2Start, End: &sameEnd, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrVersionExists) {
		t.Fatalf("内容相同的重复登记也应返回 ErrVersionExists, got %v", err)
	}
	assertDuplicateVersionBaseUnchanged(t, b)
}
