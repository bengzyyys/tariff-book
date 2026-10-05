package tariff

import (
	"errors"
	"testing"
	"time"
)

// 乱序截短场景的固定时间线（全部为 UTC 零点，结束时刻不含）：
//
//	旧版本 seat-v1：单价 150 分，2026-03-01 起生效，登记结束 2026-03-31
//	后续版本 seat-v3：单价 200 分，2026-03-20 起替代 v1，不填结束时间（持续有效）
//	补登版本 seat-v2：单价 180 分，2026-03-10 起替代 v1，登记结束必须恰为 2026-03-20
//
// 登记顺序是 v1、v3、v2，而不是按生效日期排列：较晚的 v3 先把 v1 的实际有效期
// 截短到 3 月 20 日，随后才补登生效时刻更早的 v2，在 v1 的剩余有效期 [03-01,03-20)
// 内再把 v1 截短到 3 月 10 日。所有时刻写死并注入固定时钟，结论不依赖运行当天。
var (
	oorV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	oorV1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	oorV2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	oorV3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
)

// newOutOfOrderBaseBook 按 v1、v3 的顺序登记：v3 已先把 v1 的实际有效期
// 截短到 3 月 20 日，但 v1 在 [03-01, 03-20) 内仍然实际有效。
func newOutOfOrderBaseBook(t *testing.T) *Book {
	t.Helper()
	now, _ := fixedClock(oorV1Start)
	b := NewBook(WithClock(now))
	v1End := oorV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: oorV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: oorV3Start, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}
	return b
}

// assertOutOfOrderBaseUnchanged 校验失败的 v2 登记整次回滚：
// 仍只有 v1、v3 两版；v1 的实际结束保持 v3 截短出的 3 月 20 日、
// 被 v3 替代的关系保持原值；v3 的起止与替代来源 v1 也保持原值。
func assertOutOfOrderBaseUnchanged(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("失败登记后应仍只有两个版本，got %d: %+v", len(views), views)
	}
	v1, v3 := views[0], views[1]
	if v1.VersionID != "seat-v1" || v3.VersionID != "seat-v3" {
		t.Fatalf("失败登记后版本集合/顺序异常: %q, %q", v1.VersionID, v3.VersionID)
	}

	// v1：登记结束仍是 3 月 31 日；实际结束保持被 v3 截短出的 3 月 20 日；
	// 替代关系仍是被 v3 替代，不能被失败的 v2 登记改写。
	if v1.UnitPrice != 150 {
		t.Fatalf("v1 单价被改写: %d", v1.UnitPrice)
	}
	if v1.End == nil || !v1.End.Equal(oorV1End) {
		t.Fatalf("v1 登记结束被改写: %v", v1.End)
	}
	if v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(oorV3Start) {
		t.Fatalf("v1 实际结束应保持 v3 截短出的 3 月 20 日, got %v", v1.EffectiveEnd)
	}
	if v1.Replaces != "" || v1.SupersededBy != "seat-v3" {
		t.Fatalf("v1 替代关系被失败登记改写: Replaces=%q SupersededBy=%q",
			v1.Replaces, v1.SupersededBy)
	}

	// v3：3 月 20 日起持续有效，替代来源仍登记为 v1，不能被失败登记截短或改写。
	if v3.UnitPrice != 200 || !v3.Start.Equal(oorV3Start) {
		t.Fatalf("v3 登记信息异常: %+v", v3)
	}
	if v3.End != nil || v3.EffectiveEnd != nil {
		t.Fatalf("v3 应仍持续有效: 登记结束=%v 实际结束=%v", v3.End, v3.EffectiveEnd)
	}
	if v3.Replaces != "seat-v1" || v3.SupersededBy != "" {
		t.Fatalf("v3 替代关系被改写: Replaces=%q SupersededBy=%q",
			v3.Replaces, v3.SupersededBy)
	}
}

