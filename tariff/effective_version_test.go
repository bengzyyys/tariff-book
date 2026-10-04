package tariff

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// EffectiveVersion 按费率项和指定时刻查询生效版本。
//
// 本节固定时间线（UTC，结束时刻不含）：
//
//	旧版 v1：单价 150 分，2026-03-01 起，登记结束 2026-03-31
//	新版 v2：单价 180 分，2026-03-10 起替代 v1
var (
	effV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	effV1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	effV2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
)

// registerEffV1 登记旧版 v1：[2026-03-01, 2026-03-31)。
func registerEffV1(t *testing.T, b *Book) {
	t.Helper()
	end := effV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: effV1Start, End: &end,
	}); err != nil {
		t.Fatalf("register v1: %v", err)
	}
}

// registerEffV2 登记新版 v2：2026-03-10 起替代 v1；end 为 nil 时持续有效。
func registerEffV2(t *testing.T, b *Book, end *time.Time) {
	t.Helper()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180,
		Start: effV2Start, End: end, Replaces: "v1",
	}); err != nil {
		t.Fatalf("register v2: %v", err)
	}
}

// mustEffVersion 断言指定时刻查询成功并返回期望版本。
func mustEffVersion(t *testing.T, b *Book, itemID string, at time.Time, wantVersion string) VersionView {
	t.Helper()
	v, err := b.EffectiveVersion(itemID, at)
	if err != nil {
		t.Fatalf("EffectiveVersion(%q, %s): 应命中 %q，got err %v",
			itemID, at.Format(time.RFC3339Nano), wantVersion, err)
	}
	if v.ItemID != itemID || v.VersionID != wantVersion {
		t.Fatalf("EffectiveVersion(%q, %s) = %q/%q, want 版本 %q",
			itemID, at.Format(time.RFC3339Nano), v.ItemID, v.VersionID, wantVersion)
	}
	return v
}

// mustEffNoVersion 断言指定时刻查询返回 wantErr（费率项存在但无生效版本），
// 且不是费率项不存在错误。
func mustEffNoVersion(t *testing.T, b *Book, itemID string, at time.Time, wantErr error) {
	t.Helper()
	v, err := b.EffectiveVersion(itemID, at)
	if !errors.Is(err, wantErr) {
		t.Fatalf("EffectiveVersion(%q, %s): want err %v, got %v (view %+v)",
			itemID, at.Format(time.RFC3339Nano), wantErr, err, v)
	}
	if err == nil {
		t.Fatalf("EffectiveVersion(%q, %s) 应返回错误，却返回视图 %+v",
			itemID, at.Format(time.RFC3339Nano), v)
	}
}

// 任务给出的时间线：旧版 3 月 1 日起登记到 3 月 31 日，
// 新版 3 月 10 日起替代旧版且持续有效。
// 3 月 9 日返回旧版，3 月 10 日返回新版；开始时刻含、结束时刻不含。
func TestEffectiveVersionAcrossOpenEndedHandoff(t *testing.T) {
	b := NewBook()
	registerEffV1(t, b)
	registerEffV2(t, b, nil)

	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"首版开始时刻含", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "v1"},
		{"交接前一纳秒仍是旧版", effV2Start.Add(-time.Nanosecond), "v1"},
		{"3月9日是旧版", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "v1"},
		{"交接时刻起是新版（开始含）", effV2Start, "v2"},
		{"交接后一纳秒是新版", effV2Start.Add(time.Nanosecond), "v2"},
		{"3月中下旬是新版", time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC), "v2"},
		{"旧版登记结束日已是新版", effV1End, "v2"},
		{"遥远的未来仍是持续有效的新版", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), "v2"},
	}
	for _, c := range cases {
		mustEffVersion(t, b, "seat", c.at, c.want)
	}

	// 早于首版开始：明确的无生效版本，不拿邻近版本补位。
	mustEffNoVersion(t, b, "seat", effV1Start.Add(-time.Nanosecond), ErrNoEffectiveVersion)
	mustEffNoVersion(t, b, "seat", time.Date(2026, 2, 28, 23, 59, 59, 0, time.UTC), ErrNoEffectiveVersion)
}

