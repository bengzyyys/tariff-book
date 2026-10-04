package tariff

import (
	"errors"
	"testing"
	"time"
)

// 不同费率项复用同一版本标识时，一次替代登记只能作用于请求指定费率项内的同名版本：
// 另一项存在同名版本，不能影响来源选择、交接是否合法，也不能被改写边界或替代关系。
//
// 固定时间线（UTC，结束时刻均不含）：
//
//	seat/v1：单价 150 分，2026-03-01 至 2026-03-31
//	room/v1：单价 300 分，2026-02-15 至 2026-05-31
//	        （与 seat/v1 有效期重叠，但跨费率项重叠不是冲突）
//	拟交接点：2026-03-10 00:00
var (
	crossSeatV1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	crossSeatV1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	crossRoomV1Start = time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	crossRoomV1End   = time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	crossHandoff     = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
)

// newCrossItemSameVersionBook 在同一本账本中为 seat、room 各登记一个名为 v1 的版本，
// 两者单价和结束时间不同、有效期重叠——复用版本标识与跨项重叠都应允许。
func newCrossItemSameVersionBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	seatEnd := crossSeatV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: crossSeatV1Start, End: &seatEnd,
	}); err != nil {
		t.Fatalf("register seat/v1: %v", err)
	}
	roomEnd := crossRoomV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 300,
		Start: crossRoomV1Start, End: &roomEnd,
	}); err != nil {
		t.Fatalf("不同费率项应能复用版本标识且有效期重叠: %v", err)
	}
	return b, setNow
}

// findVersionView 在指定费率项的视图中找出指定版本，找不到则让测试失败。
func findVersionView(t *testing.T, b *Book, itemID, versionID string) VersionView {
	t.Helper()
	views, err := b.ItemVersions(itemID)
	if err != nil {
		t.Fatalf("ItemVersions(%q): %v", itemID, err)
	}
	for _, v := range views {
		if v.VersionID == versionID {
			return v
		}
	}
	t.Fatalf("费率项 %q 下不存在版本 %q，实际版本视图: %+v", itemID, versionID, views)
	return VersionView{}
}

