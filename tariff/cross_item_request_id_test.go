package tariff

import (
	"errors"
	"testing"
)

// 本文件守护跨费率项复用请求标识的既有规则：费率项各自允许使用同名版本，
// 但非空报价请求标识属于整本账本。首次受理（无论确认还是拒绝）之后，
// 同一标识改报另一费率项，即使版本标识与数量完全相同，也必须返回
// ErrRequestIDConflict：不能按另一项再确认一个价格，也不能把原结果当成
// 另一项的报价返回。冲突不形成任何新记录，首次结果保持原值可查。

// 两项的 v1 都已生效：seat/v1 单价 150 分，room/v1 单价 300 分。
// 先用某标识给 seat 报数量 4 得到 600 分确认价；沿用该标识改报 room
// （版本、数量仍为 v1、4）必须发生请求内容冲突，且首次结果原样保留。
func TestCrossItemRequestIDConflictAfterConfirmation(t *testing.T) {
	now, setNow := fixedClock(at(1))
	b := NewBook(WithClock(now))
	mustReg := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	mustReg(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 150, Start: at(0)})
	mustReg(RegisterRequest{ItemID: "room", VersionID: "v1", UnitPrice: 300, Start: at(0)})

	// 首次受理：seat/v1 数量 4，按 150 分确认 600 分。
	seatReq := QuoteRequest{RequestID: "shared-id", ItemID: "seat", VersionID: "v1", Quantity: 4}
	first, err := b.Quote(seatReq)
	if err != nil {
		t.Fatalf("首次报价不应返回错误: %v", err)
	}
	if !first.Confirmed || first.Reason != ReasonNone {
		t.Fatalf("首次报价应确认: %+v", first)
	}
	if first.UnitPrice != 150 || first.Total != 600 {
		t.Fatalf("首次确认价应为 150x4=600: 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	if first.Request != seatReq || !first.AcceptedAt.Equal(at(1)) {
		t.Fatalf("首次结果来源或受理时刻异常: %+v", first)
	}

	// 沿用同一标识改报 room，版本与数量完全相同：必须是调用错误
	// ErrRequestIDConflict，不能按 room 的 300 分再确认 1200 分。
	setNow(at(2))
	conflict, err := b.Quote(QuoteRequest{RequestID: "shared-id", ItemID: "room", VersionID: "v1", Quantity: 4})
	if !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("跨费率项复用标识必须返回 ErrRequestIDConflict, got err=%v out=%+v", err, conflict)
	}
	if conflict != (Outcome{}) {
		t.Fatalf("冲突时返回的报价结果应为零值，不形成 room 的受理记录: %+v", conflict)
	}

	// 通过原标识查询仍取回 seat 的首次确认结果，全部字段保持原值。
	got, err := b.Lookup("shared-id")
	if err != nil {
		t.Fatalf("原标识查询不应报错: %v", err)
	}
	if got != first {
		t.Fatalf("冲突后原标识查询必须仍是 seat 首次结果: %+v vs %+v", got, first)
	}
	if got.Request.ItemID != "seat" || got.Request.VersionID != "v1" || got.Request.Quantity != 4 ||
		!got.Confirmed || got.UnitPrice != 150 || got.Total != 600 ||
		got.Reason != ReasonNone || !got.AcceptedAt.Equal(at(1)) {
		t.Fatalf("首次结果的费率项/版本/数量/单价/总价/确认状态/受理时刻必须保持原值: %+v", got)
	}

	// 用原标识、原内容重试（时钟已推进）仍返回 seat 首次结果，不是错误也不是新报价。
	setNow(at(3))
	replay, err := b.Quote(seatReq)
	if err != nil {
		t.Fatalf("原内容重试不应报错: %v", err)
	}
	if replay != first {
		t.Fatalf("原内容重试必须返回首次结果: %+v vs %+v", replay, first)
	}

	// 按费率项查看：首次结果只出现在 seat 的列表里，冲突不给任何一项增加记录；
	// room 没有其他受理记录，应得到空列表且不报错。
	seatList, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatList) != 1 || seatList[0] != first {
		t.Fatalf("seat 列表应只含首次确认结果: %+v", seatList)
	}
	roomList, err := b.ItemOutcomes("room")
	if err != nil {
		t.Fatalf("room 无受理记录时不应报错: %v", err)
	}
	if len(roomList) != 0 {
		t.Fatalf("冲突不能给 room 增加任何受理记录: %+v", roomList)
	}

	// room 改用全新标识提交相同内容（v1、数量 4）：正常确认 1200 分。
	roomReq := QuoteRequest{RequestID: "room-own-id", ItemID: "room", VersionID: "v1", Quantity: 4}
	roomOut, err := b.Quote(roomReq)
	if err != nil {
		t.Fatalf("room 用全新标识报价不应报错: %v", err)
	}
	if !roomOut.Confirmed || roomOut.Reason != ReasonNone {
		t.Fatalf("room 用全新标识应正常确认: %+v", roomOut)
	}
	if roomOut.UnitPrice != 300 || roomOut.Total != 1200 {
		t.Fatalf("room 应按自己的 300 分单价确认 1200: 单价=%d 总价=%d", roomOut.UnitPrice, roomOut.Total)
	}
	if roomOut.Request != roomReq {
		t.Fatalf("room 确认结果来源必须保持请求内容: %+v vs %+v", roomOut.Request, roomReq)
	}

	// room 能以自己的标识查询；seat 原有结果不受影响。
	roomGot, err := b.Lookup("room-own-id")
	if err != nil || roomGot != roomOut {
		t.Fatalf("room 应能以自己的标识查询: %v %+v", err, roomGot)
	}
	seatAgain, err := b.Lookup("shared-id")
	if err != nil || seatAgain != first {
		t.Fatalf("seat 原有结果不应受 room 新报价影响: %v %+v", err, seatAgain)
	}
	seatList, err = b.ItemOutcomes("seat")
	if err != nil || len(seatList) != 1 || seatList[0] != first {
		t.Fatalf("seat 列表不应受 room 新报价影响: %v %+v", err, seatList)
	}
	roomList, err = b.ItemOutcomes("room")
	if err != nil || len(roomList) != 1 || roomList[0] != roomOut {
		t.Fatalf("room 列表应只含自己标识的确认结果: %v %+v", err, roomList)
	}
}

