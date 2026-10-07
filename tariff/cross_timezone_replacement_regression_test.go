package tariff

import (
	"errors"
	"testing"
	"time"
)

// 跨时区替代登记回归保障所用的固定时间线。
// 同一条时间线上的时刻故意用不同时区偏移填写，制造“钟点显示顺序与实际顺序相反”：
//
//	旧版 seat-v1：单价 150 分
//	  开始 2026-03-01 09:00 (+08:00) —— 即 UTC 01:00
//	  登记结束 2026-03-01 05:00 Z     —— 即 UTC 05:00
//	  实际有效区间 [01:00Z, 05:00Z)。填写时一个钟点是 09 点、一个是 05 点，
//	  若按钟面数字比较会误判成“结束早于开始”，按实际时刻则区间合法。
//	新版 seat-v2：单价 180 分
//	  开始 2026-03-01 02:00 Z         —— 钟面 02 点比旧版开始的 09 点小，
//	                                   实际却是 02:00Z，晚于旧版实际开始 01:00Z
//	  结束 2026-03-01 11:00 (+08:00) —— 即 UTC 03:00
//	  实际有效区间 [02:00Z, 03:00Z)，替代旧版。
//
// 登记成功后：旧版登记结束保留 05:00Z，旧版实际结束被截断到交接点 02:00Z，
// 新版实际结束为 03:00Z；03:00Z 起两版都不生效，旧版不会因登记结束 05:00Z
// 尚未到来而恢复。
var (
	xtzOldStart = time.Date(2026, 3, 1, 9, 0, 0, 0, east8) // 实际 01:00Z
	xtzOldEnd   = time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC)

	xtzNewStart = time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC) // 实际 02:00Z，交接点
	xtzNewEnd   = time.Date(2026, 3, 1, 11, 0, 0, 0, east8)   // 实际 03:00Z

	// 失败边界一：结束 10:00 (+08:00) 恰好等于开始 02:00Z，区间端点相等。
	xtzBadEndEqualStart = time.Date(2026, 3, 1, 10, 0, 0, 0, east8)

	// 失败边界二：开始 13:00 (+08:00) 恰好等于旧版实际结束 05:00Z，
	// 结束 14:00 (+08:00) 即 06:00Z，自身区间合法但替代时刻不合法。
	xtzBoundaryStart = time.Date(2026, 3, 1, 13, 0, 0, 0, east8)
	xtzBoundaryEnd   = time.Date(2026, 3, 1, 14, 0, 0, 0, east8)
)

// 换算到 UTC 的参考时刻，全部用实际时刻（Instant）参与断言。
var (
	xtzOldStartUTC   = time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC)
	xtzHandoffUTC    = time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)
	xtzNewEndUTC     = time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	xtzOldEndUTC     = time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC)
	xtzBoundary06UTC = time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
)

// newXtzOldOnlyBook 返回只登记了旧版 seat-v1 的账本：
// 旧版尚未被任何版本替代，seat-v2 标识也尚未使用——两个失败边界都以此为前提。
func newXtzOldOnlyBook(t *testing.T) *Book {
	t.Helper()
	now, _ := fixedClock(xtzOldStartUTC)
	b := NewBook(WithClock(now))
	oldEnd := xtzOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: xtzOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	return b
}

// newXtzReplacedBook 返回旧版 seat-v1 已被新版 seat-v2 合法替代的账本。
func newXtzReplacedBook(t *testing.T) *Book {
	t.Helper()
	b := newXtzOldOnlyBook(t)
	newEnd := xtzNewEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: xtzNewStart, End: &newEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b
}