// 给 seat 登记 v2 替代 seat 自己的 v1：截断与替代关系只发生在本项两版之间，
// room 的同名 v1 单价、登记结束、实际结束和替代关系全部保持不变；
// 交接时刻两项各自的新报价只按各自版本的状态受理。
func TestReplacementScopedToRequestedItem(t *testing.T) {
	b, setNow := newCrossItemSameVersionBook(t, crossHandoff)

	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180,
		Start: crossHandoff, Replaces: "v1",
	}); err != nil {
		t.Fatalf("register seat/v2 replacing seat/v1: %v", err)
	}

	// seat/v1：单价不变，登记结束保留 3 月 31 日，实际结束截断到交接点 3 月 10 日。
	seatV1 := findVersionView(t, b, "seat", "v1")
	if seatV1.UnitPrice != 150 {
		t.Fatalf("seat/v1 单价被改写: %d", seatV1.UnitPrice)
	}
	if seatV1.End == nil || !seatV1.End.Equal(crossSeatV1End) {
		t.Fatalf("seat/v1 登记结束应保留原值 3 月 31 日: %v", seatV1.End)
	}
	if seatV1.EffectiveEnd == nil || !seatV1.EffectiveEnd.Equal(crossHandoff) {
		t.Fatalf("seat/v1 实际结束应为交接点 3 月 10 日: %v", seatV1.EffectiveEnd)
	}
	if seatV1.Replaces != "" || seatV1.SupersededBy != "v2" {
		t.Fatalf("seat/v1 替代关系异常: Replaces=%q SupersededBy=%q",
			seatV1.Replaces, seatV1.SupersededBy)
	}

	// seat/v2：替代来源对应本项的 v1，单价为新登记值。
	seatV2 := findVersionView(t, b, "seat", "v2")
	if seatV2.UnitPrice != 180 || seatV2.Replaces != "v1" || seatV2.SupersededBy != "" {
		t.Fatalf("seat/v2 登记信息异常: %+v", seatV2)
	}

	// room/v1：单价、登记结束、实际结束、替代关系都不受另一项交接影响，也不冒出新版本。
	roomV1 := findVersionView(t, b, "room", "v1")
	if roomV1.UnitPrice != 300 {
		t.Fatalf("room/v1 单价被另一项的替代登记改写: %d", roomV1.UnitPrice)
	}
	if roomV1.End == nil || !roomV1.End.Equal(crossRoomV1End) {
		t.Fatalf("room/v1 登记结束被改写: %v", roomV1.End)
	}
	if roomV1.EffectiveEnd == nil || !roomV1.EffectiveEnd.Equal(crossRoomV1End) {
		t.Fatalf("room/v1 实际结束不应被另一项的交接截断: %v", roomV1.EffectiveEnd)
	}
	if roomV1.Replaces != "" || roomV1.SupersededBy != "" {
		t.Fatalf("room/v1 不应出现替代关系: Replaces=%q SupersededBy=%q",
			roomV1.Replaces, roomV1.SupersededBy)
	}
	if views, _ := b.ItemVersions("room"); len(views) != 1 {
		t.Fatalf("另一项的替代登记不能在 room 留下新版本: %+v", views)
	}

	// 交接时刻：两项各用一个从未使用过的请求标识、合法正数量引用各自的 v1。
	setNow(crossHandoff)

	// seat/v1 已在交接点失效：正常受理（err 为空）但拒绝，金额为零，来源保留。
	seatReq := QuoteRequest{
		RequestID: "seat-v1-at-handoff", ItemID: "seat", VersionID: "v1", Quantity: 4,
	}
	seatOut, err := b.Quote(seatReq)
	if err != nil {
		t.Fatalf("版本拒绝是正常受理，err 应为空: %v", err)
	}
	if seatOut.Confirmed {
		t.Fatalf("seat/v1 在交接时刻已失效，必须拒绝: %+v", seatOut)
	}
	if seatOut.Reason != ReasonVersionExpired {
		t.Fatalf("seat/v1 拒绝原因=%q, want %q", seatOut.Reason, ReasonVersionExpired)
	}
	if seatOut.UnitPrice != 0 || seatOut.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: %d/%d", seatOut.UnitPrice, seatOut.Total)
	}
	if seatOut.Request != seatReq {
		t.Fatalf("拒绝结果来源必须仍是请求指定的 seat/v1: %+v vs %+v", seatOut.Request, seatReq)
	}
	if !seatOut.AcceptedAt.Equal(crossHandoff) {
		t.Fatalf("seat/v1 受理时刻=%v, want %v", seatOut.AcceptedAt, crossHandoff)
	}

	// room/v1 当时仍有效：按它自己的 300 分单价确认，报价来源保持请求指定的费率项。
	roomReq := QuoteRequest{
		RequestID: "room-v1-at-handoff", ItemID: "room", VersionID: "v1", Quantity: 4,
	}
	roomOut, err := b.Quote(roomReq)
	if err != nil {
		t.Fatalf("room/v1 报价不应返回错误: %v", err)
	}
	if !roomOut.Confirmed {
		t.Fatalf("room/v1 在交接时刻仍处有效期，应确认: %+v", roomOut)
	}
	if roomOut.UnitPrice != 300 || roomOut.Total != 1200 {
		t.Fatalf("room/v1 必须按自己的单价成交: 单价=%d 总价=%d, want 300/1200",
			roomOut.UnitPrice, roomOut.Total)
	}
	if roomOut.Reason != ReasonNone {
		t.Fatalf("确认结果不应带拒绝原因: %q", roomOut.Reason)
	}
	if roomOut.Request != roomReq {
		t.Fatalf("报价来源必须保持请求指定的 room/v1: %+v vs %+v", roomOut.Request, roomReq)
	}
	if !roomOut.AcceptedAt.Equal(crossHandoff) {
		t.Fatalf("room/v1 受理时刻=%v, want %v", roomOut.AcceptedAt, crossHandoff)
	}
}