// 不能因为旧版已显示被 v3 替代，就把仍落在它实际有效期 [03-01,03-20) 内的
// v2 登记（交接点 3 月 10 日）一概拒绝；但 v2 也不能挤占已安排好的 v3：
// 不填结束时间，或结束时间比 3 月 20 日晚一纳秒，都必须返回现有的 ErrOverlap。
func TestOutOfOrderReplacementOverlapRejected(t *testing.T) {
	cases := []struct {
		name string
		end  *time.Time
	}{
		{"不填结束时间-延伸进持续有效的v3", nil},
		{"结束晚于v3开始一纳秒", timePtr(oorV3Start.Add(time.Nanosecond))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newOutOfOrderBaseBook(t)

			err := b.RegisterVersion(RegisterRequest{
				ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
				Start: oorV2Start, End: c.end, Replaces: "seat-v1",
			})
			if !errors.Is(err, ErrOverlap) {
				t.Fatalf("v2 挤占已安排的 v3 有效时间应返回 ErrOverlap, got %v", err)
			}

			// 失败不留痕迹：v1、v3 的边界与替代关系保持原值，v2 与其标识都不在账本中。
			assertOutOfOrderBaseUnchanged(t, b)
		})
	}
}

// 交接点 3 月 10 日落在 v1 当前实际有效期内，替代来源 v1 合法：
// 补登 v2 不能被误报成 ErrInvalidReplacement。
func TestOutOfOrderReplacementSourceStillEffective(t *testing.T) {
	b := newOutOfOrderBaseBook(t)
	end := oorV3Start
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: oorV2Start, End: &end, Replaces: "seat-v1",
	})
	if err != nil {
		t.Fatalf("在旧版剩余实际有效期内补登替代版本应成功: %v", err)
	}
}

