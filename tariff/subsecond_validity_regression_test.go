package tariff

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// “开始、替代交接与到期全部落在同一秒内”的亚秒级有效期回归时间线。
// 所有时刻都以 2026-03-01 UTC 零点为起点按毫秒写死，结束时刻不含：
//
//	旧版 seat-v1：单价 150 分，第 100 毫秒起生效（含），登记结束为第 900 毫秒
//	新版 seat-v2：单价 180 分，第 500 毫秒起替代旧版（含），第 700 毫秒结束（不含）
//
// 两次登记的区间都是同一秒内的正长度区间：旧版实际有效区间被交接点截短为
// [100ms, 500ms)，新版实际有效区间为 [500ms, 700ms)，旧版登记结束 900ms 保持不变。
// 全部判断只按实际时刻进行（开始包含、结束不包含），时刻写死并注入固定时钟，
// 结论不依赖测试实际运行当天的日期或时间流逝。
var (
	subSecBase   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	subSecStart  = subSecBase.Add(100 * time.Millisecond) // 旧版生效起点（含）
	subSecRegEnd = subSecBase.Add(900 * time.Millisecond) // 旧版登记结束（不含）
	subSecHand   = subSecBase.Add(500 * time.Millisecond) // 交接点 / 新版起点（含）
	subSecNewEnd = subSecBase.Add(700 * time.Millisecond) // 新版结束（不含）
)

// newSubSecondBook 返回已按上述时间线登记 v1、v2 的账本与手动时钟。
// 两次登记都必须成功：亚秒级的正长度区间可以登记，交接点不能被提前到整秒。
func newSubSecondBook(t *testing.T) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(subSecStart)
	b := NewBook(WithClock(now))

	oldEnd := subSecRegEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: subSecStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	newEnd := subSecNewEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: subSecHand, End: &newEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// assertSubSecondTimesInSameSecond 是测试前提：整条时间线确实落在同一秒内，
// 且各边界彼此不同、毫秒偏移符合预期——否则本回归就没有测到亚秒级行为。
func assertSubSecondTimesInSameSecond(t *testing.T) {
	t.Helper()
	for name, ts := range map[string]time.Time{
		"start": subSecStart, "regEnd": subSecRegEnd,
		"hand": subSecHand, "newEnd": subSecNewEnd,
	} {
		if ts.Unix() != subSecBase.Unix() {
			t.Fatalf("%s=%v 不在基准秒 %v 内", name, ts, subSecBase)
		}
	}
	if !(subSecStart.Before(subSecHand) && subSecHand.Before(subSecNewEnd) &&
		subSecNewEnd.Before(subSecRegEnd)) {
		t.Fatalf("亚秒时间线顺序不成立: start=%v hand=%v newEnd=%v regEnd=%v",
			subSecStart, subSecHand, subSecNewEnd, subSecRegEnd)
	}
}

