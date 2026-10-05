package tariff

import (
	"errors"
	"testing"
	"time"
)

// “同一旧版先被较晚版本截短、随后在剩余有效期内又被更早版本截短”
// 场景的固定时间线（全部为 UTC 零点，结束时刻不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 起，登记结束 2026-03-31
//	较晚版本 seat-v3：单价 200 分，2026-03-20 起替代 v1，持续有效
//	更早版本 seat-v2：单价 180 分，拟从 2026-03-10 起替代 v1，结束 2026-03-20
//
// 登记顺序是 v1、v3、v2，而不是按生效日期排列。v3 登记后 v1 的实际结束
// 已提前到 3 月 20 日，但 v2 的交接点 3 月 10 日仍落在 v1 当前的实际有效
// 区间 [03-01, 03-20) 内：不能因为 v1 已显示被 v3 替代就拒绝这次登记，
// 也不能让 v2 占用已安排好的 v3 的有效时间。所有时刻写死并注入固定时钟，
// 结论不依赖运行当天的真实日期或时间流逝。
var (
	dtOldStart     = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	dtOldEnd       = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	dtEarlyHandoff = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	dtLateHandoff  = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
)

// newTwiceTruncatedBaseBook 准备按 v1、v3 顺序登记了两个版本的账本：
// v3 从 3 月 20 日起替代 v1，v1 的实际结束已被截短到 3 月 20 日，
// 登记结束仍是 3 月 31 日。
func newTwiceTruncatedBaseBook(t *testing.T) *Book {
	t.Helper()
	now, _ := fixedClock(dtOldStart)
	b := NewBook(WithClock(now))
	oldEnd := dtOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: dtOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: dtLateHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}
	return b
}

