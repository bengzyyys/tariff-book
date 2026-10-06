package tariff

import (
	"errors"
	"testing"
)

// 跨费率项复用非空请求标识的回归保障。
//
// 费率项各自允许使用同名版本（seat/v1 与 room/v1 可同时存在），
// 但非空请求标识属于整本账本：标识的“首次请求内容”按费率项、版本、数量整体记录。
// 首次受理之后把费率项改成另一项，即使版本标识和数量完全相同，也必须返回
// ErrRequestIDConflict——不能按另一项再确认一个价格，也不能把原结果当成另一项的报价。
// 首次结果是拒绝时同样占用标识：不能因为前一笔没有确认价就允许重新占用。
//
// 判定三类结果必须各有依据，不能只看“有没有报错”或“金额是不是零”：
//
//	内容冲突：err 可被 errors.Is 识别为 ErrRequestIDConflict，返回的 Outcome 为零值；
//	正常受理后的拒绝：err 为空、Confirmed 为 false、Reason 给出具体拒绝原因；
//	确认报价：err 为空、Confirmed 为 true、Reason 为空且金额为单价乘数量。

// TestRequestIDConflictAcrossItemsConfirmedFirst 覆盖首次结果为确认的情形。
//
//	seat/v1：单价 150 分，at(0) 起生效
//	room/v1：单价 300 分，at(0) 起生效（同名版本，分属不同费率项）
//	首次：at(5)，用标识 req-across-confirmed 给 seat/v1 报数量 4 → 确认 600 分
//	冲突：时钟推进后沿用同一标识改报 room/v1，版本与数量仍为 v1 和 4 → 内容冲突
func TestRequestIDConflictAcrossItemsConfirmedFirst(t *testing.T) {
	now, setNow := fixedClock(at(5))
	b := NewBook(WithClock(now))

	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: at(0), End: atPtr(100),
	}); err != nil {
		t.Fatalf("register seat/v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 300,
		Start: at(0), End: atPtr(100),
	}); err != nil {
		t.Fatalf("register room/v1: %v", err)
	}

	// 首次受理：seat/v1、数量 4，按 150 分单价确认 600 分。
	const reqID = "req-across-confirmed"
	seatReq := QuoteRequest{RequestID: reqID, ItemID: "seat", VersionID: "v1", Quantity: 4}
	first, err := b.Quote(seatReq)
	if err != nil {
		t.Fatalf("首次报价是正常受理，err 应为空: %v", err)
	}
	if !first.Confirmed || first.Reason != ReasonNone {
		t.Fatalf("首次报价应确认且不带拒绝原因: %+v", first)
	}
	if first.UnitPrice != 150 || first.Total != 600 {
		t.Fatalf("首次报价金额: 单价=%d 总价=%d, want 150/600", first.UnitPrice, first.Total)
	}
	if first.Request != seatReq {
		t.Fatalf("首次结果来源必须保留 seat/v1、数量 4: %+v vs %+v", first.Request, seatReq)
	}
	if !first.AcceptedAt.Equal(at(5)) {
		t.Fatalf("首次受理时刻=%v, want %v", first.AcceptedAt, at(5))
	}

	// 沿用同一标识，只把费率项改成 room，版本标识和数量完全相同：
	// 这是请求内容冲突，不是 room 的新报价，必须返回 ErrRequestIDConflict。
	setNow(at(6))
	conflictOut, conflictErr := b.Quote(QuoteRequest{
		RequestID: reqID, ItemID: "room", VersionID: "v1", Quantity: 4,
	})
	if !errors.Is(conflictErr, ErrRequestIDConflict) {
		t.Fatalf("跨费率项复用标识必须返回 ErrRequestIDConflict, got %v", conflictErr)
	}
	// 冲突是调用错误：返回零值结果，不能携带任何金额、来源或受理时刻，
	// 尤其不能把 seat 的首次 600 分结果伪装成 room 的报价返回。
	if conflictOut != (Outcome{}) {
		t.Fatalf("冲突时报价结果必须为零值: %+v", conflictOut)
	}

	// 冲突不形成 room 的受理记录：room 没有其他受理记录时得到空列表且不报错。
	roomOutcomes, err := b.ItemOutcomes("room")
	if err != nil {
		t.Fatalf("room 无受理记录不应报错: %v", err)
	}
	if len(roomOutcomes) != 0 {
		t.Fatalf("冲突不能在 room 留下受理记录: %+v", roomOutcomes)
	}
	// seat 的首次结果只出现在 seat 的列表里。
	seatOutcomes, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatOutcomes) != 1 || seatOutcomes[0] != first {
		t.Fatalf("seat 列表应只含首次确认结果: %+v vs %+v", seatOutcomes, first)
	}

	// 原标识查询取回的仍是 seat 的首次结果。
	looked, err := b.Lookup(reqID)
	if err != nil {
		t.Fatalf("冲突后原标识应仍可查询: %v", err)
	}
	if looked != first {
		t.Fatalf("原标识查询被冲突调用改写: %+v -> %+v", first, looked)
	}

	// 时钟再推进后用原标识、原内容重试：原样返回首次结果，
	// 费率项、版本、数量、单价、总价、确认状态和首次受理时刻全部保持原值。
	setNow(at(7))
	replay, err := b.Quote(seatReq)
	if err != nil {
		t.Fatalf("原内容重试不应报错: %v", err)
	}
	if replay != first {
		t.Fatalf("原内容重试必须返回首次结果: %+v -> %+v", first, replay)
	}
	if replay.Request != seatReq || !replay.Confirmed ||
		replay.UnitPrice != 150 || replay.Total != 600 ||
		replay.Reason != ReasonNone || !replay.AcceptedAt.Equal(at(5)) {
		t.Fatalf("原内容重试的字段被改写: %+v", replay)
	}

	// room 改用全新标识提交相同版本与数量：正常确认 300×4=1200 分，
	// 以自己的标识查询，不影响 seat 的原有结果。
	setNow(at(8))
	const roomReqID = "req-room-own"
	roomReq := QuoteRequest{RequestID: roomReqID, ItemID: "room", VersionID: "v1", Quantity: 4}
	roomConfirmed, err := b.Quote(roomReq)
	if err != nil {
		t.Fatalf("room 新标识报价不应报错: %v", err)
	}
	if !roomConfirmed.Confirmed || roomConfirmed.Reason != ReasonNone {
		t.Fatalf("room 新标识报价应确认: %+v", roomConfirmed)
	}
	if roomConfirmed.UnitPrice != 300 || roomConfirmed.Total != 1200 {
		t.Fatalf("room 报价金额: 单价=%d 总价=%d, want 300/1200",
			roomConfirmed.UnitPrice, roomConfirmed.Total)
	}
	if roomConfirmed.Request != roomReq || !roomConfirmed.AcceptedAt.Equal(at(8)) {
		t.Fatalf("room 确认结果来源或受理时刻异常: %+v", roomConfirmed)
	}

	roomLookup, err := b.Lookup(roomReqID)
	if err != nil || roomLookup != roomConfirmed {
		t.Fatalf("room 自有标识查询异常: %v %+v vs %+v", err, roomLookup, roomConfirmed)
	}
	seatLookup, err := b.Lookup(reqID)
	if err != nil || seatLookup != first {
		t.Fatalf("room 正常受理后 seat 原有结果被影响: %v %+v", err, seatLookup)
	}

	// 按费率项查看：两项各自只有自己的记录，冲突自始至终不增加任何一条。
	seatFinal, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatFinal) != 1 || seatFinal[0] != first {
		t.Fatalf("seat 最终列表异常: %+v", seatFinal)
	}
	roomFinal, err := b.ItemOutcomes("room")
	if err != nil {
		t.Fatal(err)
	}
	if len(roomFinal) != 1 || roomFinal[0] != roomConfirmed {
		t.Fatalf("room 最终列表应只含新标识的确认结果: %+v", roomFinal)
	}
}

