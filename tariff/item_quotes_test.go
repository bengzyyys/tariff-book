package tariff

import (
	"errors"
	"testing"
)

// 按费率项查看已受理报价：确认与拒绝都列出，按首次受理时刻排序，
// 时刻相同按请求标识字符串顺序，金额保留首次结果、不按当前费率重算。
func TestItemQuotesConfirmedAndRejected(t *testing.T) {
	now, setNow := fixedClock(at(0))
	b := NewBook(WithClock(now))
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 150, Start: at(0), End: atPtr(100)})

	// 旧版有效期内确认：150 分 × 4 = 600 分
	setNow(at(10))
	confirmed, err := b.Quote(QuoteRequest{RequestID: "q-confirmed", ItemID: "seat", VersionID: "v1", Quantity: 4})
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("quote: %v %+v", err, confirmed)
	}

	// 新版 180 分替代旧版
	must(RegisterRequest{ItemID: "seat", VersionID: "v2", UnitPrice: 180, Start: at(40), Replaces: "v1"})

	// 交接后新请求引用旧版，因失效被拒绝
	setNow(at(50))
	expired, err := b.Quote(QuoteRequest{RequestID: "q-expired", ItemID: "seat", VersionID: "v1", Quantity: 4})
	if err != nil {
		t.Fatal(err)
	}
	if expired.Confirmed || expired.Reason != ReasonVersionExpired {
		t.Fatalf("want expired rejection: %+v", expired)
	}

	// 空标识的拒绝不保存，不应列入结果
	if _, err := b.Quote(QuoteRequest{ItemID: "seat", VersionID: "v2", Quantity: 1}); err != nil {
		t.Fatal(err)
	}

	outs, err := b.ItemQuotes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 2 {
		t.Fatalf("want 2 records, got %d: %+v", len(outs), outs)
	}

	// 按首次受理时刻从早到晚：先确认、后拒绝
	first, second := outs[0], outs[1]
	if first.Request.RequestID != "q-confirmed" || second.Request.RequestID != "q-expired" {
		t.Fatalf("unexpected order: %s, %s", first.Request.RequestID, second.Request.RequestID)
	}

	// 确认记录仍显示旧版、150 分和 600 分，不能拿当前 180 分重算
	if !first.Confirmed || first.Request.VersionID != "v1" || first.UnitPrice != 150 || first.Total != 600 {
		t.Fatalf("confirmed record rewritten: %+v", first)
	}
	if !first.AcceptedAt.Equal(at(10)) {
		t.Fatalf("acceptance time: %v", first.AcceptedAt)
	}

	// 拒绝记录显示旧版及 version_expired；零金额不是免费确认价
	if second.Confirmed || second.Reason != ReasonVersionExpired || second.Request.VersionID != "v1" {
		t.Fatalf("rejection record: %+v", second)
	}
	if second.UnitPrice != 0 || second.Total != 0 {
		t.Fatalf("rejection must carry no amounts: %+v", second)
	}
}