// assertTwiceTruncatedBaseUnchanged 校验失败的 v2 登记整次回滚：
// 查询仍只有 v1、v3；v1 保持被 v3 截短后的状态（登记结束 3 月 31 日、
// 实际结束 3 月 20 日、由 v3 替代），v3 的信息不变，
// 失败的 v2 及其版本标识都不留痕迹。
func assertTwiceTruncatedBaseUnchanged(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("失败登记后应仍只有两个版本，got %d: %+v", len(views), views)
	}
	old, late := views[0], views[1]
	if old.VersionID != "seat-v1" || late.VersionID != "seat-v3" {
		t.Fatalf("失败登记后版本集合/顺序异常: %q, %q", old.VersionID, late.VersionID)
	}

	// 旧版：登记结束仍是 3 月 31 日，实际结束保持被 v3 截短后的 3 月 20 日，
	// 不能被失败登记再提前到 3 月 10 日，替代关系也不能改写。
	if old.UnitPrice != 150 {
		t.Fatalf("旧版单价被改写: %d", old.UnitPrice)
	}
	if old.End == nil || !old.End.Equal(dtOldEnd) {
		t.Fatalf("旧版登记结束被改写: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(dtLateHandoff) {
		t.Fatalf("旧版实际结束应保持被 v3 截短后的 3 月 20 日, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v3" {
		t.Fatalf("旧版替代关系被失败登记改写: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 较晚版本：从 3 月 20 日起持续有效，替代来源仍是 v1。
	if late.UnitPrice != 200 || !late.Start.Equal(dtLateHandoff) {
		t.Fatalf("较晚版本登记信息被改写: %+v", late)
	}
	if late.End != nil || late.EffectiveEnd != nil {
		t.Fatalf("较晚版本应仍持续有效: 登记结束=%v 实际结束=%v",
			late.End, late.EffectiveEnd)
	}
	if late.Replaces != "seat-v1" || late.SupersededBy != "" {
		t.Fatalf("较晚版本替代关系被改写: Replaces=%q SupersededBy=%q",
			late.Replaces, late.SupersededBy)
	}
}

// 旧版虽已被 v3 截短，v2 的登记仍不能挤占 v3 已经安排好的有效时间：
// 不填结束时间会延伸进持续有效的 v3；结束时间只要晚于 3 月 20 日零点，
// 哪怕只超出一纳秒，都必须返回现有的 ErrOverlap。
func TestSecondTruncationOverlappingSuccessorRejected(t *testing.T) {
	cases := []struct {
		name string
		end  *time.Time
	}{
		{"不填结束时间-持续有效", nil},
		{"结束晚于较晚版本开始一纳秒", timePtr(dtLateHandoff.Add(time.Nanosecond))},
		{"结束晚于较晚版本开始一分钟", timePtr(dtLateHandoff.Add(time.Minute))},
		{"结束晚于较晚版本开始一整天", timePtr(dtLateHandoff.Add(24 * time.Hour))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newTwiceTruncatedBaseBook(t)

			err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: dtEarlyHandoff, End: c.end, Replaces: "seat-v1",
			})
			if !errors.Is(err, ErrOverlap) {
				t.Fatalf("挤占较晚版本有效时间的登记应返回 ErrOverlap, got %v", err)
			}

			// 失败不留痕迹：v1 保持被 v3 截短后的状态，v3 不变，v2 不存在。
			assertTwiceTruncatedBaseUnchanged(t, b)
		})
	}
}

// 完整回归：v2 先因挤占 v3 被拒，修正结束时间为 3 月 20 日后沿用同一
// 版本标识登记成功——失败不占用版本标识。v1 虽已被 v3 替代，但 3 月 10 日
// 仍落在它当前的实际有效区间内，登记必须被接受，不能误报替代来源无效；
// v2 与 v3 只是端点相接（结束时刻不含），不能误判重叠。
// 成功后三个版本的登记边界、实际有效区间与替代关系逐一符合预期，
// 尤其 v3 原登记的替代来源 v1 不能为了排列成连续链而改写成 v2。
func TestSecondTruncationRejectedThenSucceedsWithSameVersionID(t *testing.T) {
	b := newTwiceTruncatedBaseBook(t)

	// 第一次尝试：不填结束时间，延伸进 v3 的有效时间，被拒。
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dtEarlyHandoff, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("无结束时间的登记应返回 ErrOverlap, got %v", err)
	}
	assertTwiceTruncatedBaseUnchanged(t, b)

	// 修正结束时间为 3 月 20 日零点（不含），沿用原标识与其余登记内容。
	fixedEnd := dtLateHandoff
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: dtEarlyHandoff, End: &fixedEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("失败登记不应占用版本标识，修正后沿用原标识应成功, got %v", err)
	}

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("成功后应有三个版本, got %d: %+v", len(views), views)
	}
	old, early, late := views[0], views[1], views[2]
	if old.VersionID != "seat-v1" || early.VersionID != "seat-v2" ||
		late.VersionID != "seat-v3" {
		t.Fatalf("版本应按生效时刻排列为 v1/v2/v3: %q, %q, %q",
			old.VersionID, early.VersionID, late.VersionID)
	}

	// 旧版：登记结束仍是 3 月 31 日（永不因后续登记改变），
	// 实际结束被 v2 进一步提前到 3 月 10 日，改显示由 v2 替代。
	if old.UnitPrice != 150 || !old.Start.Equal(dtOldStart) {
		t.Fatalf("旧版登记信息异常: %+v", old)
	}
	if old.End == nil || !old.End.Equal(dtOldEnd) {
		t.Fatalf("旧版登记结束应仍为 3 月 31 日, got %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(dtEarlyHandoff) {
		t.Fatalf("旧版实际结束应被 v2 提前到 3 月 10 日, got %v", old.EffectiveEnd)
	}
	if old.Replaces != "" || old.SupersededBy != "seat-v2" {
		t.Fatalf("旧版替代关系异常: Replaces=%q SupersededBy=%q",
			old.Replaces, old.SupersededBy)
	}

	// 更早版本：实际有效区间为 [3 月 10 日, 3 月 20 日)，替代 v1。
	if early.UnitPrice != 180 {
		t.Fatalf("更早版本单价应为 180, got %d", early.UnitPrice)
	}
	if !early.Start.Equal(dtEarlyHandoff) || !early.EffectiveStart.Equal(dtEarlyHandoff) {
		t.Fatalf("更早版本开始时刻应为 3 月 10 日: %+v", early)
	}
	if early.End == nil || !early.End.Equal(dtLateHandoff) ||
		early.EffectiveEnd == nil || !early.EffectiveEnd.Equal(dtLateHandoff) {
		t.Fatalf("更早版本有效区间应为 [03-10, 03-20): 登记结束=%v 实际结束=%v",
			early.End, early.EffectiveEnd)
	}
	if early.Replaces != "seat-v1" || early.SupersededBy != "" {
		t.Fatalf("更早版本替代关系异常: Replaces=%q SupersededBy=%q",
			early.Replaces, early.SupersededBy)
	}

	// 较晚版本：起止时间不变，原登记的替代来源 v1 保持原值，
	// 不能为了把替代关系排列成 v1→v2→v3 连续链而改写成 v2。
	if late.UnitPrice != 200 || !late.Start.Equal(dtLateHandoff) {
		t.Fatalf("较晚版本登记信息异常: %+v", late)
	}
	if late.End != nil || late.EffectiveEnd != nil {
		t.Fatalf("较晚版本应仍持续有效: 登记结束=%v 实际结束=%v",
			late.End, late.EffectiveEnd)
	}
	if late.Replaces != "seat-v1" || late.SupersededBy != "" {
		t.Fatalf("较晚版本替代来源应保持 v1 不变: Replaces=%q SupersededBy=%q",
			late.Replaces, late.SupersededBy)
	}
}