// assertXtzOldUntruncated 断言失败登记整次回滚：账本里只有旧版，
// 旧版既未被截断（实际结束仍是登记结束 05:00Z）也没有任何替代关系，
// 失败的新版及其标识都没有留下。
func assertXtzOldUntruncated(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("失败登记不能留下新版，应只剩旧版一个版本, got %d: %+v", len(views), views)
	}
	old := views[0]
	if old.VersionID != "seat-v1" {
		t.Fatalf("失败登记后版本集合异常: %q", old.VersionID)
	}
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价被改写: %d", old.UnitPrice)
	}
	if old.End == nil || !old.End.Equal(xtzOldEndUTC) {
		t.Fatalf("旧版登记结束被改写: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(xtzOldEndUTC) {
		t.Fatalf("旧版不能被失败登记截断，实际结束应仍为 05:00Z, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "" {
		t.Fatalf("失败登记不能留下替代关系: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 旧版在交接点 02:00Z 与新版结束 03:00Z 都仍应实际生效，直接证明未被截断。
	for _, instant := range []time.Time{xtzHandoffUTC, xtzNewEndUTC} {
		got, err := b.EffectiveVersionAt("seat", instant)
		if err != nil {
			t.Fatalf("失败登记后旧版在 %v 应仍生效, got err=%v", instant, err)
		}
		if got.VersionID != "seat-v1" {
			t.Fatalf("失败登记后 %v 选中了 %q，应仍是未被截断的 seat-v1",
				instant, got.VersionID)
		}
	}
}

// assertXtzReplacedState 断言替代登记成功后的完整版本视图：
// 旧版登记结束保留 05:00Z、实际结束截到交接点 02:00Z；
// 新版实际结束 03:00Z；双方互相指向替代关系。
func assertXtzReplacedState(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("成功后应有两个版本, got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("版本应按实际生效时刻排列为 v1、v2, got %q, %q",
			old.VersionID, nv.VersionID)
	}

	if old.UnitPrice != 150 || !old.Start.Equal(xtzOldStartUTC) {
		t.Fatalf("旧版登记信息异常: %+v", old)
	}
	// 旧版登记结束保留登记时填写的 05:00Z，永不因被替代而改写。
	if old.End == nil || !old.End.Equal(xtzOldEndUTC) {
		t.Fatalf("旧版登记结束应保留为 05:00Z, got %v", old.End)
	}
	// 旧版实际结束被截断到交接点 02:00Z，与登记结束不是同一个值。
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(xtzHandoffUTC) {
		t.Fatalf("旧版实际结束应为交接点 02:00Z, got %v", old.EffectiveEnd)
	}
	if old.End.Equal(*old.EffectiveEnd) {
		t.Fatalf("旧版登记结束 05:00Z 与实际结束 02:00Z 不应相等")
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	if nv.UnitPrice != 180 || !nv.Start.Equal(xtzHandoffUTC) {
		t.Fatalf("新版登记信息异常: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(xtzNewEndUTC) ||
		nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(xtzNewEndUTC) {
		t.Fatalf("新版登记/实际结束都应为 03:00Z: 登记结束=%v 实际结束=%v",
			nv.End, nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q",
			nv.Replaces, nv.SupersededBy)
	}
}

// TestCrossTimezoneTimelinePremises 先钉死测试前提：钟面顺序与实际顺序相反，
// 以及各填写值换算到 UTC 后的实际时刻。这些等式不成立则后续用例没有意义。
func TestCrossTimezoneTimelinePremises(t *testing.T) {
	// 钟面显示：旧版开始是 09 点（+08），新版开始是 02 点（Z），9 > 2；
	// 实际时刻却是旧版 01:00Z 早于新版 02:00Z。
	if xtzOldStart.Hour() != 9 || xtzNewStart.Hour() != 2 {
		t.Fatalf("用例设计前提变化：旧版开始钟点=%d 新版开始钟点=%d，应分别为 9 和 2",
			xtzOldStart.Hour(), xtzNewStart.Hour())
	}
	if !xtzOldStart.Before(xtzNewStart) || !xtzNewStart.After(xtzOldStart) {
		t.Fatalf("实际顺序应只由时刻决定：09:00+08 (01:00Z) 早于 02:00Z")
	}
	// 旧版自身的起止钟面也是 09 点到 05 点，但实际区间是 [01:00Z, 05:00Z)。
	if !xtzOldEnd.After(xtzOldStart) {
		t.Fatalf("旧版结束 05:00Z 实际晚于开始 09:00+08 (01:00Z)")
	}

	eq := func(name string, got, want time.Time) {
		t.Helper()
		if !got.Equal(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
	}
	eq("旧版开始实际时刻", xtzOldStart, xtzOldStartUTC)
	eq("旧版登记结束实际时刻", xtzOldEnd, xtzOldEndUTC)
	eq("新版开始实际时刻", xtzNewStart, xtzHandoffUTC)
	eq("新版结束实际时刻", xtzNewEnd, xtzNewEndUTC)
	eq("失败边界一的结束实际等于新版开始", xtzBadEndEqualStart, xtzHandoffUTC)
	eq("失败边界二的开始实际等于旧版（登记/实际）结束", xtzBoundaryStart, xtzOldEndUTC)
	eq("失败边界二自身的结束时刻", xtzBoundaryEnd, xtzBoundary06UTC)
	if !xtzBoundaryEnd.After(xtzBoundaryStart) {
		t.Fatal("失败边界二请求自身的起止应构成合法区间，错误只可能来自替代关系")
	}

	// 走公开入口常见的另一条路径：从带偏移的 RFC3339 字符串解析，
	// 解析出的时刻必须与 time.Date 构造的填写值代表同一瞬间。
	rfc := func(s string) time.Time {
		tt, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return tt
	}
	eq("旧版开始-RFC3339", rfc("2026-03-01T09:00:00+08:00"), xtzOldStartUTC)
	eq("旧版结束-RFC3339", rfc("2026-03-01T05:00:00Z"), xtzOldEndUTC)
	eq("新版开始-RFC3339", rfc("2026-03-01T02:00:00Z"), xtzHandoffUTC)
	eq("新版结束-RFC3339", rfc("2026-03-01T11:00:00+08:00"), xtzNewEndUTC)
}

// TestCrossTimezoneReplacementRegistrationSucceeds 主线：
// 起止填写值跨时区、钟面顺序相反，旧版与替代它的新版都必须能登记成功。
func TestCrossTimezoneReplacementRegistrationSucceeds(t *testing.T) {
	b := newXtzOldOnlyBook(t)

	// 登记新版前确认前提：旧版尚未被替代、seat-v2 尚未使用。
	assertXtzOldUntruncated(t, b)

	newEnd := xtzNewEnd
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: xtzNewStart, End: &newEnd, Replaces: "seat-v1",
	})
	if err != nil {
		t.Fatalf("开始钟点(02)虽小于旧版开始钟点(09)，但实际 02:00Z 晚于 01:00Z，" +
			"且结束 11:00+08 (03:00Z) 晚于开始，登记应成功")
	}
	assertXtzReplacedState(t, b)
}

// TestCrossTimezoneEffectiveSelection 按实际时刻查询：
// 交接点前选旧版、交接点起选新版、新版 03:00Z 到期后得到 ErrNoEffectiveVersion，
// 不会因为旧版登记结束 05:00Z 尚未到来而恢复旧版。
func TestCrossTimezoneEffectiveSelection(t *testing.T) {
	b := newXtzReplacedBook(t)

	cases := []struct {
		name    string
		instant time.Time
		wantID  string
		wantErr error
	}{
		{"旧版实际开始（含）选旧版", xtzOldStartUTC, "seat-v1", nil},
		{"交接点前1纳秒仍选旧版", xtzHandoffUTC.Add(-time.Nanosecond), "seat-v1", nil},
		{"交接点本身选新版", xtzHandoffUTC, "seat-v2", nil},
		{"新版有效期内选新版", time.Date(2026, 3, 1, 2, 30, 0, 0, time.UTC), "seat-v2", nil},
		{"新版实际结束前1纳秒仍选新版", xtzNewEndUTC.Add(-time.Nanosecond), "seat-v2", nil},
		{"新版实际结束（不含）无生效版本", xtzNewEndUTC, "", ErrNoEffectiveVersion},
		// 04:00Z：旧版登记结束 05:00Z 尚未到来，但旧版实际止于 02:00Z，不能恢复。
		{"两版都失效后旧版不恢复", time.Date(2026, 3, 1, 4, 0, 0, 0, time.UTC), "", ErrNoEffectiveVersion},
		{"旧版登记结束时刻仍无生效版本", xtzOldEndUTC, "", ErrNoEffectiveVersion},
		{"更晚时刻仍无生效版本", xtzBoundary06UTC, "", ErrNoEffectiveVersion},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := b.EffectiveVersionAt("seat", c.instant)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("want %v, got %v (view=%+v)", c.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应返回错误, got %v", err)
			}
			if got.VersionID != c.wantID {
				t.Fatalf("选中 %q, want %q", got.VersionID, c.wantID)
			}
		})
	}
}

// TestCrossTimezoneSameInstantConsistentQuery 同一瞬间换成东八区（及其他偏移）
// 表示，EffectiveVersionAt 的结论必须一致；只核对是否同一瞬间，
// 不要求返回的时间字段统一采用某种时区格式。
func TestCrossTimezoneSameInstantConsistentQuery(t *testing.T) {
	b := newXtzReplacedBook(t)

	west5 := time.FixedZone("UTC-5", -5*3600)
	instants := []time.Time{
		xtzOldStartUTC,
		xtzHandoffUTC.Add(-time.Nanosecond),
		xtzHandoffUTC,
		xtzNewEndUTC.Add(-time.Nanosecond),
		xtzNewEndUTC,
		xtzOldEndUTC,
	}
	for _, instant := range instants {
		// 同一瞬间的多种表示：UTC、东八区、西五区。
		reprs := []time.Time{
			instant.UTC(),
			instant.In(east8),
			instant.In(west5),
		}
		for i := 1; i < len(reprs); i++ {
			if !reprs[0].Equal(reprs[i]) {
				t.Fatalf("测试前提不成立：%v 与 %v 不代表同一瞬间", reprs[0], reprs[i])
			}
		}

		first, errFirst := b.EffectiveVersionAt("seat", reprs[0])
		for r := 1; r < len(reprs); r++ {
			got, err := b.EffectiveVersionAt("seat", reprs[r])
			if !errors.Is(err, errFirst) {
				t.Fatalf("瞬间 %v 用表示 %v 查询: err=%v，与 UTC 表示的 %v 不一致",
					instant, reprs[r], err, errFirst)
			}
			if errFirst == nil && got.VersionID != first.VersionID {
				t.Fatalf("瞬间 %v 用表示 %v 选中 %q，UTC 表示选中 %q",
					instant, reprs[r], got.VersionID, first.VersionID)
			}
		}
	}

	// 题目指定的对照：03:00Z 与东八区 11:00 是同一瞬间，两处都应得到
	// ErrNoEffectiveVersion，不能因表示不同而一个报错、一个恢复旧版。
	_, errUTC := b.EffectiveVersionAt("seat", xtzNewEndUTC)
	_, errEast := b.EffectiveVersionAt("seat", time.Date(2026, 3, 1, 11, 0, 0, 0, east8))
	if !errors.Is(errUTC, ErrNoEffectiveVersion) || !errors.Is(errEast, ErrNoEffectiveVersion) {
		t.Fatalf("03:00Z 与 11:00+08 是同一瞬间，都应无生效版本: %v %v", errUTC, errEast)
	}
}

// TestCrossTimezoneInvalidIntervalEndEqualsStart 失败边界一：
// 新版仍从 02:00Z 开始，结束填 10:00 (+08:00)，两者实际相等，
// 必须返回 ErrInvalidInterval（而不是先报替代关系错误），且整次登记回滚；
// 把结束修正为 11:00 (+08:00)（03:00Z）后，同一新版标识仍能登记成功。
func TestCrossTimezoneInvalidIntervalEndEqualsStart(t *testing.T) {
	b := newXtzOldOnlyBook(t)

	// 先钉死失败前提：10:00+08 与 02:00Z 实际相等。
	if !xtzBadEndEqualStart.Equal(xtzNewStart) {
		t.Fatal("测试前提不成立：失败边界一的结束应恰好等于新版开始")
	}

	badEnd := xtzBadEndEqualStart
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: xtzNewStart, End: &badEnd, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrInvalidInterval) {
		t.Fatalf("跨时区填写导致起止实际相等时应返回 ErrInvalidInterval, got %v", err)
	}

	// 失败不留新版、不占用标识、不截短旧版、不改替代关系。
	assertXtzOldUntruncated(t, b)

	// 修正为合法时间（结束 11:00+08 即 03:00Z），沿用同一标识 seat-v2 登记成功。
	fixedEnd := xtzNewEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: xtzNewStart, End: &fixedEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("失败登记不应占用版本标识，修正为合法跨时区时间后应成功, got %v", err)
	}
	assertXtzReplacedState(t, b)
}

