package tariff

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func ptrTime(t time.Time) *time.Time { return &t }

// EffectiveVersionAt 场景的固定时间线（UTC，结束时刻均不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00
//	新版 seat-v2：单价 180 分，2026-03-10 00:00 起替代旧版，2026-03-20 00:00 结束
//
// 查询时旧版的实际有效区间已被截断到 3 月 10 日：
// 3 月 9 日应选旧版，3 月 10 日应选新版，3 月 20 日两版都不生效且旧版不恢复。
var (
	effV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	effV1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	effV2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	effV2End   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
)

func newEffectiveVersionBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	v1End := effV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: effV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	v2End := effV2End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: effV2Start, End: &v2End, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// 题目示例：3 月 9 日返回旧版，3 月 10 日返回新版，
// 3 月 20 日明确表示当时没有生效版本，不能恢复旧版。
func TestEffectiveVersionAtHandoffExample(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	got, err := b.EffectiveVersionAt("seat", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("3 月 9 日旧版应生效: %v", err)
	}
	if got.VersionID != "seat-v1" || got.UnitPrice != 150 {
		t.Fatalf("3 月 9 日选错版本: %+v", got)
	}

	got, err = b.EffectiveVersionAt("seat", effV2Start)
	if err != nil {
		t.Fatalf("3 月 10 日新版应生效: %v", err)
	}
	if got.VersionID != "seat-v2" || got.UnitPrice != 180 {
		t.Fatalf("3 月 10 日选错版本: %+v", got)
	}

	_, err = b.EffectiveVersionAt("seat", effV2End)
	if !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("3 月 20 日应无生效版本, got %v (view=%+v)", err, got)
	}
}

// 边界与“旧版不恢复”：开始时刻含、结束时刻不含；
// 旧版登记结束（3 月 31 日）即使仍在将来，交接点之后也不能再被选中；
// 新版到期后同样不能回退到旧版；更远的未来仍然没有生效版本。
func TestEffectiveVersionAtBoundariesAndNoRevival(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	cases := []struct {
		name    string
		at      time.Time
		wantID  string
		wantErr error
	}{
		{"首版开始时刻（含）", effV1Start, "seat-v1", nil},
		{"交接点前1纳秒仍是旧版", effV2Start.Add(-time.Nanosecond), "seat-v1", nil},
		{"交接点本身起选新版", effV2Start, "seat-v2", nil},
		{"新版结束前1纳秒仍是新版", effV2End.Add(-time.Nanosecond), "seat-v2", nil},
		{"新版结束时刻（不含）无生效版本", effV2End, "", ErrNoEffectiveVersion},
		// 3 月 25 日：旧版登记结束 3 月 31 日尚未到，但旧版实际区间止于 3 月 10 日。
		{"两版都失效后不回退旧版", time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC), "", ErrNoEffectiveVersion},
		{"旧版登记结束当天仍无生效版本", effV1End, "", ErrNoEffectiveVersion},
		{"更远的未来仍无生效版本", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "", ErrNoEffectiveVersion},
	}
	for _, c := range cases {
		got, err := b.EffectiveVersionAt("seat", c.at)
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("%s: want %v, got %v", c.name, c.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: 不应返回错误, got %v", c.name, err)
		}
		if got.VersionID != c.wantID {
			t.Fatalf("%s: 选中 %q, want %q", c.name, got.VersionID, c.wantID)
		}
	}
}