// 新版 3 月 20 日到期后，3 月 20 日当天明确无生效版本，不能恢复登记结束尚在将来的旧版。
func TestEffectiveVersionExpiringNewVersionLeavesGapNotRevive(t *testing.T) {
	b := NewBook()
	registerEffV1(t, b)
	v2End := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	registerEffV2(t, b, &v2End)

	mustEffVersion(t, b, "seat", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "v1")
	mustEffVersion(t, b, "seat", effV2Start, "v2")
	mustEffVersion(t, b, "seat", v2End.Add(-time.Nanosecond), "v2")

	// 任务给定：3 月 20 日明确表示当时没有生效版本。
	mustEffNoVersion(t, b, "seat", v2End, ErrNoEffectiveVersion)
	// 新版本到期之后也不能回退到旧版——旧版实际止于 3 月 10 日。
	mustEffNoVersion(t, b, "seat", v2End.Add(time.Nanosecond), ErrNoEffectiveVersion)
	mustEffNoVersion(t, b, "seat", time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC), ErrNoEffectiveVersion)
	// 旧版登记结束（3 月 31 日）当天及之后，旧版同样不能复活。
	mustEffNoVersion(t, b, "seat", effV1End, ErrNoEffectiveVersion)
	mustEffNoVersion(t, b, "seat", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), ErrNoEffectiveVersion)

	// “无生效版本”必须能与“费率项不存在”区分开。
	_, err := b.EffectiveVersion("seat", v2End)
	if errors.Is(err, ErrItemNotFound) {
		t.Fatalf("存在的费率项空档期不能报 ErrItemNotFound: %v", err)
	}
}

// 早于首版开始、两版之间的空档：费率项存在，返回可识别的无生效版本错误，
// 不用邻近版本补位；从未登记过的费率项沿用费率项不存在错误。
func TestEffectiveVersionBeforeFirstAndBetweenVersions(t *testing.T) {
	b := NewBook()
	// 两段互不相邻的区间，中间 3 月 10 日至 3 月 20 日是空档。
	v1End := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), End: &v1End,
	}); err != nil {
		t.Fatal(err)
	}
	v2End := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180,
		Start: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), End: &v2End,
	}); err != nil {
		t.Fatal(err)
	}

	// 早于首版开始
	mustEffNoVersion(t, b, "seat", time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), ErrNoEffectiveVersion)
	// v1 区间内
	mustEffVersion(t, b, "seat", time.Date(2026, 3, 9, 23, 0, 0, 0, time.UTC), "v1")
	// v1 结束时刻（不含）→ 空档开始
	mustEffNoVersion(t, b, "seat", v1End, ErrNoEffectiveVersion)
	mustEffNoVersion(t, b, "seat", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), ErrNoEffectiveVersion)
	// v2 开始时刻（含）
	mustEffVersion(t, b, "seat", time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), "v2")
	// v2 结束之后
	mustEffNoVersion(t, b, "seat", v2End, ErrNoEffectiveVersion)

	// 零值时间早于任何版本，也属于无生效版本，而不是其他错误。
	mustEffNoVersion(t, b, "seat", time.Time{}, ErrNoEffectiveVersion)

	// 从未登记过的费率项：沿用现有的费率项不存在错误，两者可区分。
	mustEffNoVersion(t, b, "never-seen", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), ErrItemNotFound)

	_, gapErr := b.EffectiveVersion("seat", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	_, missingErr := b.EffectiveVersion("never-seen", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	if gapErr == missingErr || errors.Is(gapErr, ErrItemNotFound) || errors.Is(missingErr, ErrNoEffectiveVersion) {
		t.Fatalf("两种失败必须可区分: 空档=%v 不存在=%v", gapErr, missingErr)
	}

	// 空费率项标识同样视为不存在。
	mustEffNoVersion(t, b, "", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), ErrItemNotFound)
}