// TestRequestIDConflictAcrossItemsRejectedFirst 覆盖首次结果为“尚未生效”拒绝的情形：
// 首次拒绝同样占用标识，不能因为没有确认价就让另一项用同一标识重新报价。
//
//	seat/v1：单价 150 分，at(10) 才生效——at(5) 提交时 version_not_yet_effective
//	room/v1：单价 300 分，at(0) 起已生效（同名版本，分属不同费率项）
func TestRequestIDConflictAcrossItemsRejectedFirst(t *testing.T) {
	now, setNow := fixedClock(at(5))
	b := NewBook(WithClock(now))

	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: at(10), End: atPtr(100),
	}); err != nil {
		t.Fatalf("register seat/v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 300,
		Start: at(0), End: atPtr(100),
	}); err != nil {
		t.Fatalf("register room/v1: %v", err)
	}

	// 首次提交发生在 seat/v1 生效之前：调用错误为空，结果是正常受理后的拒绝，
	// 原因为 version_not_yet_effective，金额为零，来源与首次受理时刻完整保留。
	const reqID = "req-across-rejected"
	seatReq := QuoteRequest{RequestID: reqID, ItemID: "seat", VersionID: "v1", Quantity: 4}
	first, err := b.Quote(seatReq)
	if err != nil {
		t.Fatalf("尚未生效的拒绝是正常受理，err 应为空: %v", err)
	}
	if first.Confirmed {
		t.Fatalf("seat/v1 尚未生效，不能确认: %+v", first)
	}
	if first.Reason != ReasonVersionNotYetEffective {
		t.Fatalf("拒绝原因=%q, want %q", first.Reason, ReasonVersionNotYetEffective)
	}
	if first.UnitPrice != 0 || first.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: %d/%d", first.UnitPrice, first.Total)
	}
	if first.Request != seatReq {
		t.Fatalf("拒绝结果来源必须保留 seat/v1、数量 4: %+v vs %+v", first.Request, seatReq)
	}
	if !first.AcceptedAt.Equal(at(5)) {
		t.Fatalf("首次受理时刻=%v, want %v", first.AcceptedAt, at(5))
	}

	// room/v1 当时已经生效，但沿用同一标识、相同版本与数量改报 room 仍属内容冲突：
	// 不能按 room 的 300 分单价确认 1200 分，也不能返回任何非零结果。
	setNow(at(6))
	conflictOut, conflictErr := b.Quote(QuoteRequest{
		RequestID: reqID, ItemID: "room", VersionID: "v1", Quantity: 4,
	})
	if !errors.Is(conflictErr, ErrRequestIDConflict) {
		t.Fatalf("首次为拒绝时跨项复用标识仍应返回 ErrRequestIDConflict, got %v", conflictErr)
	}
	if conflictOut != (Outcome{}) {
		t.Fatalf("冲突时报价结果必须为零值，不能给出 room 的价格: %+v", conflictOut)
	}

	// 原标识查询保留的仍是首次拒绝及其来源 seat，而非 room 的任何结果。
	looked, err := b.Lookup(reqID)
	if err != nil {
		t.Fatalf("首次拒绝应可按原标识查询: %v", err)
	}
	if looked != first {
		t.Fatalf("原标识查询必须返回首次拒绝: %+v -> %+v", first, looked)
	}

	// 即使时钟走到 seat/v1 生效之后，原标识、原内容重试仍是那次首次拒绝，
	// 受理时刻停留在 at(5)——拒绝也幂等，不按当前可用性重新判断。
	setNow(at(11))
	replay, err := b.Quote(seatReq)
	if err != nil {
		t.Fatalf("原内容重试不应报错: %v", err)
	}
	if replay != first {
		t.Fatalf("原内容重试必须仍是首次拒绝: %+v -> %+v", first, replay)
	}
	if replay.Confirmed || replay.Reason != ReasonVersionNotYetEffective ||
		replay.UnitPrice != 0 || replay.Total != 0 ||
		replay.Request != seatReq || !replay.AcceptedAt.Equal(at(5)) {
		t.Fatalf("原内容重试的首次拒绝被改写: %+v", replay)
	}

	// 按费率项查看：首次拒绝只出现在 seat 列表；冲突不在 room 形成记录，
	// room 没有其他受理记录时得到空列表且不报错。
	seatOutcomes, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatOutcomes) != 1 || seatOutcomes[0] != first {
		t.Fatalf("seat 列表应只含首次拒绝: %+v vs %+v", seatOutcomes, first)
	}
	roomOutcomes, err := b.ItemOutcomes("room")
	if err != nil {
		t.Fatalf("room 无受理记录不应报错: %v", err)
	}
	if len(roomOutcomes) != 0 {
		t.Fatalf("冲突不能在 room 留下受理记录: %+v", roomOutcomes)
	}

	// room 使用全新标识提交相同内容：版本已生效，正常确认 1200 分。
	const roomReqID = "req-room-own-after-reject"
	roomReq := QuoteRequest{RequestID: roomReqID, ItemID: "room", VersionID: "v1", Quantity: 4}
	roomConfirmed, err := b.Quote(roomReq)
	if err != nil {
		t.Fatalf("room 新标识报价不应报错: %v", err)
	}
	if !roomConfirmed.Confirmed || roomConfirmed.Reason != ReasonNone {
		t.Fatalf("room 新标识报价应确认: %+v", roomConfirmed)
	}
	if roomConfirmed.UnitPrice != 300 || roomConfirmed.Total != 1200 {
		t.Fatalf("room 报价金额: 单价=%d 总价=%d, want 300/1200",
			roomConfirmed.UnitPrice, roomConfirmed.Total)
	}
	if roomConfirmed.Request != roomReq {
		t.Fatalf("room 确认结果来源异常: %+v vs %+v", roomConfirmed.Request, roomReq)
	}

	// 双方各按自己的标识查询：room 拿到新确认，seat 仍是首次拒绝，互不覆盖。
	roomLookup, err := b.Lookup(roomReqID)
	if err != nil || roomLookup != roomConfirmed {
		t.Fatalf("room 自有标识查询异常: %v %+v", err, roomLookup)
	}
	seatLookup, err := b.Lookup(reqID)
	if err != nil || seatLookup != first {
		t.Fatalf("room 正常受理不能改写 seat 的首次拒绝: %v %+v", err, seatLookup)
	}

	seatFinal, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatFinal) != 1 || seatFinal[0] != first {
		t.Fatalf("seat 最终列表仍应只含首次拒绝: %+v", seatFinal)
	}
	roomFinal, err := b.ItemOutcomes("room")
	if err != nil {
		t.Fatal(err)
	}
	if len(roomFinal) != 1 || roomFinal[0] != roomConfirmed {
		t.Fatalf("room 最终列表应只含新标识的确认结果: %+v", roomFinal)
	}
}