// 早于首版开始、落在两版之间的空档、晚于最后一版结束，
// 都返回可识别的“该时刻无生效版本”，不拿邻近版本补位。
func TestEffectiveVersionAtBeforeFirstAndGaps(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	// 不涉及替代的两段区间，中间留 3 月 10 日至 3 月 20 日的空档。
	g1End := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	must(RegisterRequest{
		ItemID: "gap", VersionID: "g1", UnitPrice: 100,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), End: &g1End,
	})
	must(RegisterRequest{
		ItemID: "gap", VersionID: "g2", UnitPrice: 200,
		Start: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		End:   ptrTime(time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)),
	})

	none := []struct {
		name string
		at   time.Time
	}{
		{"早于首版开始", time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)},
		{"首版开始前1纳秒", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Add(-time.Nanosecond)},
		{"第一段结束时刻（不含）", time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)},
		{"两版之间的空档", time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)},
		{"第二段开始前1纳秒", time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC).Add(-time.Nanosecond)},
		{"全部到期之后", time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)},
		{"更远的未来", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range none {
		if _, err := b.EffectiveVersionAt("gap", c.at); !errors.Is(err, ErrNoEffectiveVersion) {
			t.Fatalf("%s: want ErrNoEffectiveVersion, got %v", c.name, err)
		}
	}

	// 区间内命中各自版本，边界相接点由后一段取得。
	got, err := b.EffectiveVersionAt("gap", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || got.VersionID != "g1" {
		t.Fatalf("空档场景 g1: %+v (%v)", got, err)
	}
	got, err = b.EffectiveVersionAt("gap", time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC))
	if err != nil || got.VersionID != "g2" {
		t.Fatalf("空档场景 g2 起点: %+v (%v)", got, err)
	}
}

// 从未登记过的费率项沿用现有的费率项不存在错误，
// 与“项存在但该时刻无生效版本”是两种可区分的失败。
func TestEffectiveVersionAtMissingItemVsNoVersion(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	_, err := b.EffectiveVersionAt("never-seen", effV1Start)
	if !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("从未登记的费率项应返回 ErrItemNotFound, got %v", err)
	}
	if errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("费率项不存在不能被误报成该时刻无生效版本: %v", err)
	}

	_, err = b.EffectiveVersionAt("seat", time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("存在的项在空档应返回 ErrNoEffectiveVersion, got %v", err)
	}
	if errors.Is(err, ErrItemNotFound) {
		t.Fatalf("费率项存在不能被误报成不存在: %v", err)
	}
}

// 同一瞬间用不同时区表示，查询结论必须一致。
func TestEffectiveVersionAtSameInstantAnyTimezone(t *testing.T) {
	b := NewBook()
	// 以东八区登记：交接点为东八区 3 月 10 日 00:00（UTC 3 月 9 日 16:00），
	// 新版结束为东八区 3 月 20 日 00:00（UTC 3 月 19 日 16:00）。
	oldStart := time.Date(2026, 3, 1, 0, 0, 0, 0, east8)
	oldEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, east8)
	handoff := time.Date(2026, 3, 10, 0, 0, 0, 0, east8)
	newEnd := time.Date(2026, 3, 20, 0, 0, 0, 0, east8)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: oldStart, End: &oldEnd,
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: handoff, End: &newEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		instant time.Time
		wantID  string
		wantErr error
	}{
		{"交接点前1小时（旧版）", handoff.Add(-time.Hour), "seat-v1", nil},
		{"交接点本身（新版）", handoff, "seat-v2", nil},
		{"新版结束时刻（无生效版本）", newEnd, "", ErrNoEffectiveVersion},
	}
	for _, c := range cases {
		reprs := []time.Time{
			c.instant,
			c.instant.UTC(),
			c.instant.In(time.FixedZone("UTC-5", -5*3600)),
		}
		for r, instant := range reprs {
			got, err := b.EffectiveVersionAt("seat", instant)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("%s 表示%d: want %v, got %v", c.name, r, c.wantErr, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s 表示%d: %v", c.name, r, err)
			}
			if got.VersionID != c.wantID {
				t.Fatalf("%s 表示%d: 选中 %q, want %q", c.name, r, got.VersionID, c.wantID)
			}
		}
	}
}