// 亚秒级登记成功后，版本视图必须保留完整时刻的小数部分：
// 旧版登记结束仍是第 900 毫秒，实际结束被截短到第 500 毫秒；
// 新版保留自己的第 500 毫秒开始、第 700 毫秒结束和替代来源。
func TestSubSecondVersionViewPreservesFractions(t *testing.T) {
	assertSubSecondTimesInSameSecond(t)
	b, _ := newSubSecondBook(t)

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("应只有两个版本, got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("版本顺序异常: %q, %q", old.VersionID, nv.VersionID)
	}

	// 旧版：登记信息停留在 [100ms, 900ms)，实际有效区间被新版截短为 [100ms, 500ms)。
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价异常: %d", old.UnitPrice)
	}
	if !old.Start.Equal(subSecStart) || old.Start.Nanosecond() != 100_000_000 {
		t.Fatalf("旧版开始应为第 100 毫秒且小数部分保留: %v", old.Start)
	}
	if old.End == nil || !old.End.Equal(subSecRegEnd) || old.End.Nanosecond() != 900_000_000 {
		t.Fatalf("旧版登记结束应为第 900 毫秒且小数部分保留: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(subSecHand) ||
		old.EffectiveEnd.Nanosecond() != 500_000_000 {
		t.Fatalf("旧版实际结束应被截短到第 500 毫秒且小数部分保留: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 新版：[500ms, 700ms)，登记结束与实际结束一致，替代来源为旧版。
	if nv.UnitPrice != 180 {
		t.Fatalf("新版单价异常: %d", nv.UnitPrice)
	}
	if !nv.Start.Equal(subSecHand) || !nv.EffectiveStart.Equal(subSecHand) ||
		nv.Start.Nanosecond() != 500_000_000 {
		t.Fatalf("新版开始应为第 500 毫秒且小数部分保留: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(subSecNewEnd) || nv.End.Nanosecond() != 700_000_000 ||
		nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(subSecNewEnd) ||
		nv.EffectiveEnd.Nanosecond() != 700_000_000 {
		t.Fatalf("新版结束应为第 700 毫秒且小数部分保留: 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}

	// 返回的结束时刻是独立副本：修改本次视图不能改写账本边界，
	// 再次查询仍应看到完整的毫秒小数。
	*views[0].End = subSecBase
	*views[0].EffectiveEnd = subSecBase
	*views[1].End = subSecBase
	again, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if again[0].End == nil || !again[0].End.Equal(subSecRegEnd) {
		t.Fatalf("旧版登记结束被返回视图的外部修改改写: %v", again[0].End)
	}
	if again[0].EffectiveEnd == nil || !again[0].EffectiveEnd.Equal(subSecHand) {
		t.Fatalf("旧版实际结束被返回视图的外部修改改写: %v", again[0].EffectiveEnd)
	}
	if again[1].End == nil || !again[1].End.Equal(subSecNewEnd) {
		t.Fatalf("新版结束被返回视图的外部修改改写: %v", again[1].End)
	}
}

// 同一秒内的指定时刻查询：开始包含、结束不包含、交接点不能对齐到整秒。
// [100ms, 500ms) 选旧版；[500ms, 700ms) 选新版；第 700 毫秒起没有任何生效版本，
// 即便旧版登记的结束时刻第 900 毫秒还没到，也不能重新选中旧版。
func TestSubSecondEffectiveVersionAtBoundaries(t *testing.T) {
	b, _ := newSubSecondBook(t)

	cases := []struct {
		name      string
		at        time.Time
		versionID string
		unitPrice int64
	}{
		{"旧版开始前一纳秒-无版本", subSecStart.Add(-time.Nanosecond), "", 0},
		{"旧版开始本身-含", subSecStart, "seat-v1", 150},
		{"旧版区间内", subSecBase.Add(300 * time.Millisecond), "seat-v1", 150},
		{"交接点前一纳秒-仍是旧版", subSecHand.Add(-time.Nanosecond), "seat-v1", 150},
		{"交接点本身-选新版", subSecHand, "seat-v2", 180},
		{"新版区间内", subSecBase.Add(600 * time.Millisecond), "seat-v2", 180},
		{"新版结束前一纳秒-仍是新版", subSecNewEnd.Add(-time.Nanosecond), "seat-v2", 180},
		{"新版结束本身-无版本", subSecNewEnd, "", 0},
		{"到期后早于旧版登记结束-不回退旧版", subSecBase.Add(800 * time.Millisecond), "", 0},
		{"旧版登记结束前一纳秒-仍无版本", subSecRegEnd.Add(-time.Nanosecond), "", 0},
		{"旧版登记结束本身-仍无版本", subSecRegEnd, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view, err := b.EffectiveVersionAt("seat", c.at)
			if c.versionID == "" {
				if !errors.Is(err, ErrNoEffectiveVersion) {
					t.Fatalf("时刻 %v 应返回 ErrNoEffectiveVersion, got %v (view=%+v)",
						c.at, err, view)
				}
				return
			}
			if err != nil {
				t.Fatalf("时刻 %v 应命中版本: %v", c.at, err)
			}
			if view.VersionID != c.versionID || view.UnitPrice != c.unitPrice {
				t.Fatalf("时刻 %v 应选 %s（%d 分）, got %s（%d 分）",
					c.at, c.versionID, c.unitPrice, view.VersionID, view.UnitPrice)
			}
		})
	}
}

// 在各边界时刻用新的非空请求标识、数量 4 引用指定版本报价：
// 有效旧版按 150 分确认 600 分，有效新版按 180 分确认 720 分；
// 交接前引用新版得到 version_not_yet_effective，交接点起引用旧版、
// 以及新版结束时引用新版，都得到 version_expired。拒绝也是正常受理
// （err 为空、单价总价为零），结果保留指定的费率项、版本、数量和
// 精确到纳秒的受理时刻，不自动改用另一版。
func TestSubSecondBoundaryQuotes(t *testing.T) {
	b, setNow := newSubSecondBook(t)

	cases := []struct {
		name      string
		at        time.Time
		versionID string
		confirmed bool
		unitPrice int64
		total     int64
		reason    RejectReason
	}{
		{"旧版开始本身-旧版确认", subSecStart, "seat-v1", true, 150, 600, ReasonNone},
		{"交接点前一纳秒-旧版确认", subSecHand.Add(-time.Nanosecond), "seat-v1", true, 150, 600, ReasonNone},
		{"交接点前一纳秒-新版尚未生效", subSecHand.Add(-time.Nanosecond), "seat-v2", false, 0, 0, ReasonVersionNotYetEffective},
		{"交接点本身-旧版已失效", subSecHand, "seat-v1", false, 0, 0, ReasonVersionExpired},
		{"交接点本身-新版确认", subSecHand, "seat-v2", true, 180, 720, ReasonNone},
		{"新版结束前一纳秒-新版确认", subSecNewEnd.Add(-time.Nanosecond), "seat-v2", true, 180, 720, ReasonNone},
		{"新版结束前一纳秒-旧版已失效", subSecNewEnd.Add(-time.Nanosecond), "seat-v1", false, 0, 0, ReasonVersionExpired},
		{"新版结束本身-新版已失效", subSecNewEnd, "seat-v2", false, 0, 0, ReasonVersionExpired},
		{"新版结束本身-旧版仍已失效", subSecNewEnd, "seat-v1", false, 0, 0, ReasonVersionExpired},
		{"到期后早于旧版登记结束-旧版不复活", subSecBase.Add(800 * time.Millisecond), "seat-v1", false, 0, 0, ReasonVersionExpired},
		{"到期后早于旧版登记结束-新版已失效", subSecBase.Add(800 * time.Millisecond), "seat-v2", false, 0, 0, ReasonVersionExpired},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setNow(c.at)
			req := QuoteRequest{
				// 每个时刻用全新的非空标识，避免命中幂等保留的首次结果。
				RequestID: fmt.Sprintf("subsecond-%02d", i),
				ItemID:    "seat",
				VersionID: c.versionID,
				Quantity:  4,
			}
			out, err := b.Quote(req)
			if err != nil {
				t.Fatalf("确认/拒绝都是正常受理，err 应为空, got %v", err)
			}
			if out.Confirmed != c.confirmed {
				t.Fatalf("Confirmed=%v, want %v (%+v)", out.Confirmed, c.confirmed, out)
			}
			if out.UnitPrice != c.unitPrice || out.Total != c.total {
				t.Fatalf("单价/总价=%d/%d, want %d/%d",
					out.UnitPrice, out.Total, c.unitPrice, c.total)
			}
			if out.Reason != c.reason {
				t.Fatalf("拒绝原因=%q, want %q", out.Reason, c.reason)
			}
			// 来源仍是调用方指定的版本与数量：拒绝不能自动改用另一版计算。
			if out.Request != req {
				t.Fatalf("结果未保留原请求的费率项/版本/数量: %+v vs %+v", out.Request, req)
			}
			// 受理时刻精确到纳秒：亚秒边界不能被截断到整秒。
			if !out.AcceptedAt.Equal(c.at) || out.AcceptedAt.Unix() != c.at.Unix() ||
				out.AcceptedAt.Nanosecond() != c.at.Nanosecond() {
				t.Fatalf("受理时刻=%v, want 精确时刻 %v", out.AcceptedAt, c.at)
			}

			// 首次结果（含拒绝）按标识保存，再次查询与受理时完全一致。
			saved, err := b.Lookup(req.RequestID)
			if err != nil {
				t.Fatalf("首次受理结果应可按标识查询: %v", err)
			}
			if saved != out {
				t.Fatalf("保存结果与受理结果不一致: %+v vs %+v", saved, out)
			}
		})
	}
}

// 同一秒内结束恰好等于开始或早于开始时，登记仍返回 ErrInvalidInterval，
// 不留版本、不占用版本标识，也不改写已有版本的时间与替代关系——
// 即使请求声明了替代旧版，截短也不能发生。
func TestSubSecondInvalidIntervalRejectedWithoutTrace(t *testing.T) {
	cases := []struct {
		name      string
		versionID string
		start     time.Time
		end       time.Time
		replaces  string
	}{
		{
			"结束恰好等于开始-不提前到整秒",
			"seat-v3-eq",
			subSecBase.Add(150 * time.Millisecond),
			subSecBase.Add(150 * time.Millisecond),
			"seat-v1",
		},
		{
			"结束早于开始-同在一秒内",
			"seat-v3-before",
			subSecBase.Add(150 * time.Millisecond),
			subSecBase.Add(140 * time.Millisecond),
			"seat-v1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := newSubSecondBook(t)

			err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: c.versionID, UnitPrice: 200,
				Start: c.start, End: &c.end, Replaces: c.replaces,
			})
			if !errors.Is(err, ErrInvalidInterval) {
				t.Fatalf("应返回 ErrInvalidInterval, got %v", err)
			}

			views, err := b.ItemVersions("seat")
			if err != nil {
				t.Fatal(err)
			}
			if len(views) != 2 {
				t.Fatalf("失败登记不应留下版本，应仍只有 2 个, got %d: %+v",
					len(views), views)
			}
			old, nv := views[0], views[1]
			if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
				t.Fatalf("失败登记后版本集合异常: %q, %q", old.VersionID, nv.VersionID)
			}

			// 旧版的登记结束、截短后的实际结束与替代关系都保持不变。
			if old.End == nil || !old.End.Equal(subSecRegEnd) ||
				old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(subSecHand) {
				t.Fatalf("旧版时间被失败登记改写: 登记结束=%v 实际结束=%v",
					old.End, old.EffectiveEnd)
			}
			if old.SupersededBy != "seat-v2" {
				t.Fatalf("旧版替代关系被失败登记改写: SupersededBy=%q", old.SupersededBy)
			}
			// 新版的开始、结束与替代来源保持不变。
			if !nv.Start.Equal(subSecHand) || nv.End == nil || !nv.End.Equal(subSecNewEnd) ||
				nv.Replaces != "seat-v1" {
				t.Fatalf("新版信息被失败登记改写: %+v", nv)
			}

			// 失败不占用版本标识：同一标识用于一次落在 v1 剩余实际区间
			// [100ms, 500ms) 内、结束与交接点相接（不含）的合法登记，
			// 必须成功——端点相接不能与新版 [500ms, 700ms) 误判重叠。
			goodEnd := subSecHand
			if err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: c.versionID, UnitPrice: 200,
				Start: c.start, End: &goodEnd, Replaces: "seat-v1",
			}); err != nil {
				t.Fatalf("失败登记不应占用版本标识，合法登记应成功, got %v", err)
			}
		})
	}
}
