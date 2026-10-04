package tariff

import (
	"errors"
	"testing"
	"time"
)

// 替代版本登记回归保障所用的固定时间线（全部为 UTC 零点，结束时刻不含）：
//
//	旧版本 seat-v1：单价 150 分，2026-03-01 起，登记结束 2026-03-31
//	新版本 seat-v2：单价 180 分，拟从 2026-03-10 起替代 seat-v1
//	后续版本 seat-v3：单价 200 分，2026-04-01 起持续有效，与 v1 没有替代关系
//
// v2 的交接点本身合法（落在 v1 当前有效区间内），Replaces 只能截短 v1，
// 不能让 v2 占用同项其他版本 v3 的有效时间。所有时刻在代码中写死并注入固定时钟，
// 结论不依赖运行当天的真实日期或时间流逝。
var (
	rrOldStart    = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rrOldEnd      = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	rrHandoff     = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	rrFollowStart = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
)

// newReplacementBaseBook 准备只登记了 v1、v3 两个版本的账本：
// v1 为 3 月内有效的旧版本，v3 从 4 月 1 日起持续有效，二者无替代关系。
func newReplacementBaseBook(t *testing.T) *Book {
	t.Helper()
	now, _ := fixedClock(rrOldStart)
	b := NewBook(WithClock(now))
	oldEnd := rrOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: rrOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: rrFollowStart,
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}
	return b
}

// assertReplacementBaseUnchanged 校验失败登记整次回滚：
// 查询仍只有 v1、v3，v1 未被提前截断、没有失败版本留下的替代关系，
// v3 继续保持原来的持续有效区间。
func assertReplacementBaseUnchanged(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("失败登记后应仍只有两个版本，got %d: %+v", len(views), views)
	}
	old, follow := views[0], views[1]
	if old.VersionID != "seat-v1" || follow.VersionID != "seat-v3" {
		t.Fatalf("失败登记后版本集合/顺序异常: %q, %q", old.VersionID, follow.VersionID)
	}

	// 旧版本：登记结束与实际有效结束都仍是 3 月 31 日，不能止于 3 月 10 日。
	if old.UnitPrice != 150 {
		t.Fatalf("旧版本单价被改写: %d", old.UnitPrice)
	}
	if old.End == nil || !old.End.Equal(rrOldEnd) {
		t.Fatalf("旧版本登记结束被改写: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(rrOldEnd) {
		t.Fatalf("旧版本不能被失败登记提前截断，实际结束应仍为 3 月 31 日, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "" {
		t.Fatalf("失败登记不能留下替代关系: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 后续版本：从 4 月 1 日起持续有效，无替代关系。
	if follow.UnitPrice != 200 {
		t.Fatalf("后续版本单价被改写: %d", follow.UnitPrice)
	}
	if !follow.Start.Equal(rrFollowStart) {
		t.Fatalf("后续版本开始时刻被改写: %v", follow.Start)
	}
	if follow.End != nil || follow.EffectiveEnd != nil {
		t.Fatalf("后续版本应仍持续有效: 登记结束=%v 实际结束=%v",
			follow.End, follow.EffectiveEnd)
	}
	if follow.Replaces != "" || follow.SupersededBy != "" {
		t.Fatalf("后续版本替代关系应保持为空: Replaces=%q SupersededBy=%q",
			follow.Replaces, follow.SupersededBy)
	}
}

// 即使交接点合法，替代版本也不能占用同项另一个已登记版本的有效时间：
// 不填结束时间会延伸进持续有效的 v3；结束时间只要晚于 4 月 1 日零点，
// 哪怕只超出一纳秒，或已经通过 Replaces 指定了被替代版本，仍必须返回 ErrOverlap。
func TestReplacementOverlapsFollowUpVersion(t *testing.T) {
	cases := []struct {
		name string
		end  *time.Time
	}{
		{"不填结束时间-持续有效", nil},
		{"结束晚于后续版本开始一纳秒", timePtr(rrFollowStart.Add(time.Nanosecond))},
		{"结束晚于后续版本开始一分钟", timePtr(rrFollowStart.Add(time.Minute))},
		{"结束晚于后续版本开始一整天", timePtr(rrFollowStart.Add(24 * time.Hour))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newReplacementBaseBook(t)

			err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: rrHandoff, End: c.end, Replaces: "seat-v1",
			})
			if !errors.Is(err, ErrOverlap) {
				t.Fatalf("替代版本占用后续版本有效时间应返回 ErrOverlap, got %v", err)
			}

			// 失败不留痕迹：原两版不变，失败版本及其标识都不在查询结果中。
			assertReplacementBaseUnchanged(t, b)
		})
	}
}

// 结束时间恰好等于后续版本的开始时刻属于端点相接（结束时刻不含），不算重叠。
func TestReplacementEndingExactlyAtFollowUpStartAllowed(t *testing.T) {
	b := newReplacementBaseBook(t)
	end := rrFollowStart
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: rrHandoff, End: &end, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("与后续版本端点相接的替代登记应成功: %v", err)
	}

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("want 3 versions, got %d", len(views))
	}
	got := []string{views[0].VersionID, views[1].VersionID, views[2].VersionID}
	want := []string{"seat-v1", "seat-v2", "seat-v3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("版本应按生效时刻排列为 %v, got %v", want, got)
		}
	}
}