// 时刻相同时按请求标识的字符串顺序排列；同一份首次结果只出现一次。
func TestItemQuotesOrderingAndUniqueness(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 1, Start: at(0)}); err != nil {
		t.Fatal(err)
	}
	// 同一受理时刻提交三笔，标识乱序
	for _, id := range []string{"q-c", "q-a", "q-b"} {
		if _, err := b.Quote(QuoteRequest{RequestID: id, ItemID: "seat", VersionID: "v1", Quantity: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// 原样重试与内容冲突都不产生新记录
	if _, err := b.Quote(QuoteRequest{RequestID: "q-a", ItemID: "seat", VersionID: "v1", Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Quote(QuoteRequest{RequestID: "q-b", ItemID: "seat", VersionID: "v1", Quantity: 9}); !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("want conflict: %v", err)
	}

	outs, err := b.ItemQuotes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 3 {
		t.Fatalf("retries/conflicts must not add records, got %d", len(outs))
	}
	for i, id := range []string{"q-a", "q-b", "q-c"} {
		if outs[i].Request.RequestID != id {
			t.Fatalf("position %d: want %s, got %s", i, id, outs[i].Request.RequestID)
		}
	}
}

// 其他费率项即使用了同名版本也不能混入。
func TestItemQuotesCrossItemIsolation(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	for _, item := range []string{"seat", "room"} {
		if err := b.RegisterVersion(RegisterRequest{ItemID: item, VersionID: "v1", UnitPrice: 1, Start: at(0)}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Quote(QuoteRequest{RequestID: "q-" + item, ItemID: item, VersionID: "v1", Quantity: 1}); err != nil {
			t.Fatal(err)
		}
	}

	outs, err := b.ItemQuotes("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0].Request.ItemID != "seat" || outs[0].Request.RequestID != "q-seat" {
		t.Fatalf("other item leaked in: %+v", outs)
	}
}

// 费率项从未登记过版本，但已有合法数量的请求因 version_not_found 被拒绝并保存，
// 这份拒绝同样能查到，不能因为没有费率版本而让查询失败。
func TestItemQuotesItemWithoutVersions(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	rej, err := b.Quote(QuoteRequest{RequestID: "q-ghost", ItemID: "ghost", VersionID: "v1", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rej.Confirmed || rej.Reason != ReasonVersionNotFound {
		t.Fatalf("want not-found rejection: %+v", rej)
	}

	outs, err := b.ItemQuotes("ghost")
	if err != nil {
		t.Fatalf("missing versions must not fail the query: %v", err)
	}
	if len(outs) != 1 || outs[0] != rej {
		t.Fatalf("saved rejection not listed: %+v", outs)
	}
}

// 非空标识无匹配记录返回空列表且不报错；空标识返回可明确识别的输入错误。
func TestItemQuotesEmptyAndBlank(t *testing.T) {
	b := NewBook()
	outs, err := b.ItemQuotes("never-seen")
	if err != nil {
		t.Fatalf("no matches must not be an error: %v", err)
	}
	if outs == nil || len(outs) != 0 {
		t.Fatalf("want empty non-nil list, got %#v", outs)
	}
	if _, err := b.ItemQuotes(""); !errors.Is(err, ErrEmptyItemID) {
		t.Fatalf("blank item id: %v", err)
	}
}

// 返回的列表和记录独立于账本：调用方改动后，单笔查询、原样重试和
// 再次列表查询仍返回原来的结果；列表查询本身不受理报价、不占用标识。
func TestItemQuotesResultsIndependent(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 100, Start: at(0)}); err != nil {
		t.Fatal(err)
	}
	first, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 3})
	if err != nil || !first.Confirmed {
		t.Fatalf("quote: %v %+v", err, first)
	}

	outs, err := b.ItemQuotes("seat")
	if err != nil || len(outs) != 1 {
		t.Fatalf("list: %v %+v", err, outs)
	}
	// 调用方改写返回的记录与列表
	outs[0].Confirmed = false
	outs[0].UnitPrice = 0
	outs[0].Total = 0
	outs[0].Reason = ReasonVersionExpired
	outs[0].Request.ItemID = "tampered"
	outs[0].Request.Quantity = 999

	// 单笔查询仍返回原记录
	got, err := b.Lookup("q1")
	if err != nil || got != first {
		t.Fatalf("lookup after mutation: %+v (%v)", got, err)
	}
	// 原样重试仍返回首次结果
	replay, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 3})
	if err != nil || replay != first {
		t.Fatalf("replay after mutation: %+v (%v)", replay, err)
	}
	// 再次列表查询仍返回原来的结果
	again, err := b.ItemQuotes("seat")
	if err != nil || len(again) != 1 || again[0] != first {
		t.Fatalf("re-list after mutation: %+v (%v)", again, err)
	}

	// 列表查询是只读的：不为篡改出的标识留下记录
	if _, err := b.Lookup("tampered"); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("list query must not reserve ids: %v", err)
	}
}