// 完整回归：v2 先因重叠被拒（不填结束时间、再试晚一纳秒），修正结束时间为
// 3 月 20 日后沿用同一 v2 标识登记成功；成功后三版按生效时刻排列，
// 各自的登记边界、实际有效区间与替代关系逐一符合预期。
func TestOutOfOrderReplacementRejectedThenSucceedsWithSameVersionID(t *testing.T) {
	b := newOutOfOrderBaseBook(t)

	// 第一次尝试：不填结束时间，延伸进持续有效的 v3，被拒并整次回滚。
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: oorV2Start, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("无结束时间的 v2 登记应返回 ErrOverlap, got %v", err)
	}
	assertOutOfOrderBaseUnchanged(t, b)

	// 第二次尝试：结束只比 v3 开始晚一纳秒，仍然重叠。
	tooLate := oorV3Start.Add(time.Nanosecond)
	err = b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: oorV2Start, End: &tooLate, Replaces: "seat-v1",
	})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("结束晚于 v3 开始一纳秒应返回 ErrOverlap, got %v", err)
	}
	assertOutOfOrderBaseUnchanged(t, b)

	// 修正结束时间为 3 月 20 日零点（不含），沿用同一标识与其余登记内容。
	// v2 区间 [03-10, 03-20) 与 v3 的 [03-20, ∞) 仅端点相接，不算重叠。
	fixedEnd := oorV3Start
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: oorV2Start, End: &fixedEnd, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("失败登记不应占用版本标识，修正后沿用 v2 标识应成功: %v", err)
	}

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("成功后应有三个版本, got %d", len(views))
	}
	v1, v2, v3 := views[0], views[1], views[2]
	if v1.VersionID != "seat-v1" || v2.VersionID != "seat-v2" || v3.VersionID != "seat-v3" {
		t.Fatalf("版本应按生效时刻排列为 v1/v2/v3: %q, %q, %q",
			v1.VersionID, v2.VersionID, v3.VersionID)
	}
	if v1.UnitPrice != 150 || v2.UnitPrice != 180 || v3.UnitPrice != 200 {
		t.Fatalf("三版单价应分别为 150/180/200: %d, %d, %d",
			v1.UnitPrice, v2.UnitPrice, v3.UnitPrice)
	}

	// v1：登记结束仍是 3 月 31 日；实际结束被后补的 v2 进一步提前到 3 月 10 日，
	// 显示由 v2 替代。
	if !v1.Start.Equal(oorV1Start) {
		t.Fatalf("v1 开始时刻异常: %v", v1.Start)
	}
	if v1.End == nil || !v1.End.Equal(oorV1End) {
		t.Fatalf("v1 登记结束应仍为 3 月 31 日, got %v", v1.End)
	}
	if v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(oorV2Start) {
		t.Fatalf("v1 实际结束应提前到 3 月 10 日, got %v", v1.EffectiveEnd)
	}
	if v1.Replaces != "" || v1.SupersededBy != "seat-v2" {
		t.Fatalf("v1 应显示由 v2 替代: Replaces=%q SupersededBy=%q",
			v1.Replaces, v1.SupersededBy)
	}

	// v2：记录替代 v1，实际区间恰为 [3 月 10 日, 3 月 20 日)。
	if !v2.Start.Equal(oorV2Start) || !v2.EffectiveStart.Equal(oorV2Start) {
		t.Fatalf("v2 开始时刻应为 3 月 10 日: %+v", v2)
	}
	if v2.End == nil || !v2.End.Equal(oorV3Start) ||
		v2.EffectiveEnd == nil || !v2.EffectiveEnd.Equal(oorV3Start) {
		t.Fatalf("v2 实际区间应为 [03-10, 03-20): 登记结束=%v 实际结束=%v",
			v2.End, v2.EffectiveEnd)
	}
	if v2.Replaces != "seat-v1" || v2.SupersededBy != "" {
		t.Fatalf("v2 应记录替代 v1、尚未被替代: Replaces=%q SupersededBy=%q",
			v2.Replaces, v2.SupersededBy)
	}

	// v3：起止时间和登记时填写的替代来源 v1 都保持不变——不能为了把版本排列成
	// 连续链而把 v3 的 Replaces 改写成 v2，也不能被 v2 提前截短。
	if !v3.Start.Equal(oorV3Start) {
		t.Fatalf("v3 开始时刻被改写: %v", v3.Start)
	}
	if v3.End != nil || v3.EffectiveEnd != nil {
		t.Fatalf("v3 应仍从 3 月 20 日起持续有效: 登记结束=%v 实际结束=%v",
			v3.End, v3.EffectiveEnd)
	}
	if v3.Replaces != "seat-v1" || v3.SupersededBy != "" {
		t.Fatalf("v3 的替代来源应保持登记时的 v1，不能被改写成连续链: Replaces=%q SupersededBy=%q",
			v3.Replaces, v3.SupersededBy)
	}
}

// 按指定时刻查询的选择依据是实际有效区间，而不是登记顺序或 v1 的月底登记结束：
// 3 月 10 日交接点前选 v1，交接点起至 3 月 20 日前选 v2，3 月 20 日交接点起选 v3；
// 返回的版本与单价必须和版本列表一致。
func TestOutOfOrderReplacementEffectiveVersionAt(t *testing.T) {
	b := newOutOfOrderBaseBook(t)
	end := oorV3Start
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: oorV2Start, End: &end, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}

	cases := []struct {
		name      string
		at        time.Time
		wantID    string
		wantPrice int64
		wantErr   error
	}{
		{"早于首版开始无生效版本", time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), "", 0, ErrNoEffectiveVersion},
		{"v1 开始时刻（含）选 v1", oorV1Start, "seat-v1", 150, nil},
		{"3 月 9 日选 v1", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), "seat-v1", 150, nil},
		{"v2 交接点前一纳秒仍选 v1", oorV2Start.Add(-time.Nanosecond), "seat-v1", 150, nil},
		{"v2 交接点起选 v2", oorV2Start, "seat-v2", 180, nil},
		{"3 月 15 日选 v2", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), "seat-v2", 180, nil},
		{"v3 交接点前一纳秒仍选 v2", oorV3Start.Add(-time.Nanosecond), "seat-v2", 180, nil},
		{"v3 交接点起选 v3", oorV3Start, "seat-v3", 200, nil},
		// 3 月 25 日：v1 登记结束 3 月 31 日尚未到，但按实际区间应选持续有效的 v3。
		{"v1 登记结束前仍选 v3", time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC), "seat-v3", 200, nil},
		{"更远的未来仍选持续有效的 v3", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "seat-v3", 200, nil},
	}
	for _, c := range cases {
		got, err := b.EffectiveVersionAt("seat", c.at)
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("%s: want %v, got %v (view=%+v)", c.name, c.wantErr, err, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: 不应返回错误, got %v", c.name, err)
		}
		if got.VersionID != c.wantID || got.UnitPrice != c.wantPrice {
			t.Fatalf("%s: 选中 %q 单价 %d, want %q 单价 %d",
				c.name, got.VersionID, got.UnitPrice, c.wantID, c.wantPrice)
		}
	}
}