// 本项 v1 在拟定交接时刻已经结束，而另一项的同名 v1 当时仍有效：
// 替代是否合法只以本项已有来源的实际有效区间为依据，必须返回 ErrInvalidReplacement，
// 不能借另一项仍有效的同名版本通过校验，也不能把来源说成属于另一项。
// 结束时刻本身不属于有效期（半开区间 [start, end)）。
func TestReplacementJudgedByOwnEndedVersionNotOtherItemLiveOne(t *testing.T) {
	now, setNow := fixedClock(crossSeatV1Start)
	b := NewBook(WithClock(now))

	// seat/v1 的登记结束恰好是拟交接点 3 月 10 日零点（不含）；room 同名 v1 到 5 月 31 日。
	seatEnd := crossHandoff
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: crossSeatV1Start, End: &seatEnd,
	}); err != nil {
		t.Fatalf("register seat/v1: %v", err)
	}
	roomEnd := crossRoomV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 300,
		Start: crossRoomV1Start, End: &roomEnd,
	}); err != nil {
		t.Fatalf("register room/v1: %v", err)
	}

	// 失败登记之前，room 上已有一笔确认报价，事后必须原样保留。
	setNow(time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	firstReq := QuoteRequest{
		RequestID: "room-before-failed-replacement", ItemID: "room", VersionID: "v1", Quantity: 2,
	}
	first, err := b.Quote(firstReq)
	if err != nil || !first.Confirmed {
		t.Fatalf("失败前 room/v1 报价应确认: %v %+v", err, first)
	}
	if first.UnitPrice != 300 || first.Total != 600 || first.Request != firstReq {
		t.Fatalf("失败前 room/v1 报价内容异常: %+v", first)
	}

	// 交接点等于结束时刻（不含）以及晚于结束时刻，都不在 seat/v1 的实际有效区间内；
	// 这两个开始时刻与 seat/v1 区间仅相邻或更晚，不会触发 ErrOverlap，
	// 唯一正确的结论是替代窗口校验失败。
	cases := []struct {
		name  string
		start time.Time
	}{
		{"交接时刻等于本项版本结束时刻（结束时刻不含）", crossHandoff},
		{"交接时刻晚于本项版本结束时刻", crossHandoff.Add(24 * time.Hour)},
	}
	for _, c := range cases {
		err := b.RegisterVersion(RegisterRequest{
			ItemID: "seat", VersionID: "v2", UnitPrice: 180,
			Start: c.start, Replaces: "v1",
		})
		if !errors.Is(err, ErrInvalidReplacement) {
			t.Fatalf("%s: 必须按本项 v1 已结束返回 ErrInvalidReplacement, got %v", c.name, err)
		}
		if errors.Is(err, ErrReplaceTargetWrongItem) {
			t.Fatalf("%s: 替代来源就在本项，不能借另一项同名版本说成来源属于另一项: %v",
				c.name, err)
		}
	}

	// 失败后：两项都不出现失败的新版本 v2，旧版时间边界与替代关系与登记前一致。
	seatViews, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatViews) != 1 || seatViews[0].VersionID != "v1" {
		t.Fatalf("失败登记后 seat 应仍只有 v1: %+v", seatViews)
	}
	if seatViews[0].UnitPrice != 150 ||
		seatViews[0].End == nil || !seatViews[0].End.Equal(crossHandoff) ||
		seatViews[0].EffectiveEnd == nil || !seatViews[0].EffectiveEnd.Equal(crossHandoff) ||
		seatViews[0].Replaces != "" || seatViews[0].SupersededBy != "" {
		t.Fatalf("seat/v1 边界或替代关系被失败登记改写: %+v", seatViews[0])
	}
	roomViews, err := b.ItemVersions("room")
	if err != nil {
		t.Fatal(err)
	}
	if len(roomViews) != 1 || roomViews[0].VersionID != "v1" {
		t.Fatalf("失败登记不能波及 room 的版本集合: %+v", roomViews)
	}
	if roomViews[0].UnitPrice != 300 ||
		roomViews[0].End == nil || !roomViews[0].End.Equal(crossRoomV1End) ||
		roomViews[0].EffectiveEnd == nil || !roomViews[0].EffectiveEnd.Equal(crossRoomV1End) ||
		roomViews[0].Replaces != "" || roomViews[0].SupersededBy != "" {
		t.Fatalf("room/v1 边界或替代关系被另一项的失败登记改写: %+v", roomViews[0])
	}

	// room 既有确认记录的金额与来源不被失败登记改变。
	got, err := b.Lookup(firstReq.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("room 既有确认记录被改写: %+v -> %+v", first, got)
	}

	// 交接时刻用各自的新标识、正数量再报价：seat/v1 正常受理但 version_expired；
	// room/v1 仍按自己的 300 分单价确认，金额与来源都不受失败登记影响。
	setNow(crossHandoff)

	seatReq := QuoteRequest{
		RequestID: "seat-v1-expired-at-handoff", ItemID: "seat", VersionID: "v1", Quantity: 4,
	}
	seatOut, err := b.Quote(seatReq)
	if err != nil {
		t.Fatalf("版本拒绝是正常受理，err 应为空: %v", err)
	}
	if seatOut.Confirmed || seatOut.Reason != ReasonVersionExpired {
		t.Fatalf("seat/v1 在结束时刻应 version_expired 拒绝: %+v", seatOut)
	}
	if seatOut.UnitPrice != 0 || seatOut.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: %d/%d", seatOut.UnitPrice, seatOut.Total)
	}
	if seatOut.Request != seatReq {
		t.Fatalf("拒绝结果来源必须仍是 seat/v1: %+v vs %+v", seatOut.Request, seatReq)
	}

	roomReq := QuoteRequest{
		RequestID: "room-v1-still-live-at-handoff", ItemID: "room", VersionID: "v1", Quantity: 3,
	}
	roomOut, err := b.Quote(roomReq)
	if err != nil {
		t.Fatalf("room/v1 报价不应返回错误: %v", err)
	}
	if !roomOut.Confirmed || roomOut.Reason != ReasonNone {
		t.Fatalf("room/v1 应正常确认: %+v", roomOut)
	}
	if roomOut.UnitPrice != 300 || roomOut.Total != 900 {
		t.Fatalf("room/v1 应按自己的单价 300 成交: 单价=%d 总价=%d, want 300/900",
			roomOut.UnitPrice, roomOut.Total)
	}
	if roomOut.Request != roomReq {
		t.Fatalf("报价来源必须保持 room/v1: %+v vs %+v", roomOut.Request, roomReq)
	}
}

