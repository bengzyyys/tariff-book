package tariff

import (
	"errors"
	"testing"
	"time"
)

// 同名版本场景的固定时间线（UTC）：
//
//	seat/v1：单价 150 分，2026-03-01 起生效，登记结束 2026-03-31
//	meal/v1：单价 60 分，2026-03-05 起生效，登记结束 2026-03-20
//
// 两个费率项复用版本标识 v1，有效期互相重叠；
// 重叠只会在同一费率项内部构成冲突，跨项同名是合法登记。
var (
	sameNameStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seatV1End       = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	mealV1Start     = time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	mealV1End       = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	sameNameHandoff = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
)

// newSameNameBook 返回一本 seat 与 meal 各自拥有名为 v1 的版本的账本及其时钟。
func newSameNameBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: sameNameStart,
		End:   &seatV1End,
	}); err != nil {
		t.Fatalf("register seat v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "meal", VersionID: "v1", UnitPrice: 60,
		Start: mealV1Start,
		End:   &mealV1End,
	}); err != nil {
		t.Fatalf("register meal v1: %v", err)
	}
	return b, setNow
}

// registerSeatV2 登记 seat/v2：单价 180，在交接时刻起替代本项的 v1。
func registerSeatV2(t *testing.T, b *Book) {
	t.Helper()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180,
		Start:    sameNameHandoff,
		Replaces: "v1",
	}); err != nil {
		t.Fatalf("register seat v2: %v", err)
	}
}

// 一次替代只能作用于请求指定的费率项：seat 登记 v2 替代自己的 v1 后，
// meal 的同名 v1 在单价、时间边界和替代关系上都保持原样。
func TestSameNameReplacementScopedToRequestedItem(t *testing.T) {
	b, _ := newSameNameBook(t, sameNameStart)
	registerSeatV2(t, b)

	// 本项 seat：旧版实际结束被截断到交接时刻，登记结束保留原值，
	// 新旧替代关系对应本项的 v1 与 v2。
	seatViews, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatViews) != 2 {
		t.Fatalf("seat 应有 2 个版本, got %d", len(seatViews))
	}
	old, nv := seatViews[0], seatViews[1]
	if old.VersionID != "v1" || nv.VersionID != "v2" {
		t.Fatalf("unexpected order: %v, %v", old.VersionID, nv.VersionID)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(sameNameHandoff) {
		t.Fatalf("seat v1 实际结束应为交接时刻: %v", old.EffectiveEnd)
	}
	if old.End == nil || !old.End.Equal(seatV1End) {
		t.Fatalf("seat v1 登记结束应保留原值: %v", old.End)
	}
	if old.SupersededBy != "v2" || nv.Replaces != "v1" {
		t.Fatalf("替代关系应落在 seat 的 v1/v2 之间: %q / %q", old.SupersededBy, nv.Replaces)
	}
	if nv.SupersededBy != "" || nv.UnitPrice != 180 {
		t.Fatalf("seat v2 视图异常: %+v", nv)
	}

	// 另一项 meal 的同名 v1：单价、登记结束、实际结束、替代关系全部不变。
	mealViews, err := b.ItemVersions("meal")
	if err != nil {
		t.Fatal(err)
	}
	if len(mealViews) != 1 {
		t.Fatalf("meal 应仍只有 v1, got %d versions", len(mealViews))
	}
	mv := mealViews[0]
	if mv.VersionID != "v1" || mv.UnitPrice != 60 {
		t.Fatalf("meal v1 被改动: %+v", mv)
	}
	if mv.End == nil || !mv.End.Equal(mealV1End) {
		t.Fatalf("meal v1 登记结束不应变化: %v", mv.End)
	}
	if mv.EffectiveEnd == nil || !mv.EffectiveEnd.Equal(mealV1End) {
		t.Fatalf("meal v1 实际结束不应被截断: %v", mv.EffectiveEnd)
	}
	if mv.Replaces != "" || mv.SupersededBy != "" {
		t.Fatalf("meal v1 不应出现替代关系: replaces=%q supersededBy=%q", mv.Replaces, mv.SupersededBy)
	}
}