// TestCrossTimezoneIndependentOfMachineLocalZone 把进程的本机默认时区逐个换成
// 不同偏移后重跑整条主线与两个失败边界：账本只按实际时刻判断，
// 结论不能随本机默认时区变化。所有业务时刻都在代码中写死并注入固定时钟，
// 因此同样不依赖运行当天的真实日期。
//
// 本包测试均不使用 t.Parallel，临时替换全局 time.Local 是安全的。
func TestCrossTimezoneIndependentOfMachineLocalZone(t *testing.T) {
	localZones := []*time.Location{
		time.UTC,
		time.FixedZone("UTC+8", 8*3600),
		time.FixedZone("UTC-5", -5*3600),
		time.FixedZone("UTC+5:30", 5*3600+30*60),
	}
	for _, loc := range localZones {
		loc := loc
		t.Run(loc.String(), func(t *testing.T) {
			saved := time.Local
			time.Local = loc
			t.Cleanup(func() { time.Local = saved })

			// 成功主线：登记 + 视图 + 交接点选择 + 到期不恢复。
			b := newXtzReplacedBook(t)
			assertXtzReplacedState(t, b)
			if got, err := b.EffectiveVersionAt(
				"seat", xtzHandoffUTC.Add(-time.Nanosecond)); err != nil || got.VersionID != "seat-v1" {
				t.Fatalf("交接点前应选旧版: %v %+v", err, got)
			}
			if got, err := b.EffectiveVersionAt("seat", xtzHandoffUTC); err != nil || got.VersionID != "seat-v2" {
				t.Fatalf("交接点应选新版: %v %+v", err, got)
			}
			if _, err := b.EffectiveVersionAt("seat", xtzNewEndUTC); !errors.Is(err, ErrNoEffectiveVersion) {
				t.Fatalf("03:00Z 应无生效版本, got %v", err)
			}

			// 失败边界一：起止实际相等 -> ErrInvalidInterval，失败后标识可复用。
			b1 := newXtzOldOnlyBook(t)
			badEnd := xtzBadEndEqualStart
			if err := b1.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: xtzNewStart, End: &badEnd, Replaces: "seat-v1",
			}); !errors.Is(err, ErrInvalidInterval) {
				t.Fatalf("失败边界一应返回 ErrInvalidInterval, got %v", err)
			}
			assertXtzOldUntruncated(t, b1)

			// 失败边界二：替代开始恰等于旧版实际结束 -> ErrInvalidReplacement。
			b2 := newXtzOldOnlyBook(t)
			boundaryEnd := xtzBoundaryEnd
			if err := b2.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: xtzBoundaryStart, End: &boundaryEnd, Replaces: "seat-v1",
			}); !errors.Is(err, ErrInvalidReplacement) {
				t.Fatalf("失败边界二应返回 ErrInvalidReplacement, got %v", err)
			}
			assertXtzOldUntruncated(t, b2)
		})
	}
}