// 完整回归：替代登记先因重叠被拒，用户修正结束时间后沿用同一版本标识成功；
// 成功后三个版本的登记边界、实际有效区间与替代关系逐一符合预期。
// 整段流程在全新账本上重复执行多轮，保证结论稳定可复现。
func TestReplacementRejectedThenSucceedsWithSameVersionID(t *testing.T) {
	const rounds = 3
	for round := 0; round < rounds; round++ {
		t.Run("重复登记失败再修正", func(t *testing.T) {
			b := newReplacementBaseBook(t)

			// 第一次尝试：不填结束时间，延伸进 v3 的有效时间，被拒。
			err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: rrHandoff, Replaces: "seat-v1",
			})
			if !errors.Is(err, ErrOverlap) {
				t.Fatalf("第%d轮: 无结束时间的替代登记应返回 ErrOverlap, got %v", round+1, err)
			}
			assertReplacementBaseUnchanged(t, b)

			// 修正结束时间为 4 月 1 日零点（不含），沿用原标识与其余登记内容。
			fixedEnd := rrFollowStart
			if err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: rrHandoff, End: &fixedEnd, Replaces: "seat-v1",
			}); err != nil {
				t.Fatalf("第%d轮: 失败登记不应占用版本标识，修正后沿用原标识应成功, got %v",
					round+1, err)
			}

			views, err := b.ItemVersions("seat")
			if err != nil {
				t.Fatal(err)
			}
			if len(views) != 3 {
				t.Fatalf("第%d轮: 成功后应有三个版本, got %d", round+1, len(views))
			}
			old, nv, follow := views[0], views[1], views[2]
			if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" ||
				follow.VersionID != "seat-v3" {
				t.Fatalf("第%d轮: 版本集合/顺序异常: %q, %q, %q",
					round+1, old.VersionID, nv.VersionID, follow.VersionID)
			}

			// 旧版本：登记结束仍是 3 月 31 日（永不因后续登记改变），
			// 实际有效结束被截断到 3 月 10 日，并显示由 v2 替代。
			if old.UnitPrice != 150 || !old.Start.Equal(rrOldStart) {
				t.Fatalf("第%d轮: 旧版本登记信息异常: %+v", round+1, old)
			}
			if old.End == nil || !old.End.Equal(rrOldEnd) {
				t.Fatalf("第%d轮: 旧版本登记结束应仍为 3 月 31 日, got %v", round+1, old.End)
			}
			if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(rrHandoff) {
				t.Fatalf("第%d轮: 旧版本实际有效结束应为交接点 3 月 10 日, got %v",
					round+1, old.EffectiveEnd)
			}
			if old.Replaces != "" || old.SupersededBy != "seat-v2" {
				t.Fatalf("第%d轮: 旧版本替代关系异常: Replaces=%q SupersededBy=%q",
					round+1, old.Replaces, old.SupersededBy)
			}

			// 新版本：实际有效区间为 [3 月 10 日, 4 月 1 日)，替代 v1。
			if nv.UnitPrice != 180 {
				t.Fatalf("第%d轮: 新版本单价应为 180, got %d", round+1, nv.UnitPrice)
			}
			if !nv.Start.Equal(rrHandoff) || !nv.EffectiveStart.Equal(rrHandoff) {
				t.Fatalf("第%d轮: 新版本开始时刻应为 3 月 10 日: %+v", round+1, nv)
			}
			if nv.End == nil || !nv.End.Equal(rrFollowStart) ||
				nv.EffectiveEnd == nil || !nv.EffectiveEnd.Equal(rrFollowStart) {
				t.Fatalf("第%d轮: 新版本有效区间应为 [03-10, 04-01): 登记结束=%v 实际结束=%v",
					round+1, nv.End, nv.EffectiveEnd)
			}
			if nv.Replaces != "seat-v1" || nv.SupersededBy != "" {
				t.Fatalf("第%d轮: 新版本替代关系异常: Replaces=%q SupersededBy=%q",
					round+1, nv.Replaces, nv.SupersededBy)
			}

			// 后续版本：边界与替代关系保持登记时的原值。
			if follow.UnitPrice != 200 || !follow.Start.Equal(rrFollowStart) {
				t.Fatalf("第%d轮: 后续版本登记信息异常: %+v", round+1, follow)
			}
			if follow.End != nil || follow.EffectiveEnd != nil {
				t.Fatalf("第%d轮: 后续版本应仍持续有效: 登记结束=%v 实际结束=%v",
					round+1, follow.End, follow.EffectiveEnd)
			}
			if follow.Replaces != "" || follow.SupersededBy != "" {
				t.Fatalf("第%d轮: 后续版本不应出现替代关系: Replaces=%q SupersededBy=%q",
					round+1, follow.Replaces, follow.SupersededBy)
			}
		})
	}
}

// timePtr 返回时间的指针，供表驱动用例填写结束时刻。
func timePtr(t time.Time) *time.Time {
	return &t
}