// 交接时刻用各自未使用过的请求标识引用两项的同名 v1：
// 已交接的 seat/v1 以 version_expired 拒绝，meal/v1 仍按自己的单价确认，
// 报价来源保持请求指定的费率项。
func TestSameNameQuoteAtHandoff(t *testing.T) {
	b, setNow := newSameNameBook(t, sameNameStart)
	registerSeatV2(t, b)
	setNow(sameNameHandoff)

	// 版本拒绝是正常受理结果：err 为空，结果不确认且单价、总价为零。
	expired, err := b.Quote(QuoteRequest{
		RequestID: "same-name-seat-v1", ItemID: "seat", VersionID: "v1", Quantity: 3,
	})
	if err != nil {
		t.Fatalf("拒绝是正常受理，err 应为空: %v", err)
	}
	if expired.Confirmed || expired.Reason != ReasonVersionExpired {
		t.Fatalf("seat v1 在交接时刻应以 version_expired 拒绝: %+v", expired)
	}
	if expired.UnitPrice != 0 || expired.Total != 0 {
		t.Fatalf("拒绝结果金额应为 0: 单价=%d 总价=%d", expired.UnitPrice, expired.Total)
	}
	if expired.Request.ItemID != "seat" || expired.Request.VersionID != "v1" {
		t.Fatalf("拒绝结果必须保留请求来源: %+v", expired.Request)
	}
	if !expired.AcceptedAt.Equal(sameNameHandoff) {
		t.Fatalf("受理时刻应为交接时刻: %v", expired.AcceptedAt)
	}

	// 另一项的同名 v1 不受 seat 交接影响，仍按自己的单价确认。
	kept, err := b.Quote(QuoteRequest{
		RequestID: "same-name-meal-v1", ItemID: "meal", VersionID: "v1", Quantity: 3,
	})
	if err != nil {
		t.Fatalf("meal v1 报价 err 应为空: %v", err)
	}
	if !kept.Confirmed || kept.UnitPrice != 60 || kept.Total != 180 {
		t.Fatalf("meal v1 应按自己的单价确认: %+v", kept)
	}
	if kept.Request.ItemID != "meal" || kept.Request.VersionID != "v1" {
		t.Fatalf("报价来源应保持请求指定的费率项: %+v", kept.Request)
	}
}

// 同名来源状态不一致：seat/v1 在拟定交接时刻已经结束，meal/v1 当时仍有效。
// 替代 seat/v1 必须返回 ErrInvalidReplacement，不能借 meal 仍有效的同名版本通过校验。
func TestSameNameReplacementSourceAlreadyEnded(t *testing.T) {
	seatEnd := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	mealEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	now, setNow := fixedClock(sameNameStart)
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: sameNameStart, End: &seatEnd,
	}); err != nil {
		t.Fatalf("register seat v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "meal", VersionID: "v1", UnitPrice: 60,
		Start: sameNameStart, End: &mealEnd,
	}); err != nil {
		t.Fatalf("register meal v1: %v", err)
	}

	// 测试前提：在拟定的交接时刻，本项 v1 已结束，另一项同名 v1 仍有效。
	setNow(seatEnd)
	seatOut, err := b.Quote(QuoteRequest{
		RequestID: "premise-seat", ItemID: "seat", VersionID: "v1", Quantity: 1,
	})
	if err != nil || seatOut.Confirmed || seatOut.Reason != ReasonVersionExpired {
		t.Fatalf("测试前提不成立：seat v1 在交接时刻应已结束: %v %+v", err, seatOut)
	}
	mealOut, err := b.Quote(QuoteRequest{
		RequestID: "premise-meal", ItemID: "meal", VersionID: "v1", Quantity: 1,
	})
	if err != nil || !mealOut.Confirmed {
		t.Fatalf("测试前提不成立：meal v1 在交接时刻应仍有效: %v %+v", err, mealOut)
	}

	// 结束时刻本身不属于有效期：以 seat/v1 的结束时刻作为新版开始，必须拒绝。
	err = b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180,
		Start: seatEnd, Replaces: "v1",
	})
	if !errors.Is(err, ErrInvalidReplacement) {
		t.Fatalf("来源已结束时替代应返回 ErrInvalidReplacement: %v", err)
	}
	if errors.Is(err, ErrReplaceTargetWrongItem) {
		t.Fatalf("本项已有来源，不能说成替代来源属于另一项: %v", err)
	}

	// 晚于来源结束时刻也不行：不能借 meal/v1 当时仍有效而通过校验。
	err = b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180,
		Start: seatEnd.Add(24 * time.Hour), Replaces: "v1",
	})
	if !errors.Is(err, ErrInvalidReplacement) {
		t.Fatalf("交接时刻晚于来源结束应返回 ErrInvalidReplacement: %v", err)
	}

	// 失败后两项都不应出现失败的新版本，旧版本边界与替代关系与登记前一致。
	seatViews, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatViews) != 1 || seatViews[0].VersionID != "v1" {
		t.Fatalf("seat 不应出现失败的 v2: %+v", seatViews)
	}
	sv := seatViews[0]
	if sv.End == nil || !sv.End.Equal(seatEnd) || sv.EffectiveEnd == nil || !sv.EffectiveEnd.Equal(seatEnd) {
		t.Fatalf("seat v1 时间边界不应变化: %+v", sv)
	}
	if sv.Replaces != "" || sv.SupersededBy != "" {
		t.Fatalf("seat v1 不应出现替代关系: %+v", sv)
	}
	mealViews, err := b.ItemVersions("meal")
	if err != nil {
		t.Fatal(err)
	}
	if len(mealViews) != 1 || mealViews[0].VersionID != "v1" {
		t.Fatalf("meal 不应出现失败的 v2: %+v", mealViews)
	}
	mv := mealViews[0]
	if mv.UnitPrice != 60 || mv.End == nil || !mv.End.Equal(mealEnd) ||
		mv.EffectiveEnd == nil || !mv.EffectiveEnd.Equal(mealEnd) ||
		mv.Replaces != "" || mv.SupersededBy != "" {
		t.Fatalf("meal v1 不应受失败登记影响: %+v", mv)
	}

	// 失败登记也不能改变另一项正常报价的金额或来源。
	after, err := b.Quote(QuoteRequest{
		RequestID: "after-failure-meal", ItemID: "meal", VersionID: "v1", Quantity: 2,
	})
	if err != nil || !after.Confirmed || after.UnitPrice != 60 || after.Total != 120 {
		t.Fatalf("meal 报价不应受失败登记影响: %v %+v", err, after)
	}
	if after.Request.ItemID != "meal" || after.Request.VersionID != "v1" {
		t.Fatalf("报价来源应保持请求指定的费率项: %+v", after.Request)
	}
}