// 二次截短后按指定时刻查询：选择依据是各版本的实际有效区间，
// 不能沿用登记顺序（v1、v3、v2），也不能沿用 v1 的月底登记结束日期。
// [03-01, 03-10) 选 v1，3 月 10 日交接点（含）至 3 月 20 日前选 v2，
// 3 月 20 日交接点（含）起选 v3；查询返回的单价与版本列表一致。
func TestEffectiveVersionAtAfterSecondTruncation(t *testing.T) {
	now, _ := fixedClock(dtOldStart)
	b := NewBook(WithClock(now))
	oldEnd := dtOldEnd
	earlyEnd := dtLateHandoff
	for _, req := range []RegisterRequest{
		{ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150, Start: dtOldStart, End: &oldEnd},
		{ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200, Start: dtLateHandoff, Replaces: "seat-v1"},
		{ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180, Start: dtEarlyHandoff, End: &earlyEnd, Replaces: "seat-v1"},
	} {
		if err := b.RegisterVersion(req); err != nil {
			t.Fatalf("register %s: %v", req.VersionID, err)
		}
	}

	cases := []struct {
		name      string
		at        time.Time
		versionID string
		unitPrice int64
	}{
		{"v1 生效起点-含", dtOldStart, "seat-v1", 150},
		{"v2 交接点前一纳秒", dtEarlyHandoff.Add(-time.Nanosecond), "seat-v1", 150},
		{"v2 交接点-含", dtEarlyHandoff, "seat-v2", 180},
		{"v2 区间中段", time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC), "seat-v2", 180},
		{"v3 交接点前一纳秒", dtLateHandoff.Add(-time.Nanosecond), "seat-v2", 180},
		{"v3 交接点-含", dtLateHandoff, "seat-v3", 200},
		{"v1 登记结束当天仍选 v3", dtOldEnd, "seat-v3", 200},
		{"v1 登记结束之后仍选 v3", dtOldEnd.Add(24 * time.Hour), "seat-v3", 200},
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

	// 首版开始之前没有任何生效版本，不拿邻近的 v1 补位。
	if _, err := b.EffectiveVersionAt("seat", dtOldStart.Add(-time.Nanosecond)); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("首版开始之前应返回 ErrNoEffectiveVersion, got %v", err)
	}
}

// 报价与查询共用同一套实际有效期判断：时钟停在 v2 的有效区间内时，
// 引用 v2 按 180 分确认，引用实际结束已提前到 3 月 10 日的 v1 则以
// version_expired 拒绝——即使 v1 的登记结束 3 月 31 日仍在将来。
func TestQuoteAfterSecondTruncation(t *testing.T) {
	now, setNow := fixedClock(dtOldStart)
	b := NewBook(WithClock(now))
	oldEnd := dtOldEnd
	earlyEnd := dtLateHandoff
	for _, req := range []RegisterRequest{
		{ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150, Start: dtOldStart, End: &oldEnd},
		{ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200, Start: dtLateHandoff, Replaces: "seat-v1"},
		{ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180, Start: dtEarlyHandoff, End: &earlyEnd, Replaces: "seat-v1"},
	} {
		if err := b.RegisterVersion(req); err != nil {
			t.Fatalf("register %s: %v", req.VersionID, err)
		}
	}

	acceptedAt := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	setNow(acceptedAt)

	fresh, err := b.Quote(QuoteRequest{
		RequestID: "dt-v2-360", ItemID: "seat", VersionID: "seat-v2", Quantity: 2,
	})
	if err != nil {
		t.Fatalf("引用 v2 是正常受理，err 应为空: %v", err)
	}
	if !fresh.Confirmed || fresh.UnitPrice != 180 || fresh.Total != 360 {
		t.Fatalf("v2 报价应按 180 分确认 360 分: %+v", fresh)
	}
	if !fresh.AcceptedAt.Equal(acceptedAt) || fresh.Reason != ReasonNone {
		t.Fatalf("v2 确认结果受理时刻/拒绝原因异常: %+v", fresh)
	}

	expired, err := b.Quote(QuoteRequest{
		RequestID: "dt-v1-expired", ItemID: "seat", VersionID: "seat-v1", Quantity: 2,
	})
	if err != nil {
		t.Fatalf("引用 v1 是正常受理，err 应为空: %v", err)
	}
	if expired.Confirmed || expired.Reason != ReasonVersionExpired {
		t.Fatalf("v1 实际结束已提前到受理时刻之前，应以 version_expired 拒绝: %+v", expired)
	}
	if expired.UnitPrice != 0 || expired.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: 单价=%d 总价=%d", expired.UnitPrice, expired.Total)
	}
}