// 交接点报价同样以实际有效区间为准（结束时刻不含）：
// 3 月 10 日起引用 v1 得到 version_expired，引用 v2 按 180 分确认；
// 3 月 20 日起引用 v2 得到 version_expired，引用 v3 按 200 分确认。
// 拒绝是正常受理（err 为 nil、金额为零），不影响已确认记录。
func TestOutOfOrderReplacementQuotesAcrossHandoffs(t *testing.T) {
	now, setNow := fixedClock(oorV1Start)
	b := NewBook(WithClock(now))
	v1End := oorV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: oorV1Start, End: &v1End,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v3", UnitPrice: 200,
		Start: oorV3Start, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v3: %v", err)
	}
	v2End := oorV3Start
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: oorV2Start, End: &v2End, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}

	// 第一个交接点：v1 已失效，v2 生效。
	setNow(oorV2Start)
	expiredV1, err := b.Quote(QuoteRequest{
		RequestID: "oor-v1-expired", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("拒绝是正常受理，err 应为空: %v", err)
	}
	if expiredV1.Confirmed || expiredV1.Reason != ReasonVersionExpired {
		t.Fatalf("3 月 10 日引用 v1 应返回 version_expired: %+v", expiredV1)
	}
	if expiredV1.UnitPrice != 0 || expiredV1.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: %d, %d", expiredV1.UnitPrice, expiredV1.Total)
	}
	confirmedV2, err := b.Quote(QuoteRequest{
		RequestID: "oor-v2-720", ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("确认是正常受理，err 应为空: %v", err)
	}
	if !confirmedV2.Confirmed || confirmedV2.UnitPrice != 180 || confirmedV2.Total != 720 {
		t.Fatalf("3 月 10 日引用 v2 应确认 720 分: %+v", confirmedV2)
	}

	// 第二个交接点：v2 已失效，v3 生效。
	setNow(oorV3Start)
	expiredV2, err := b.Quote(QuoteRequest{
		RequestID: "oor-v2-expired", ItemID: "seat", VersionID: "seat-v2", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("拒绝是正常受理，err 应为空: %v", err)
	}
	if expiredV2.Confirmed || expiredV2.Reason != ReasonVersionExpired {
		t.Fatalf("3 月 20 日引用 v2 应返回 version_expired: %+v", expiredV2)
	}
	confirmedV3, err := b.Quote(QuoteRequest{
		RequestID: "oor-v3-800", ItemID: "seat", VersionID: "seat-v3", Quantity: 4,
	})
	if err != nil {
		t.Fatalf("确认是正常受理，err 应为空: %v", err)
	}
	if !confirmedV3.Confirmed || confirmedV3.UnitPrice != 200 || confirmedV3.Total != 800 {
		t.Fatalf("3 月 20 日引用 v3 应确认 800 分: %+v", confirmedV3)
	}
}
