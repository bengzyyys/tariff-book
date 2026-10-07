package tariff

import (
	"errors"
	"testing"
	"time"
)

// 跨时区替代登记回归保障所用的固定时间线（起止时间混用 +08:00 与 Z 两种偏移）：
//
//	旧版 seat-v1：单价 150 分，开始 2026-03-01 09:00 (+08:00) = 01:00Z，
//	             登记结束 2026-03-01 05:00Z = 13:00 (+08:00)
//	新版 seat-v2：单价 180 分，开始 2026-03-01 02:00Z = 10:00 (+08:00)，
//	             合法结束 2026-03-01 11:00 (+08:00) = 03:00Z，替代 seat-v1
//
// 钟点上新版开始（02:00）比旧版开始（09:00）小，但实际时刻 02:00Z 晚于
// 01:00Z，替代仍合法；新版结束的钟点 11:00 看似更晚，实际时刻只有 03:00Z。
// 登记与查询是否合法一律按实际时刻判断，不看钟点大小。
var (
	czOldStart     = time.Date(2026, 3, 1, 9, 0, 0, 0, east8)   // 01:00Z
	czOldStartUTC  = time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC)
	czOldEnd       = time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC) // 旧版登记结束（不含）
	czOldEndEast   = time.Date(2026, 3, 1, 13, 0, 0, 0, east8)
	czNewStart     = time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC) // 交接点 02:00Z
	czNewStartEast = time.Date(2026, 3, 1, 10, 0, 0, 0, east8)
	czNewEnd       = time.Date(2026, 3, 1, 11, 0, 0, 0, east8) // 03:00Z
	czNewEndUTC    = time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)

	// 失败边界 A：新版仍从 02:00Z 开始，结束填 10:00 (+08:00) = 02:00Z，
	// 与开始实际相等，必须返回 ErrInvalidInterval。
	czEndEqualsStart = time.Date(2026, 3, 1, 10, 0, 0, 0, east8)

	// 失败边界 B：新版开始填 13:00 (+08:00) = 05:00Z，恰等于旧版当前实际结束，
	// 结束 14:00 (+08:00) = 06:00Z，必须返回 ErrInvalidReplacement。
	czStartAtOldEnd     = time.Date(2026, 3, 1, 13, 0, 0, 0, east8)
	czStartAtOldEndEnd  = time.Date(2026, 3, 1, 14, 0, 0, 0, east8)
	czStartAtOldEndUTC  = time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC)
)

// assertCrossZoneTimeline 校验本文件所有时间换算前提：
// 两种时区写法必须代表同一实际瞬间，否则后续断言没有意义。
func assertCrossZoneTimeline(t *testing.T) {
	t.Helper()
	pairs := []struct {
		name string
		a, b time.Time
	}{
		{"旧版开始 09:00+08 == 01:00Z", czOldStart, czOldStartUTC},
		{"旧版结束 05:00Z == 13:00+08", czOldEnd, czOldEndEast},
		{"新版开始 02:00Z == 10:00+08", czNewStart, czNewStartEast},
		{"新版结束 11:00+08 == 03:00Z", czNewEnd, czNewEndUTC},
		{"边界A结束 10:00+08 == 02:00Z", czEndEqualsStart, czNewStart},
		{"边界B开始 13:00+08 == 05:00Z", czStartAtOldEnd, czStartAtOldEndUTC},
		{"边界B结束 14:00+08 == 06:00Z",
			czStartAtOldEndEnd, time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)},
	}
	for _, p := range pairs {
		if !p.a.Equal(p.b) {
			t.Fatalf("测试前提不成立（%s）：%v 与 %v 不是同一瞬间", p.name, p.a, p.b)
		}
	}
	// 钟点顺序与实际顺序相反：新版开始钟点 02 小于旧版开始钟点 09，
	// 但实际时刻必须晚于旧版开始。
	if czOldStart.Hour() != 9 || czNewStart.Hour() != 2 {
		t.Fatalf("测试前提不成立：钟点反转示例被改坏：旧版 %d 点、新版 %d 点",
			czOldStart.Hour(), czNewStart.Hour())
	}
	if !czOldStart.Before(czNewStart) {
		t.Fatal("测试前提不成立：旧版开始（01:00Z）应早于新版开始（02:00Z）")
	}
}