// TestCrossTimezoneInvalidReplacementStartEqualsOldEnd 失败边界二：
// 新版开始改为 13:00 (+08:00)（恰为旧版实际结束 05:00Z）、结束 14:00 (+08:00)，
// 请求自身区间合法，但替代开始必须严格落在旧版当前实际有效区间内，
// 端点相等不允许，必须返回 ErrInvalidReplacement 且整次登记回滚；
// 把开始修正为 02:00Z 后，同一新版标识仍能登记成功。
func TestCrossTimezoneInvalidReplacementStartEqualsOldEnd(t *testing.T) {
	b := newXtzOldOnlyBook(t)

	// 失败前提：13:00+08 恰好等于旧版登记/实际结束 05:00Z；旧版尚未被替代。
	if !xtzBoundaryStart.Equal(xtzOldEnd) {
		t.Fatal("测试前提不成立：失败边界二的开始应恰好等于旧版实际结束")
	}

	boundaryEnd := xtzBoundaryEnd
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: xtzBoundaryStart, End: &boundaryEnd, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrInvalidReplacement) {
		t.Fatalf("替代开始恰好等于旧版实际结束时应返回 ErrInvalidReplacement, got %v", err)
	}

	// 失败不留新版、不占用标识、不截短旧版、不改替代关系。
	assertXtzOldUntruncated(t, b)

	// 修正为合法的交接时刻 02:00Z、结束 11:00+08（03:00Z），沿用同一标识成功。
	fixedEnd := xtzNewEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: xtzNewStart, End: &fixedEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("失败登记不应占用版本标识，修正开始时刻后应成功, got %v", err)
	}
	assertXtzReplacedState(t, b)

	// 成功后再按实际时刻抽查选择结论：交接点前旧版、交接点新版、03:00Z 起无版本。
	if got, err := b.EffectiveVersionAt("seat", xtzHandoffUTC.Add(-time.Nanosecond)); err != nil ||
		got.VersionID != "seat-v1" {
		t.Fatalf("交接点前应选旧版: %v %+v", err, got)
	}
	if got, err := b.EffectiveVersionAt("seat", xtzHandoffUTC); err != nil ||
		got.VersionID != "seat-v2" {
		t.Fatalf("交接点应选新版: %v %+v", err, got)
	}
	if _, err := b.EffectiveVersionAt("seat", xtzNewEndUTC); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("新版到期后应无生效版本, got %v", err)
	}
}