// 同一瞬间用不同时区表示，查询结论必须一致。
func TestEffectiveVersionSameInstantAnyTimezone(t *testing.T) {
	b := NewBook()
	registerEffV1(t, b)
	registerEffV2(t, b, nil)

	east := time.FixedZone("UTC+8", 8*3600)
	cases := []struct {
		name string
		want string
	}{
		{"交接前一纳秒", "v1"},
		{"交接时刻", "v2"},
		{"交接后一纳秒", "v2"},
	}
	instants := []time.Time{
		effV2Start.Add(-time.Nanosecond),
		effV2Start,
		effV2Start.Add(time.Nanosecond),
	}
	for i, instant := range instants {
		reprs := []time.Time{
			instant,          // UTC
			instant.In(east), // 东八区表示
			instant.UTC(),    // 显式 UTC
			instant.In(time.FixedZone("UTC-5", -5*3600)), // 负偏移表示
		}
		for r, repr := range reprs {
			if !repr.Equal(instant) {
				t.Fatalf("测试前提不成立：表示不代表同一时刻")
			}
			got := mustEffVersion(t, b, "seat", repr, cases[i].want)
			// 命中版本后，视图内时间边界按实际时刻比较，与返回的时区表示无关。
			if !got.EffectiveStart.Equal(effV1Start) && !got.EffectiveStart.Equal(effV2Start) {
				t.Fatalf("case %s 表示%d: 异常的 EffectiveStart %v", cases[i].name, r, got.EffectiveStart)
			}
		}
	}

	// 无生效版本的结论也与时区表示无关。
	eastView := time.Date(2026, 2, 28, 8, 0, 0, 0, east) // 即 UTC 3 月 1 日 00:00 之前
	if !eastView.Before(effV1Start) {
		t.Fatal("测试前提不成立")
	}
	mustEffNoVersion(t, b, "seat", time.Date(2026, 2, 28, 7, 59, 59, int(time.Second-time.Nanosecond), east), ErrNoEffectiveVersion)
	mustEffNoVersion(t, b, "seat", time.Date(2026, 2, 28, 23, 59, 59, int(time.Second-time.Nanosecond), time.UTC), ErrNoEffectiveVersion)
}

// 其他费率项中即使有同名版本，也不能参与本项的选择。
func TestEffectiveVersionScopedToRequestedItem(t *testing.T) {
	b := NewBook()

	// seat/v1 与 room/v1 同名、同期；seat 在 3 月 10 日被 v2 替代。
	registerEffV1(t, b)
	registerEffV2(t, b, nil)
	roomEnd := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 300,
		Start: time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC), End: &roomEnd,
	}); err != nil {
		t.Fatal(err)
	}

	midMarch := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	seatView := mustEffVersion(t, b, "seat", midMarch, "v2")
	if seatView.ItemID != "seat" || seatView.UnitPrice != 180 || seatView.Replaces != "v1" {
		t.Fatalf("seat 应选自己的 v2: %+v", seatView)
	}
	// room 的同名 v1 不参与 seat 的选择。
	roomView := mustEffVersion(t, b, "room", midMarch, "v1")
	if roomView.ItemID != "room" || roomView.UnitPrice != 300 || roomView.SupersededBy != "" {
		t.Fatalf("room 应选自己的 v1，不受 seat 交接影响: %+v", roomView)
	}

	// 3 月 5 日：两项都由各自的 v1 生效，返回的费率项标识不能串。
	earlyMarch := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	s := mustEffVersion(t, b, "seat", earlyMarch, "v1")
	r := mustEffVersion(t, b, "room", earlyMarch, "v1")
	if s.ItemID != "seat" || s.UnitPrice != 150 || r.ItemID != "room" || r.UnitPrice != 300 {
		t.Fatalf("同名版本跨项串选: seat=%+v room=%+v", s, r)
	}

	// seat 在交接后查不到 v1，不能借 room 的同名 v1 补位。
	old := mustEffVersion(t, b, "seat", effV2Start.Add(-time.Nanosecond), "v1")
	if old.ItemID != "seat" {
		t.Fatalf("交接前 seat 命中的应是本项 v1: %+v", old)
	}

	// 根本不存在的费率项仍是“不存在”，即便多项里都有覆盖该时刻的版本。
	mustEffNoVersion(t, b, "hall", midMarch, ErrItemNotFound)
}