// 本项根本没有所填的替代来源、只有其他费率项存在该版本标识时，
// 按已有含义返回 ErrReplaceTargetWrongItem，且不留下任何变更。
func TestSameNameReplaceTargetOnlyExistsInOtherItem(t *testing.T) {
	now, setNow := fixedClock(sameNameStart)
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "s0", UnitPrice: 150,
		Start: sameNameStart, End: &seatV1End,
	}); err != nil {
		t.Fatalf("register seat s0: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "meal", VersionID: "v1", UnitPrice: 60,
		Start: sameNameStart, End: &mealV1End,
	}); err != nil {
		t.Fatalf("register meal v1: %v", err)
	}

	// seat 没有名为 v1 的版本，只有 meal 有：替代来源属于另一项。
	err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180,
		Start: sameNameHandoff, Replaces: "v1",
	})
	if !errors.Is(err, ErrReplaceTargetWrongItem) {
		t.Fatalf("替代来源只存在于另一项时应返回 ErrReplaceTargetWrongItem: %v", err)
	}

	// 失败后：两项都不出现失败的 v2，旧版本边界与替代关系与登记前一致。
	seatViews, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatViews) != 1 || seatViews[0].VersionID != "s0" {
		t.Fatalf("seat 不应出现失败的 v2: %+v", seatViews)
	}
	sv := seatViews[0]
	if sv.End == nil || !sv.End.Equal(seatV1End) || sv.EffectiveEnd == nil || !sv.EffectiveEnd.Equal(seatV1End) {
		t.Fatalf("seat s0 时间边界不应变化: %+v", sv)
	}
	if sv.Replaces != "" || sv.SupersededBy != "" {
		t.Fatalf("seat s0 不应出现替代关系: %+v", sv)
	}
	mealViews, err := b.ItemVersions("meal")
	if err != nil {
		t.Fatal(err)
	}
	if len(mealViews) != 1 || mealViews[0].VersionID != "v1" {
		t.Fatalf("meal 不应出现失败的 v2: %+v", mealViews)
	}
	mv := mealViews[0]
	if mv.UnitPrice != 60 || mv.End == nil || !mv.End.Equal(mealV1End) ||
		mv.EffectiveEnd == nil || !mv.EffectiveEnd.Equal(mealV1End) ||
		mv.Replaces != "" || mv.SupersededBy != "" {
		t.Fatalf("meal v1 不应受失败登记影响: %+v", mv)
	}

	// 失败登记也不能改变另一项正常报价的金额或来源。
	setNow(sameNameHandoff)
	out, err := b.Quote(QuoteRequest{
		RequestID: "wrong-item-meal", ItemID: "meal", VersionID: "v1", Quantity: 2,
	})
	if err != nil || !out.Confirmed || out.UnitPrice != 60 || out.Total != 120 {
		t.Fatalf("meal 报价不应受失败登记影响: %v %+v", err, out)
	}
	if out.Request.ItemID != "meal" || out.Request.VersionID != "v1" {
		t.Fatalf("报价来源应保持请求指定的费率项: %+v", out.Request)
	}
}