// newCrossZoneBaseBook 返回只登记了旧版 seat-v1 的账本：
// 单价 150 分，[2026-03-01 09:00+08, 2026-03-01 05:00Z)，
// 即实际有效期 [01:00Z, 05:00Z)。
func newCrossZoneBaseBook(t *testing.T) *Book {
	t.Helper()
	assertCrossZoneTimeline(t)
	now, _ := fixedClock(czOldStart)
	b := NewBook(WithClock(now))
	oldEnd := czOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     czOldStart, // 09:00+08 = 01:00Z
		End:       &oldEnd,    // 05:00Z
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	return b
}

// registerValidCrossZoneReplacement 以合法的跨时区起止时间登记新版 seat-v2：
// 开始 02:00Z、结束 11:00+08（03:00Z），替代 seat-v1。
func registerValidCrossZoneReplacement(t *testing.T, b *Book) {
	t.Helper()
	newEnd := czNewEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     czNewStart, // 02:00Z，钟点小于旧版的 09:00 但实际更晚
		End:       &newEnd,    // 11:00+08 = 03:00Z
		Replaces:  "seat-v1",
	}); err != nil {
		t.Fatalf("跨时区替代登记应成功：%v", err)
	}
}

// assertOldVersionUntouched 校验失败登记整次回滚：账本中只有旧版，
// 它未被截断、没有被替代关系，失败的新版标识未被占用。
func assertOldVersionUntouched(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("失败登记后应只剩旧版一个版本，got %d: %+v", len(views), views)
	}
	old := views[0]
	if old.VersionID != "seat-v1" {
		t.Fatalf("失败登记不应留下新版，版本集合异常: %+v", views)
	}
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价被改写: %d", old.UnitPrice)
	}
	if !old.Start.Equal(czOldStart) {
		t.Fatalf("旧版开始被改写: %v", old.Start)
	}
	if old.End == nil || !old.End.Equal(czOldEnd) {
		t.Fatalf("旧版登记结束被改写: %v", old.End)
	}
	// 未被替代：实际结束仍是登记结束 05:00Z，不能被截到 02:00Z。
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(czOldEnd) {
		t.Fatalf("失败登记不能截断旧版，实际结束应仍为 05:00Z, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "" {
		t.Fatalf("失败登记不能留下替代关系: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 回滚后交接点仍落在旧版实际有效期 [01:00Z, 05:00Z) 内，查询必须选旧版。
	got, err := b.EffectiveVersionAt("seat", czNewStart)
	if err != nil {
		t.Fatalf("失败回滚后交接点查询应选未被截断的旧版: %v", err)
	}
	if got.VersionID != "seat-v1" {
		t.Fatalf("失败回滚后交接点不应出现新版: %q", got.VersionID)
	}
}

// assertCrossZoneReplacementViews 校验成功后两个版本的登记边界、
// 实际有效区间与替代关系。
func assertCrossZoneReplacementViews(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("成功后应有两个版本，got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("版本集合/顺序异常: %q, %q", old.VersionID, nv.VersionID)
	}

	// 旧版：登记结束仍是 05:00Z；实际有效结束被截到交接点 02:00Z；
	// 钟点 09:00 的开始仍排在钟点 02:00 的新版之前。
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价应为 150, got %d", old.UnitPrice)
	}
	if !old.Start.Equal(czOldStartUTC) || !old.EffectiveStart.Equal(czOldStartUTC) {
		t.Fatalf("旧版实际开始应为 01:00Z: %+v", old)
	}
	if old.End == nil || !old.End.Equal(czOldEnd) {
		t.Fatalf("旧版登记结束应保留为 05:00Z, got %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(czNewStart) {
		t.Fatalf("旧版实际结束应被截到交接点 02:00Z, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}
	if !old.EffectiveStart.Before(nv.EffectiveStart) {
		t.Fatalf("尽管新版开始钟点更小，旧版实际开始仍应早于新版: %v vs %v",
			old.EffectiveStart, nv.EffectiveStart)
	}

	// 新版：实际有效区间 [02:00Z, 03:00Z)，替代旧版。
	if nv.UnitPrice != 180 {
		t.Fatalf("新版单价应为 180, got %d", nv.UnitPrice)
	}
	if !nv.Start.Equal(czNewStart) || !nv.EffectiveStart.Equal(czNewStart) {
		t.Fatalf("新版开始应为 02:00Z: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(czNewEndUTC) {
		t.Fatalf("新版登记结束实际时刻应为 03:00Z, got %v", nv.End)
	}
	if nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(czNewEndUTC) {
		t.Fatalf("新版实际结束应为 03:00Z, got %v", nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}
}

// sameVersionView 按实际时刻比较两个版本视图的关键字段，
// 不要求两边的时间采用同一时区写法。
func sameVersionView(a, b VersionView) bool {
	endEq := (a.End == nil) == (b.End == nil)
	if a.End != nil && b.End != nil && !a.End.Equal(*b.End) {
		endEq = false
	}
	effEq := (a.EffectiveEnd == nil) == (b.EffectiveEnd == nil)
	if a.EffectiveEnd != nil && b.EffectiveEnd != nil && !a.EffectiveEnd.Equal(*b.EffectiveEnd) {
		effEq = false
	}
	return a.ItemID == b.ItemID &&
		a.VersionID == b.VersionID &&
		a.UnitPrice == b.UnitPrice &&
		a.Start.Equal(b.Start) &&
		a.EffectiveStart.Equal(b.EffectiveStart) &&
		endEq && effEq &&
		a.Replaces == b.Replaces &&
		a.SupersededBy == b.SupersededBy
}

// assertCrossZoneEffectiveQueries 用同一瞬间的 Z 与 +08:00 两种表示分别查询，
// 选择结论必须一致：旧版有效期内选旧版，交接点选新版，新版结束（03:00Z）
// 与旧版登记结束（05:00Z）都返回 ErrNoEffectiveVersion——旧版不会因登记
// 结束尚未到来而恢复。
func assertCrossZoneEffectiveQueries(t *testing.T, b *Book) {
	t.Helper()
	cases := []struct {
		name string
		z    time.Time // UTC 表示
		alt  time.Time // 东八区表示（同一瞬间）
		want string    // 期望选中的版本标识；空表示期望 ErrNoEffectiveVersion
	}{
		{"旧版实际有效期内、交接点之前",
			time.Date(2026, 3, 1, 1, 30, 0, 0, time.UTC),
			time.Date(2026, 3, 1, 9, 30, 0, 0, east8),
			"seat-v1"},
		{"交接点 02:00Z / 10:00+08",
			czNewStart, czNewStartEast, "seat-v2"},
		{"新版结束 03:00Z / 11:00+08（结束不含，旧版不恢复）",
			czNewEndUTC, czNewEnd, ""},
		{"旧版登记结束 05:00Z / 13:00+08（同样无生效版本）",
			czOldEnd, czOldEndEast, ""},
	}
	for _, c := range cases {
		if !c.z.Equal(c.alt) {
			t.Fatalf("%s：测试前提不成立，两种表示不是同一瞬间", c.name)
		}
		var first VersionView
		for r, at := range []time.Time{c.z, c.alt} {
			got, err := b.EffectiveVersionAt("seat", at)
			if c.want == "" {
				if !errors.Is(err, ErrNoEffectiveVersion) {
					t.Fatalf("%s 表示%d（%s）：应返回 ErrNoEffectiveVersion, got %v view=%+v",
						c.name, r, at.Format(time.RFC3339), err, got)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s 表示%d（%s）：应选 %s, got err %v",
					c.name, r, at.Format(time.RFC3339), c.want, err)
			}
			if got.VersionID != c.want {
				t.Fatalf("%s 表示%d（%s）：选中 %q, want %q",
					c.name, r, at.Format(time.RFC3339), got.VersionID, c.want)
			}
			if r == 0 {
				first = got
			} else if !sameVersionView(first, got) {
				t.Fatalf("%s：同一瞬间换时区表示后选择依据变化: %+v vs %+v",
					c.name, first, got)
			}
		}
	}
}

// 主线：跨时区起止时间的替代登记成功后，版本边界、替代关系正确，
// 同一瞬间的两种时区表示查询结论一致。整段流程在全新账本上重复多轮。
func TestCrossTimezoneReplacementFlow(t *testing.T) {
	const rounds = 3
	for round := 0; round < rounds; round++ {
		t.Run("跨时区替代主线", func(t *testing.T) {
			b := newCrossZoneBaseBook(t)
			registerValidCrossZoneReplacement(t, b)
			assertCrossZoneReplacementViews(t, b)
			assertCrossZoneEffectiveQueries(t, b)
		})
	}
}

// 失败边界 A：新版结束（10:00+08 = 02:00Z）与开始（02:00Z）实际相等，
// 必须返回 ErrInvalidInterval；失败不留新版、不占标识、不截短旧版。
// 把结束修正为 11:00+08（03:00Z）后，同一新版标识仍能登记成功。
func TestCrossTimezoneEndEqualsStartRejectedThenFixed(t *testing.T) {
	b := newCrossZoneBaseBook(t)

	badEnd := czEndEqualsStart
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: czNewStart, End: &badEnd, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrInvalidInterval) {
		t.Fatalf("结束实际等于开始应返回 ErrInvalidInterval, got %v", err)
	}
	assertOldVersionUntouched(t, b)

	// 修正为合法时间后沿用同一标识 seat-v2，必须成功且替代关系正常建立。
	registerValidCrossZoneReplacement(t, b)
	assertCrossZoneReplacementViews(t, b)
	assertCrossZoneEffectiveQueries(t, b)
}

// 失败边界 B：新版开始（13:00+08 = 05:00Z）恰等于旧版当前实际结束，
// 不属于“落在旧版有效区间内”，必须返回 ErrInvalidReplacement
// （新版自身区间 [05:00Z, 06:00Z) 合法，不能误报成 ErrInvalidInterval）。
// 失败同样不留痕迹；把开始修正为 02:00Z、结束为 11:00+08 后，
// 同一新版标识仍能登记成功。
func TestCrossTimezoneStartAtOldEffectiveEndRejectedThenFixed(t *testing.T) {
	b := newCrossZoneBaseBook(t)

	lateEnd := czStartAtOldEndEnd
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: czStartAtOldEnd, End: &lateEnd, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrInvalidReplacement) {
		t.Fatalf("新版开始等于旧版实际结束应返回 ErrInvalidReplacement, got %v", err)
	}
	assertOldVersionUntouched(t, b)

	registerValidCrossZoneReplacement(t, b)
	assertCrossZoneReplacementViews(t, b)
	assertCrossZoneEffectiveQueries(t, b)
}

// 结论不随本机默认时区变化：在多种 time.Local 下重跑主线与两个失败边界，
// 结果必须完全一致。所有登记与查询时刻都带显式偏移，不依赖本地时区解析。
func TestCrossTimezoneFlowIndependentOfLocalZone(t *testing.T) {
	zones := []*time.Location{
		time.UTC,
		east8,
		time.FixedZone("UTC-8", -8*3600),
		time.FixedZone("UTC+5:30", 5*3600+1800),
	}
	for _, loc := range zones {
		t.Run(loc.String(), func(t *testing.T) {
			prevLocal := time.Local
			time.Local = loc
			defer func() { time.Local = prevLocal }()

			// 主线成功与查询。
			b := newCrossZoneBaseBook(t)
			registerValidCrossZoneReplacement(t, b)
			assertCrossZoneReplacementViews(t, b)
			assertCrossZoneEffectiveQueries(t, b)

			// 失败边界 A。
			badA := newCrossZoneBaseBook(t)
			badEndA := czEndEqualsStart
			if err := badA.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: czNewStart, End: &badEndA, Replaces: "seat-v1",
			}); !errors.Is(err, ErrInvalidInterval) {
				t.Fatalf("边界A 应返回 ErrInvalidInterval, got %v", err)
			}
			assertOldVersionUntouched(t, badA)

			// 失败边界 B。
			badB := newCrossZoneBaseBook(t)
			badEndB := czStartAtOldEndEnd
			if err := badB.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: czStartAtOldEnd, End: &badEndB, Replaces: "seat-v1",
			}); !errors.Is(err, ErrInvalidReplacement) {
				t.Fatalf("边界B 应返回 ErrInvalidReplacement, got %v", err)
			}
			assertOldVersionUntouched(t, badB)
		})
	}
}