// 查询结果沿用现有版本视图的全部信息：版本标识、整数分单价、
// 登记起止时间、实际有效区间及双向替代关系，可供核对选择依据。
func TestEffectiveVersionViewContents(t *testing.T) {
	b := NewBook()
	registerEffV1(t, b)
	registerEffV2(t, b, nil)

	// 交接前命中旧版：实际有效结束显示交接点，而非登记结束 3 月 31 日。
	old := mustEffVersion(t, b, "seat", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "v1")
	if old.ItemID != "seat" || old.VersionID != "v1" || old.UnitPrice != 150 {
		t.Fatalf("旧版标识/单价异常: %+v", old)
	}
	if !old.Start.Equal(effV1Start) || !old.EffectiveStart.Equal(effV1Start) {
		t.Fatalf("旧版开始时刻异常: %+v", old)
	}
	if old.End == nil || !old.End.Equal(effV1End) {
		t.Fatalf("旧版应保留登记结束 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(effV2Start) {
		t.Fatalf("旧版实际有效结束应为交接点 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}
	// 查询时刻确实落在返回视图的实际有效区间内，可据此核对选择依据。
	if old.EffectiveStart.After(time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)) ||
		!time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC).Before(*old.EffectiveEnd) {
		t.Fatalf("查询时刻不在返回的实际有效区间内: %+v", old)
	}

	// 交接后命中新版：无结束时间、保留替代来源。
	nv := mustEffVersion(t, b, "seat", effV2Start, "v2")
	if nv.UnitPrice != 180 || !nv.Start.Equal(effV2Start) || !nv.EffectiveStart.Equal(effV2Start) {
		t.Fatalf("新版开始/单价异常: %+v", nv)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("持续有效的新版两种结束都应为 nil: %+v", nv)
	}
	if nv.Replaces != "v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q", nv.Replaces, nv.SupersededBy)
	}
}

// 连续替代：甲→乙→丙各自的区间边界都按实际有效期选择。
func TestEffectiveVersionChainedReplacement(t *testing.T) {
	b, _ := newChainedBook(t, chainV1Start)

	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"首版之前", time.Date(2026, 2, 28, 0, 0, 0, 0, east8), ""},
		{"甲版开始时刻含", chainV1Start, "seat-v1"},
		{"甲版期间", time.Date(2026, 3, 9, 12, 0, 0, 0, east8), "seat-v1"},
		{"第一次交接时刻选乙版", chainV2Start, "seat-v2"},
		{"乙版期间", time.Date(2026, 3, 19, 0, 0, 0, 0, east8), "seat-v2"},
		{"第二次交接时刻选丙版", chainV3Start, "seat-v3"},
		{"丙版最后一刻", chainV3End.Add(-time.Nanosecond), "seat-v3"},
		{"丙版结束时刻无生效版本", chainV3End, ""},
		{"丙版结束之后无版本可回退", time.Date(2026, 4, 1, 0, 0, 0, 0, east8), ""},
		{"甲版登记结束日也不能复活甲版", chainV1End, ""},
	}
	for _, c := range cases {
		if c.want == "" {
			mustEffNoVersion(t, b, "seat", c.at, ErrNoEffectiveVersion)
		} else {
			mustEffVersion(t, b, "seat", c.at, c.want)
		}
	}

	// 选择只看实际有效期，不看登记结束：3 月 15 日甲版登记结束（3 月 31 日）尚在将来，
	// 但它早已被乙版截断，不能被选中。
	v := mustEffVersion(t, b, "seat", time.Date(2026, 3, 15, 0, 0, 0, 0, east8), "seat-v2")
	if v.Replaces != "seat-v1" || v.SupersededBy != "seat-v3" {
		t.Fatalf("3 月 15 日应选乙版并保留双向替代关系: %+v", v)
	}
}