// 其他费率项中即使有同名版本，也不能参与本项的选择。
func TestEffectiveVersionAtScopedToItem(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	// seat/v1：3 月 1 日至 3 月 31 日；room/v1：3 月 1 日起持续有效，同名标识、单价不同。
	seatEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	must(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), End: &seatEnd,
	})
	must(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 300,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	})

	// 4 月 15 日 seat/v1 已到期，不能借 room 中仍有效的同名 v1 补位。
	if _, err := b.EffectiveVersionAt("seat", time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("seat 空档不能选到另一项的同名版本: %v", err)
	}
	got, err := b.EffectiveVersionAt("room", time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("room 的同名版本应持续有效: %v", err)
	}
	if got.ItemID != "room" || got.VersionID != "v1" || got.UnitPrice != 300 {
		t.Fatalf("room 查询串项: %+v", got)
	}

	// seat 登记 3 月 10 日起持续有效的 v2 替代本项 v1；两项在未来各选各的版本。
	must(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180,
		Start: time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), Replaces: "v1",
	})
	got, err = b.EffectiveVersionAt("seat", time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || got.VersionID != "v2" || got.UnitPrice != 180 {
		t.Fatalf("seat 未来应选本项 v2: %+v (%v)", got, err)
	}
	got, err = b.EffectiveVersionAt("room", time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || got.VersionID != "v1" || got.UnitPrice != 300 {
		t.Fatalf("room 不能被 seat 的替代影响: %+v (%v)", got, err)
	}
}

// 连续替代：甲→乙→丙，每段按各自实际有效区间选择，丙到期后没有版本，甲、乙都不恢复。
func TestEffectiveVersionAtChainedReplacement(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{
		ItemID: "chain", VersionID: "c1", UnitPrice: 100,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		End:   ptrTime(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)),
	})
	must(RegisterRequest{
		ItemID: "chain", VersionID: "c2", UnitPrice: 200,
		Start:    time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
		End:      ptrTime(time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)),
		Replaces: "c1",
	})
	must(RegisterRequest{
		ItemID: "chain", VersionID: "c3", UnitPrice: 300,
		Start:    time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		End:      ptrTime(time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)),
		Replaces: "c2",
	})

	cases := []struct {
		name   string
		at     time.Time
		wantID string
	}{
		{"3 月 9 日选甲版", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "c1"},
		{"3 月 10 日交接点选乙版", time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), "c2"},
		{"3 月 14 日仍是乙版", time.Date(2026, 3, 14, 23, 0, 0, 0, time.UTC), "c2"},
		{"3 月 15 日交接点选丙版", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), "c3"},
		{"3 月 17 日仍是丙版", time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC), "c3"},
	}
	for _, c := range cases {
		got, err := b.EffectiveVersionAt("chain", c.at)
		if err != nil || got.VersionID != c.wantID {
			t.Fatalf("%s: got %q (%v), want %q", c.name, got.VersionID, err, c.wantID)
		}
	}
	for _, at := range []time.Time{
		time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	} {
		if _, err := b.EffectiveVersionAt("chain", at); !errors.Is(err, ErrNoEffectiveVersion) {
			t.Fatalf("%s: 丙版到期后甲乙都不应恢复, got %v", at.Format("01-02"), err)
		}
	}
}