// 首次结果被拒绝时同样占用标识：seat/v1 尚未生效、room/v1 已生效的时刻，
// 用一个未用过的非空标识先向 seat 提交数量 4，得到 version_not_yet_effective
// 拒绝（调用错误为空）；再用该标识向 room 提交相同版本与数量，仍必须是
// ErrRequestIDConflict——不能因为前一笔没有确认价就允许重新占用标识。
func TestCrossItemRequestIDConflictAfterRejection(t *testing.T) {
	now, setNow := fixedClock(at(1))
	b := NewBook(WithClock(now))
	// seat/v1 在 at(100) 才生效；room/v1 从 at(0) 起持续有效。
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 150, Start: at(100)}); err != nil {
		t.Fatalf("register seat/v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{ItemID: "room", VersionID: "v1", UnitPrice: 300, Start: at(0)}); err != nil {
		t.Fatalf("register room/v1: %v", err)
	}

	// 首次受理：seat/v1 尚未生效，正常受理（err 为空）但拒绝，金额为零。
	seatReq := QuoteRequest{RequestID: "rejected-shared-id", ItemID: "seat", VersionID: "v1", Quantity: 4}
	first, err := b.Quote(seatReq)
	if err != nil {
		t.Fatalf("版本尚未生效的拒绝是正常受理，err 应为空: %v", err)
	}
	if first.Confirmed {
		t.Fatalf("seat/v1 尚未生效，必须拒绝: %+v", first)
	}
	if first.Reason != ReasonVersionNotYetEffective {
		t.Fatalf("拒绝原因=%q, want %q", first.Reason, ReasonVersionNotYetEffective)
	}
	if first.UnitPrice != 0 || first.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应为零: %d/%d", first.UnitPrice, first.Total)
	}
	if first.Request != seatReq || !first.AcceptedAt.Equal(at(1)) {
		t.Fatalf("拒绝结果来源或受理时刻异常: %+v", first)
	}

	// 同一标识改报 room（版本、数量相同）：拒绝也占用标识，必须冲突。
	setNow(at(2))
	conflict, err := b.Quote(QuoteRequest{RequestID: "rejected-shared-id", ItemID: "room", VersionID: "v1", Quantity: 4})
	if !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("前一笔虽被拒绝，标识仍被占用: want ErrRequestIDConflict, got err=%v out=%+v", err, conflict)
	}
	if conflict != (Outcome{}) {
		t.Fatalf("冲突时返回的报价结果应为零值: %+v", conflict)
	}

	// 原标识查询仍保留首次拒绝及其来源（seat/v1），不是 room 的结果。
	got, err := b.Lookup("rejected-shared-id")
	if err != nil {
		t.Fatalf("原标识查询不应报错: %v", err)
	}
	if got != first {
		t.Fatalf("原标识查询必须仍是首次拒绝结果: %+v vs %+v", got, first)
	}
	if got.Request.ItemID != "seat" || got.Request.VersionID != "v1" || got.Request.Quantity != 4 ||
		got.Confirmed || got.Reason != ReasonVersionNotYetEffective ||
		got.UnitPrice != 0 || got.Total != 0 || !got.AcceptedAt.Equal(at(1)) {
		t.Fatalf("首次拒绝的来源与内容必须保持原值: %+v", got)
	}

	// 原内容重试仍返回首次拒绝（即使时钟推进后 seat/v1 仍未生效，也以首次结果为准）。
	replay, err := b.Quote(seatReq)
	if err != nil || replay != first {
		t.Fatalf("原内容重试必须返回首次拒绝结果: %v %+v vs %+v", err, replay, first)
	}

	// 按费率项查看：首次拒绝只出现在 seat 的列表里；room 没有任何受理记录。
	seatList, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatList) != 1 || seatList[0] != first {
		t.Fatalf("seat 列表应只含首次拒绝结果: %+v", seatList)
	}
	roomList, err := b.ItemOutcomes("room")
	if err != nil {
		t.Fatalf("room 无受理记录时不应报错: %v", err)
	}
	if len(roomList) != 0 {
		t.Fatalf("冲突不能给 room 增加任何受理记录: %+v", roomList)
	}

	// room 改用全新标识提交相同内容：正常确认 1200 分，不影响 seat 的拒绝记录。
	roomOut, err := b.Quote(QuoteRequest{RequestID: "room-fresh-id", ItemID: "room", VersionID: "v1", Quantity: 4})
	if err != nil {
		t.Fatalf("room 用全新标识报价不应报错: %v", err)
	}
	if !roomOut.Confirmed || roomOut.UnitPrice != 300 || roomOut.Total != 1200 || roomOut.Reason != ReasonNone {
		t.Fatalf("room 应确认 1200 分: %+v", roomOut)
	}
	seatAgain, err := b.Lookup("rejected-shared-id")
	if err != nil || seatAgain != first {
		t.Fatalf("seat 的拒绝记录不应受 room 新报价影响: %v %+v", err, seatAgain)
	}
	seatList, err = b.ItemOutcomes("seat")
	if err != nil || len(seatList) != 1 || seatList[0] != first {
		t.Fatalf("seat 列表不应受 room 新报价影响: %v %+v", err, seatList)
	}
}