// 本项根本没有所填的替代来源，只有其他费率项存在该版本标识：
// 按既有语义返回 ErrReplaceTargetWrongItem；失败不留任何痕迹，也不影响另一项报价。
func TestReplacementTargetMissingInItemOnlyExistsInOtherItem(t *testing.T) {
	now, setNow := fixedClock(crossSeatV1Start)
	b := NewBook(WithClock(now))

	// seat 只有 v0（3 月 5 日结束）；名为 v1 的版本只存在于 room。
	v0End := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v0", UnitPrice: 100,
		Start: crossSeatV1Start, End: &v0End,
	}); err != nil {
		t.Fatalf("register seat/v0: %v", err)
	}
	roomEnd := crossRoomV1End
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 300,
		Start: crossRoomV1Start, End: &roomEnd,
	}); err != nil {
		t.Fatalf("register room/v1: %v", err)
	}

	// 拟交接点 3 月 10 日落在 room/v1 有效期内，却在 seat 中找不到来源 v1：
	// 不能拿 room 的 v1 当来源，必须按既有含义报错。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180,
		Start: crossHandoff, Replaces: "v1",
	}); !errors.Is(err, ErrReplaceTargetWrongItem) {
		t.Fatalf("本项无 v1、另一项有同名 v1: want ErrReplaceTargetWrongItem, got %v", err)
	}

	// 费率项本身都不存在时，语义相同：同名版本只在别的项里。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "hall", VersionID: "v9", UnitPrice: 1,
		Start: crossHandoff, Replaces: "v1",
	}); !errors.Is(err, ErrReplaceTargetWrongItem) {
		t.Fatalf("不存在的费率项也应返回 ErrReplaceTargetWrongItem, got %v", err)
	}

	// 失败不留痕：seat 仍只有 v0，room 仍只有 v1，hall 未被创建。
	seatViews, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatViews) != 1 || seatViews[0].VersionID != "v0" {
		t.Fatalf("失败登记不能在 seat 留下 v2 或 v1: %+v", seatViews)
	}
	if seatViews[0].UnitPrice != 100 ||
		seatViews[0].End == nil || !seatViews[0].End.Equal(v0End) ||
		seatViews[0].EffectiveEnd == nil || !seatViews[0].EffectiveEnd.Equal(v0End) ||
		seatViews[0].Replaces != "" || seatViews[0].SupersededBy != "" {
		t.Fatalf("seat/v0 被失败登记改写: %+v", seatViews[0])
	}
	roomViews, err := b.ItemVersions("room")
	if err != nil {
		t.Fatal(err)
	}
	if len(roomViews) != 1 {
		t.Fatalf("room 版本集合被失败登记波及: %+v", roomViews)
	}
	if roomViews[0].UnitPrice != 300 ||
		roomViews[0].End == nil || !roomViews[0].End.Equal(crossRoomV1End) ||
		roomViews[0].EffectiveEnd == nil || !roomViews[0].EffectiveEnd.Equal(crossRoomV1End) ||
		roomViews[0].Replaces != "" || roomViews[0].SupersededBy != "" {
		t.Fatalf("room/v1 被失败登记改写: %+v", roomViews[0])
	}
	if _, err := b.ItemVersions("hall"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("失败登记不应凭空创建 hall 费率项: %v", err)
	}

	// 交接时刻的报价不受失败登记影响。
	setNow(crossHandoff)

	// seat/v1 确实不存在：正常受理（err 为空）的 version_not_found 拒绝，金额为零。
	missingReq := QuoteRequest{
		RequestID: "seat-v1-never-registered", ItemID: "seat", VersionID: "v1", Quantity: 4,
	}
	missingOut, err := b.Quote(missingReq)
	if err != nil {
		t.Fatalf("版本未找到的拒绝是正常受理，err 应为空: %v", err)
	}
	if missingOut.Confirmed || missingOut.Reason != ReasonVersionNotFound {
		t.Fatalf("seat/v1 应 version_not_found 拒绝: %+v", missingOut)
	}
	if missingOut.UnitPrice != 0 || missingOut.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: %d/%d", missingOut.UnitPrice, missingOut.Total)
	}
	if missingOut.Request != missingReq {
		t.Fatalf("拒绝结果来源必须保留请求内容: %+v vs %+v", missingOut.Request, missingReq)
	}

	// room/v1 仍按自己的单价确认，金额与来源保持不变。
	roomReq := QuoteRequest{
		RequestID: "room-v1-unaffected-at-handoff", ItemID: "room", VersionID: "v1", Quantity: 4,
	}
	roomOut, err := b.Quote(roomReq)
	if err != nil {
		t.Fatalf("room/v1 报价不应返回错误: %v", err)
	}
	if !roomOut.Confirmed || roomOut.UnitPrice != 300 || roomOut.Total != 1200 {
		t.Fatalf("room/v1 应按 300 分单价确认总价 1200: %+v", roomOut)
	}
	if roomOut.Request != roomReq {
		t.Fatalf("报价来源必须保持 room/v1: %+v vs %+v", roomOut.Request, roomReq)
	}

	// 失败登记不占用新版本标识：去掉错误来源后，seat/v2 在与 v0 不重叠的 3 月 10 日可正常登记。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 180, Start: crossHandoff,
	}); err != nil {
		t.Fatalf("失败的登记不应占用版本标识 v2: %v", err)
	}
	seatV2 := findVersionView(t, b, "seat", "v2")
	if seatV2.Replaces != "" || seatV2.SupersededBy != "" || seatV2.UnitPrice != 180 {
		t.Fatalf("重新登记的 seat/v2 不应带有失败时填写的替代来源: %+v", seatV2)
	}
}