// 不填结束时间、也未被替代的版本对任意未来瞬间持续有效。
func TestEffectiveVersionAtOpenEnded(t *testing.T) {
	b := NewBook()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "open", VersionID: "v1", UnitPrice: 7,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := b.EffectiveVersionAt("open", time.Date(2099, 12, 31, 23, 59, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("持续有效的版本未来也应命中: %v", err)
	}
	if got.VersionID != "v1" || got.EffectiveEnd != nil || got.End != nil {
		t.Fatalf("持续有效版本视图异常: %+v", got)
	}
}

// 查询以调用方给定的时刻为准，可以针对过去或未来，与账本时钟无关：
// 时钟停在任何位置都不改变查询结论。
func TestEffectiveVersionAtIgnoresLedgerClock(t *testing.T) {
	b, setNow := newEffectiveVersionBook(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	// 时钟停在 6 月（所有版本都已成为历史），查过去仍能拿到当时生效的版本。
	for _, c := range []struct {
		at     time.Time
		wantID string
	}{
		{time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "seat-v1"},
		{effV2Start, "seat-v2"},
	} {
		got, err := b.EffectiveVersionAt("seat", c.at)
		if err != nil || got.VersionID != c.wantID {
			t.Fatalf("时钟在未来不影响查过去: %s got %q (%v)", c.at, got.VersionID, err)
		}
	}

	// 把时钟拨回版本登记之前，查询结论也不变。
	setNow(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	got, err := b.EffectiveVersionAt("seat", time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	if err != nil || got.VersionID != "seat-v2" {
		t.Fatalf("时钟在过去不影响查未来: %+v (%v)", got, err)
	}
}

// 后来补登过去生效的替代版本后，历史时刻按账本当前登记的实际区间回答：
// 选择依据是“查询时”已经登记的实际有效区间，而不是该时刻当时已知的版本。
func TestEffectiveVersionAtReflectsRetroactiveRegistration(t *testing.T) {
	now, _ := fixedClock(time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC))
	b := NewBook(WithClock(now))
	oldEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: effV1Start, End: &oldEnd,
	}); err != nil {
		t.Fatal(err)
	}
	// 补登前：3 月 15 日只有旧版。
	got, err := b.EffectiveVersionAt("seat", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || got.VersionID != "seat-v1" {
		t.Fatalf("补登前 3 月 15 日应选旧版: %+v (%v)", got, err)
	}

	// 在 3 月 15 日补登 3 月 10 日起生效的替代版本。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: effV2Start, Replaces: "seat-v1",
	}); err != nil {
		t.Fatal(err)
	}
	// 补登后再查同一历史时刻：旧版实际区间已被截断到 3 月 10 日，应选新版。
	got, err = b.EffectiveVersionAt("seat", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || got.VersionID != "seat-v2" {
		t.Fatalf("补登后历史时刻应按当前登记的实际区间选新版: %+v (%v)", got, err)
	}
	got, err = b.EffectiveVersionAt("seat", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	if err != nil || got.VersionID != "seat-v1" {
		t.Fatalf("交接点之前仍应选旧版: %+v (%v)", got, err)
	}
}

// 返回结果沿用版本视图的全部信息：版本标识、整数分单价、登记起止、
// 实际有效区间与双向替代关系，供调用方核对选择依据。
func TestEffectiveVersionAtViewFields(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	old, err := b.EffectiveVersionAt("seat", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if old.ItemID != "seat" || old.VersionID != "seat-v1" || old.UnitPrice != 150 {
		t.Fatalf("旧版标识/单价异常: %+v", old)
	}
	if !old.Start.Equal(effV1Start) || !old.EffectiveStart.Equal(effV1Start) {
		t.Fatalf("旧版开始时刻异常: %+v", old)
	}
	if old.End == nil || !old.End.Equal(effV1End) {
		t.Fatalf("旧版应保留登记结束 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(effV2Start) {
		t.Fatalf("旧版实际结束应显示交接点 3 月 10 日: %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q", old.Replaces, old.SupersededBy)
	}

	nv, err := b.EffectiveVersionAt("seat", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if nv.ItemID != "seat" || nv.VersionID != "seat-v2" || nv.UnitPrice != 180 {
		t.Fatalf("新版标识/单价异常: %+v", nv)
	}
	if !nv.Start.Equal(effV2Start) || !nv.EffectiveStart.Equal(effV2Start) {
		t.Fatalf("新版开始时刻异常: %+v", nv)
	}
	if nv.End == nil || !nv.End.Equal(effV2End) {
		t.Fatalf("新版登记结束异常: %v", nv.End)
	}
	if nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(effV2End) {
		t.Fatalf("新版实际结束异常: %v", nv.EffectiveEnd)
	}
	if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
		t.Fatalf("新版替代关系异常: Replaces=%q SupersededBy=%q", nv.Replaces, nv.SupersededBy)
	}
}

// 查询结果是独立副本：修改返回的结束时间不能改变账本、其他查询结果或后续报价。
func TestEffectiveVersionAtViewMutationIsolated(t *testing.T) {
	b, setNow := newEffectiveVersionBook(t, effV1Start)

	old, err := b.EffectiveVersionAt("seat", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// 调用方为展示目的改写这份结果中的两种结束时间。
	*old.End = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	*old.EffectiveEnd = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	again, err := b.EffectiveVersionAt("seat", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if again.End == nil || !again.End.Equal(effV1End) {
		t.Fatalf("登记结束被查询结果的修改改写: %v", again.End)
	}
	if again.EffectiveEnd == nil || !again.EffectiveEnd.Equal(effV2Start) {
		t.Fatalf("实际结束被查询结果的修改改写: %v", again.EffectiveEnd)
	}

	// 两次查询的同名指针字段也必须互相独立。
	if old.End == again.End || old.EffectiveEnd == again.EffectiveEnd {
		t.Fatal("两次生效版本查询共享了结束时间指针")
	}
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.VersionID == "seat-v1" {
			if v.End == again.End || v.EffectiveEnd == again.EffectiveEnd {
				t.Fatal("生效版本查询与版本列表查询共享了结束时间指针")
			}
		}
	}

	// 报价仍按账本真实区间处理：交接点起旧版失效、新版有效。
	setNow(effV2Start)
	out, _ := b.Quote(QuoteRequest{RequestID: "q-old", ItemID: "seat", VersionID: "seat-v1", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("修改查询结果不能影响后续报价依据: %+v", out)
	}
	out, _ = b.Quote(QuoteRequest{RequestID: "q-new", ItemID: "seat", VersionID: "seat-v2", Quantity: 1})
	if !out.Confirmed {
		t.Fatalf("新版在交接点应可报价: %+v", out)
	}
}

// 查询是只读操作：不受理报价、不占用请求标识，也不产生可被 Lookup 的记录。
func TestEffectiveVersionAtDoesNotConsumeRequestID(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	// 大量查询前后，未使用过的标识都查不到报价记录。
	for i := 0; i < 8; i++ {
		if _, err := b.EffectiveVersionAt("seat", effV1Start.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatalf("查询 %d 意外失败: %v", i, err)
		}
	}
	if _, err := b.Lookup("quote-after-queries"); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("只读查询不应产生报价记录: %v", err)
	}

	// 该标识仍是全新的：首次报价正常受理并被保存。
	out, err := b.Quote(QuoteRequest{
		RequestID: "quote-after-queries", ItemID: "seat", VersionID: "seat-v1", Quantity: 2,
	})
	if err != nil || !out.Confirmed || out.Total != 300 {
		t.Fatalf("查询不应占用请求标识或影响首次受理: %+v (%v)", out, err)
	}
	got, err := b.Lookup("quote-after-queries")
	if err != nil || got != out {
		t.Fatalf("首次受理结果应原样可查: %+v (%v)", got, err)
	}

	// 查询“无生效版本”的时刻同样不留任何报价痕迹。
	if _, err := b.EffectiveVersionAt("seat", effV2End); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("前提：3 月 20 日应无生效版本, got %v", err)
	}
	views, err := b.ItemVersions("seat")
	if err != nil || len(views) != 2 {
		t.Fatalf("查询不应改动版本集合: %d 个版本 (%v)", len(views), err)
	}
}

// 并发只读查询应始终给出一致结论。
func TestEffectiveVersionAtConcurrentReads(t *testing.T) {
	b, _ := newEffectiveVersionBook(t, effV1Start)

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := b.EffectiveVersionAt("seat", effV2Start)
			if err != nil {
				errs <- fmt.Errorf("查询 %d: %w", i, err)
				return
			}
			if got.VersionID != "seat-v2" || got.UnitPrice != 180 {
				errs <- fmt.Errorf("查询 %d 结果异常: %+v", i, got)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
