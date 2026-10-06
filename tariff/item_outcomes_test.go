package tariff

import (
	"errors"
	"math"
	"testing"
)

// TestItemOutcomesListsConfirmationsAndRejections 覆盖按费率项查看已受理报价的
// 核心场景：确认与拒绝都列出；旧版确认价不被当前费率重算；
// 失效后引用旧版的拒绝与旧确认同时保留；排序按首次受理时刻、再按请求标识。
func TestItemOutcomesListsConfirmationsAndRejections(t *testing.T) {
	now, setNow := fixedClock(at(1))
	b := NewBook(WithClock(now))
	mustReg := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	mustReg(RegisterRequest{ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150, Start: at(0), End: atPtr(100)})
	mustReg(RegisterRequest{ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180, Start: at(10), Replaces: "seat-v1"})

	// ① 旧版按 150 分、数量 4 确认 600 分。
	setNow(at(2))
	confirmed, err := b.Quote(QuoteRequest{RequestID: "q-old-confirmed", ItemID: "seat", VersionID: "seat-v1", Quantity: 4})
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirmed quote: %v %+v", err, confirmed)
	}

	// 交接后新请求引用旧版，因失效被拒绝（零金额不是免费确认价）。
	setNow(at(20))
	expired, err := b.Quote(QuoteRequest{RequestID: "q-old-expired", ItemID: "seat", VersionID: "seat-v1", Quantity: 4})
	if err != nil {
		t.Fatal(err)
	}
	if expired.Confirmed || expired.Reason != ReasonVersionExpired {
		t.Fatalf("want version_expired rejection: %+v", expired)
	}

	// 交接后引用新版的确认也应一并列出（受理时刻更晚）。
	setNow(at(21))
	newConfirmed, err := b.Quote(QuoteRequest{RequestID: "q-new-confirmed", ItemID: "seat", VersionID: "seat-v2", Quantity: 4})
	if err != nil || !newConfirmed.Confirmed || newConfirmed.UnitPrice != 180 || newConfirmed.Total != 720 {
		t.Fatalf("new confirmed: %v %+v", err, newConfirmed)
	}

	got, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 outcomes, got %d: %+v", len(got), got)
	}

	// 按首次受理时刻从早到晚排列。
	wantOrder := []string{"q-old-confirmed", "q-old-expired", "q-new-confirmed"}
	for i, id := range wantOrder {
		if got[i].Request.RequestID != id {
			t.Fatalf("position %d: want %s, got %s (full: %v)", i, id, got[i].Request.RequestID, requestIDs(got))
		}
	}

	old := got[0]
	if !old.Confirmed || old.Request.VersionID != "seat-v1" || old.Request.Quantity != 4 ||
		old.UnitPrice != 150 || old.Total != 600 || old.Reason != ReasonNone ||
		!old.AcceptedAt.Equal(at(2)) {
		t.Fatalf("old confirmation must keep first-result values: %+v", old)
	}
	rejected := got[1]
	if rejected.Confirmed || rejected.Reason != ReasonVersionExpired ||
		rejected.Request.VersionID != "seat-v1" || rejected.UnitPrice != 0 || rejected.Total != 0 {
		t.Fatalf("expired rejection must keep version and zero amounts: %+v", rejected)
	}
}

// TestItemOutcomesSameAcceptedAtTieBreak 相同时刻按请求标识字符串顺序排列。
func TestItemOutcomesSameAcceptedAtTieBreak(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 10, Start: at(0)}); err != nil {
		t.Fatal(err)
	}
	// 故意乱序提交，验证结果不依赖 map 遍历或提交顺序。
	for _, id := range []string{"q-c", "q-a", "q-b"} {
		if _, err := b.Quote(QuoteRequest{RequestID: id, ItemID: "seat", VersionID: "v1", Quantity: 1}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if ids := requestIDs(got); len(ids) != 3 || ids[0] != "q-a" || ids[1] != "q-b" || ids[2] != "q-c" {
		t.Fatalf("tie-break by request id: %v", ids)
	}
}

// TestItemOutcomesReplayAndConflictAddNoRecords 原样重试与内容冲突都不算新记录。
func TestItemOutcomesReplayAndConflictAddNoRecords(t *testing.T) {
	now, setNow := fixedClock(at(1))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 10, Start: at(0), End: atPtr(100)}); err != nil {
		t.Fatal(err)
	}
	first, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}

	// 时钟推进后原样重试：仍是首次结果，列表不新增。
	setNow(at(50))
	replay, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 2})
	if err != nil || replay != first {
		t.Fatalf("idempotent replay changed: %v %+v vs %+v", err, replay, first)
	}

	// 相同标识、不同内容是调用错误，不留任何记录。
	if _, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 9}); !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("want conflict, got %v", err)
	}

	got, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != first {
		t.Fatalf("replay/conflict must not add records: %+v", got)
	}
}

// TestItemOutcomesExcludesEmptyRequestID 空请求标识的拒绝不保存、不列入。
func TestItemOutcomesExcludesEmptyRequestID(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 10, Start: at(0)}); err != nil {
		t.Fatal(err)
	}
	empty, err := b.Quote(QuoteRequest{ItemID: "seat", VersionID: "v1", Quantity: 1})
	if err != nil || empty.Confirmed || empty.Reason != ReasonEmptyRequestID {
		t.Fatalf("empty-id quote: %v %+v", err, empty)
	}
	saved, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != saved {
		t.Fatalf("empty-id rejection must not be listed: %+v", got)
	}
}