// 查询只读版本信息：不受理报价、不占用请求标识，结果只由调用方给定的时刻决定，
// 与账本时钟无关；已保存的确认价与拒绝原因保持原值。
func TestEffectiveVersionIsReadOnlyAndClockIndependent(t *testing.T) {
	// 时钟固定在 6 月 1 日：晚于所有登记边界，但查询结论不能受它影响。
	now, setNow := fixedClock(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	b := NewBook(WithClock(now))
	v1End := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: effV1Start, End: &v1End,
	}); err != nil {
		t.Fatal(err)
	}
	v2End := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	registerEffV2(t, b, &v2End)

	// 时钟指向 6 月，但显式查询 3 月 9 日仍返回旧版、3 月 10 日返回新版、
	// 3 月 20 日返回无生效版本——不能只返回“当前”生效的版本。
	mustEffVersion(t, b, "seat", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "v1")
	mustEffVersion(t, b, "seat", effV2Start, "v2")
	mustEffNoVersion(t, b, "seat", v2End, ErrNoEffectiveVersion)
	// 显式查询未来：把时钟留在 6 月 1 日，查 2030 年仍是“无生效版本”。
	mustEffNoVersion(t, b, "seat", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), ErrNoEffectiveVersion)

	// 查询不产生任何报价记录：任意请求标识都查不到受理结果。
	if _, err := b.Lookup(""); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("查询不应保存报价记录: %v", err)
	}

	// 先保存一笔确认价和一笔拒绝，再反复查询，二者必须原样保留。
	setNow(time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	confirmed, err := b.Quote(QuoteRequest{
		RequestID: "keep-confirm", ItemID: "seat", VersionID: "v1", Quantity: 4,
	})
	if err != nil || !confirmed.Confirmed || confirmed.Total != 600 {
		t.Fatalf("准备确认价失败: %v %+v", err, confirmed)
	}
	rejected, err := b.Quote(QuoteRequest{
		RequestID: "keep-reject", ItemID: "seat", VersionID: "v1", Quantity: 0,
	})
	if err != nil || rejected.Confirmed || rejected.Reason != ReasonInvalidQuantity {
		t.Fatalf("准备拒绝结果失败: %v %+v", err, rejected)
	}
	for _, ts := range []time.Time{
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		effV2Start,
		v2End,
		time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		b.EffectiveVersion("seat", ts)
	}
	got, err := b.Lookup("keep-confirm")
	if err != nil || got != confirmed {
		t.Fatalf("查询改写了已保存的确认价: %+v vs %+v (%v)", got, confirmed, err)
	}
	got, err = b.Lookup("keep-reject")
	if err != nil || got != rejected {
		t.Fatalf("查询改写了已保存的拒绝原因: %+v vs %+v (%v)", got, rejected, err)
	}

	// 查询同样不占用请求标识：随后使用任意新标识报价都能正常受理。
	setNow(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	out, err := b.Quote(QuoteRequest{
		RequestID: "after-queries", ItemID: "seat", VersionID: "v2", Quantity: 2,
	})
	if err != nil {
		t.Fatalf("查询不应占用请求标识: %v", err)
	}
	if !out.Confirmed || out.Total != 360 {
		t.Fatalf("查询后报价异常: %+v", out)
	}
}

// 返回的版本视图与账本、其他查询结果相互独立：
// 调用方修改其中的结束时间，不能改变账本或影响后续报价。
func TestEffectiveVersionViewIndependent(t *testing.T) {
	now, setNow := fixedClock(effV1Start)
	b := NewBook(WithClock(now))
	registerEffV1(t, b)
	v2End := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	registerEffV2(t, b, &v2End)

	first := mustEffVersion(t, b, "seat", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "v1")
	// 登记结束与实际结束在视图内也是各自独立的副本。
	if first.End == first.EffectiveEnd {
		t.Fatal("登记结束与实际有效结束共享同一指针")
	}

	// 调用方为展示目的改写这份结果。
	*first.End = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	*first.EffectiveEnd = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	first.Start = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	// 再次查询仍是账本中的真实边界。
	second := mustEffVersion(t, b, "seat", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "v1")
	if second.End == nil || !second.End.Equal(effV1End) {
		t.Fatalf("账本登记结束被返回视图的改写改变: %v", second.End)
	}
	if second.EffectiveEnd == nil || !second.EffectiveEnd.Equal(effV2Start) {
		t.Fatalf("账本实际有效结束被返回视图的改写改变: %v", second.EffectiveEnd)
	}
	if !second.Start.Equal(effV1Start) {
		t.Fatalf("账本开始时刻被返回视图的改写改变: %v", second.Start)
	}
	// 两次查询不共享指针。
	if first.End == second.End || first.EffectiveEnd == second.EffectiveEnd {
		t.Fatal("两次查询共享了结束时间指针")
	}

	// ItemVersions 看到的账本边界也不变。
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		switch v.VersionID {
		case "v1":
			if v.End == nil || !v.End.Equal(effV1End) || v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(effV2Start) {
				t.Fatalf("ItemVersions 边界被改写: %+v", v)
			}
		case "v2":
			if v.End == nil || !v.End.Equal(v2End) || v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(v2End) {
				t.Fatalf("v2 边界被改写: %+v", v)
			}
		}
	}

	// 后续报价仍按账本真实区间处理：交接点起旧版失效、新版生效，到期后无版可用。
	setNow(effV2Start)
	out, _ := b.Quote(QuoteRequest{RequestID: "q-old", ItemID: "seat", VersionID: "v1", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("旧版必须在交接点失效，视图改写不能影响报价: %+v", out)
	}
	out, _ = b.Quote(QuoteRequest{RequestID: "q-new", ItemID: "seat", VersionID: "v2", Quantity: 1})
	if !out.Confirmed {
		t.Fatalf("新版应在交接点生效: %+v", out)
	}
	setNow(v2End)
	out, _ = b.Quote(QuoteRequest{RequestID: "q-new-expired", ItemID: "seat", VersionID: "v2", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("新版到期后应拒绝: %+v", out)
	}
}

// 零单价版本同样可以被选中，选择逻辑与金额无关。
func TestEffectiveVersionZeroUnitPrice(t *testing.T) {
	b := NewBook()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "free", UnitPrice: 0,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	v := mustEffVersion(t, b, "seat", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "free")
	if v.UnitPrice != 0 {
		t.Fatalf("零单价版本: %+v", v)
	}
}

// 并发只读查询应当安全，且结论一致。
func TestEffectiveVersionConcurrentReads(t *testing.T) {
	b := NewBook()
	registerEffV1(t, b)
	registerEffV2(t, b, nil)

	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var at time.Time
			var want string
			if i%2 == 0 {
				at, want = time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "v1"
			} else {
				at, want = effV2Start, "v2"
			}
			v, err := b.EffectiveVersion("seat", at)
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = v.VersionID
			if v.VersionID != want {
				errs[i] = errMismatch(v.VersionID, want)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发查询 %d: %v", i, err)
		}
	}
}

// errMismatch 构造一个简单的版本不匹配错误，避免在 goroutine 中直接调用 t.Fatalf。
func errMismatch(got, want string) error {
	return errors.New("effective version mismatch: got " + got + ", want " + want)
}