// TestItemOutcomesIsolatesItems 只匹配已保存的请求来源；其他费率项即使用同名版本也不混入。
func TestItemOutcomesIsolatesItems(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	for _, item := range []string{"seat", "room"} {
		if err := b.RegisterVersion(RegisterRequest{ItemID: item, VersionID: "v1", UnitPrice: 10, Start: at(0)}); err != nil {
			t.Fatal(err)
		}
	}
	seatOut, err := b.Quote(QuoteRequest{RequestID: "seat-q", ItemID: "seat", VersionID: "v1", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	roomOut, err := b.Quote(QuoteRequest{RequestID: "room-q", ItemID: "room", VersionID: "v1", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}

	seatGot, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(seatGot) != 1 || seatGot[0] != seatOut || seatGot[0].Request.ItemID != "seat" {
		t.Fatalf("seat list leaked across items: %+v", seatGot)
	}
	roomGot, err := b.ItemOutcomes("room")
	if err != nil {
		t.Fatal(err)
	}
	if len(roomGot) != 1 || roomGot[0] != roomOut || roomGot[0].Request.ItemID != "room" {
		t.Fatalf("room list leaked across items: %+v", roomGot)
	}
}

// TestItemOutcomesItemWithoutVersions 费率项从未登记过版本，合法数量请求因
// version_not_found 被拒绝时，这份拒绝仍能按费率项查到。
func TestItemOutcomesItemWithoutVersions(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	out, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "ghost-item", VersionID: "any", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Confirmed || out.Reason != ReasonVersionNotFound {
		t.Fatalf("want version_not_found: %+v", out)
	}
	got, err := b.ItemOutcomes("ghost-item")
	if err != nil {
		t.Fatalf("unregistered item must not error: %v", err)
	}
	if len(got) != 1 || got[0] != out {
		t.Fatalf("version_not_found rejection must be listed: %+v", got)
	}
}

// TestItemOutcomesUnknownItemReturnsEmpty 非空但无任何匹配记录的费率项返回空列表、不报错。
func TestItemOutcomesUnknownItemReturnsEmpty(t *testing.T) {
	b := NewBook()
	got, err := b.ItemOutcomes("never-used")
	if err != nil {
		t.Fatalf("unknown item must not be an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty list, got %+v", got)
	}

	// 该项登记过版本但没有任何报价时，同样返回空列表而非 ErrItemNotFound。
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 1, Start: at(0)}); err != nil {
		t.Fatal(err)
	}
	got, err = b.ItemOutcomes("seat")
	if err != nil {
		t.Fatalf("registered-but-never-quoted item: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("want non-nil empty list, got %+v", got)
	}
}

// TestItemOutcomesEmptyItemID 空费率项标识返回可明确识别的输入错误。
func TestItemOutcomesEmptyItemID(t *testing.T) {
	b := NewBook()
	got, err := b.ItemOutcomes("")
	if !errors.Is(err, ErrEmptyItemIDQuery) {
		t.Fatalf("want ErrEmptyItemIDQuery, got %v", err)
	}
	if got != nil {
		t.Fatalf("error result must carry no list: %+v", got)
	}
}

// TestItemOutcomesIndependentCopies 返回的列表与记录独立于账本：
// 调用方改写后，单笔查询、原样重试、再次列表都仍是原始结果。
func TestItemOutcomesIndependentCopies(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 150, Start: at(0)}); err != nil {
		t.Fatal(err)
	}
	first, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 4})
	if err != nil {
		t.Fatal(err)
	}

	list, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != first {
		t.Fatalf("unexpected list: %+v vs %+v", list, first)
	}

	// 调用方破坏性改写返回记录。
	list[0].Confirmed = false
	list[0].UnitPrice = 999
	list[0].Total = math.MaxInt64
	list[0].Reason = ReasonVersionExpired
	list[0].AcceptedAt = at(999)
	list[0].Request.RequestID = "tampered"
	list[0].Request.ItemID = "other-item"
	list[0].Request.VersionID = "v9"
	list[0].Request.Quantity = -7

	// 单笔查询仍是原始结果。
	if got, err := b.Lookup("q1"); err != nil || got != first {
		t.Fatalf("Lookup changed through list mutation: %v %+v", err, got)
	}
	// 原样重试仍是原始结果。
	if replay, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 4}); err != nil || replay != first {
		t.Fatalf("replay changed through list mutation: %v %+v", err, replay)
	}
	// 再次列表仍是原始结果（数量仍为 1，且仍属于 seat）。
	again, err := b.ItemOutcomes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0] != first {
		t.Fatalf("second list changed through first list mutation: %+v vs %+v", again, first)
	}
}

// TestItemOutcomesDoesNotOccupyRequestID 只读查询不受理报价、不占用请求标识。
func TestItemOutcomesDoesNotOccupyRequestID(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 10, Start: at(0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ItemOutcomes("seat"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ItemOutcomes("seat"); err != nil {
		t.Fatal(err)
	}
	// 查询之后首次报价仍正常受理，说明没有产生或占用任何记录。
	out, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 3})
	if err != nil || !out.Confirmed || out.Total != 30 {
		t.Fatalf("quote after list queries: %v %+v", err, out)
	}
	got, err := b.ItemOutcomes("seat")
	if err != nil || len(got) != 1 || got[0] != out {
		t.Fatalf("list after quote: %v %+v", err, got)
	}
}

func requestIDs(outcomes []Outcome) []string {
	ids := make([]string, len(outcomes))
	for i, o := range outcomes {
		ids[i] = o.Request.RequestID
	}
	return ids
}
