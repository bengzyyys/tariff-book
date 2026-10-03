package tariff

import (
	"errors"
	"sync"
	"testing"
)

// 并发提交同一请求标识的不同数量时，收集每个调用方得到的结果。
type quoteResult struct {
	outcome Outcome
	err     error
}

// raceQuantities 用 quantities 中每个数量各起 callers 个调用方，
// 同时使用同一请求标识报价，返回按数量分组的结果。
func raceQuantities(b *Book, req QuoteRequest, callers int, quantities ...int64) map[int64][]quoteResult {
	results := make(map[int64][]quoteResult, len(quantities))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, qty := range quantities {
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func(qty int64) {
				defer wg.Done()
				r := req
				r.Quantity = qty
				out, err := b.Quote(r)
				mu.Lock()
				results[qty] = append(results[qty], quoteResult{out, err})
				mu.Unlock()
			}(qty)
		}
	}
	wg.Wait()
	return results
}

// 同一非空请求标识被不同数量的调用方同时使用时，首次受理规则：
// 最终只保存其中一种数量的结果（2→14 分或 5→35 分，单价 7 分），
// 与首次内容相同的调用方都取回完全一致的首次结果，
// 数量不同的调用方都得到 ErrRequestIDConflict，且不产生各自的确认价。
// 两种数量都允许先被受理，检查可重复执行。
func TestConcurrentQuantityConflict(t *testing.T) {
	const (
		itemID    = "seat"
		versionID = "v1"
		unitPrice = 7
		requestID = "race-qty"
		callers   = 8 // 每种数量的并发调用方数
	)
	quantities := []int64{2, 5}

	// 记录哪些数量曾经先被受理；不要求某个固定数量必然先取得结果。
	won := make(map[int64]bool)

	for round := 0; round < 50; round++ {
		now, setNow := fixedClock(at(5))
		b := NewBook(WithClock(now))
		if err := b.RegisterVersion(RegisterRequest{
			ItemID: itemID, VersionID: versionID, UnitPrice: unitPrice, Start: at(0),
		}); err != nil {
			t.Fatal(err)
		}
		req := QuoteRequest{RequestID: requestID, ItemID: itemID, VersionID: versionID}

		results := raceQuantities(b, req, callers, quantities...)

		// 账本最终保存的首次结果只能属于其中一种数量。
		stored, err := b.Lookup(requestID)
		if err != nil {
			t.Fatalf("round %d: lookup: %v", round, err)
		}
		winQty := stored.Request.Quantity
		if winQty != quantities[0] && winQty != quantities[1] {
			t.Fatalf("round %d: stored quantity %d is not one of %v", round, winQty, quantities)
		}
		won[winQty] = true

		// 保存内容的费率项、版本、数量、单价、总价与受理时刻必须互相对应。
		if !stored.Confirmed {
			t.Fatalf("round %d: stored outcome not confirmed: %+v", round, stored)
		}
		if stored.Request.ItemID != itemID || stored.Request.VersionID != versionID {
			t.Fatalf("round %d: stored source mismatch: %+v", round, stored.Request)
		}
		if stored.UnitPrice != unitPrice || stored.Total != unitPrice*winQty {
			t.Fatalf("round %d: stored amounts inconsistent: qty=%d price=%d total=%d",
				round, winQty, stored.UnitPrice, stored.Total)
		}
		if !stored.AcceptedAt.Equal(at(5)) {
			t.Fatalf("round %d: stored acceptance time: %v", round, stored.AcceptedAt)
		}

		for _, qty := range quantities {
			for i, res := range results[qty] {
				if qty == winQty {
					// 与首次内容相同：正常返回完全一致的首次结果。
					if res.err != nil {
						t.Fatalf("round %d: same-content caller %d (qty %d): %v", round, i, qty, res.err)
					}
					if res.outcome != stored {
						t.Fatalf("round %d: same-content caller %d (qty %d) diverged: %+v vs %+v",
							round, i, qty, res.outcome, stored)
					}
				} else {
					// 数量不同：只能得到标识冲突，不能各自生成确认价。
					if !errors.Is(res.err, ErrRequestIDConflict) {
						t.Fatalf("round %d: conflicting caller %d (qty %d): want ErrRequestIDConflict, got %v",
							round, i, qty, res.err)
					}
					if res.outcome != (Outcome{}) {
						t.Fatalf("round %d: conflicting caller %d (qty %d) produced an outcome: %+v",
							round, i, qty, res.outcome)
					}
				}
			}
		}

		// 并发结束后：按标识查询与原样重试都取回同一份首次结果。
		replay, err := b.Quote(QuoteRequest{RequestID: requestID, ItemID: itemID, VersionID: versionID, Quantity: winQty})
		if err != nil || replay != stored {
			t.Fatalf("round %d: replay of first content: %+v (%v), stored %+v", round, replay, err, stored)
		}
		loseQty := quantities[0] + quantities[1] - winQty
		if _, err := b.Quote(QuoteRequest{RequestID: requestID, ItemID: itemID, VersionID: versionID, Quantity: loseQty}); !errors.Is(err, ErrRequestIDConflict) {
			t.Fatalf("round %d: late conflicting quantity %d: %v", round, loseQty, err)
		}
		if got, _ := b.Lookup(requestID); got != stored {
			t.Fatalf("round %d: stored record changed after conflicts: %+v -> %+v", round, stored, got)
		}

		// 延后再次提交不改写受理时刻。
		setNow(at(50))
		late, err := b.Quote(QuoteRequest{RequestID: requestID, ItemID: itemID, VersionID: versionID, Quantity: winQty})
		if err != nil || late != stored {
			t.Fatalf("round %d: delayed replay rewrote the record: %+v -> %+v", round, stored, late)
		}
		if !late.AcceptedAt.Equal(at(5)) {
			t.Fatalf("round %d: acceptance time rewritten: %v", round, late.AcceptedAt)
		}
	}

	if len(won) == 0 {
		t.Fatal("no quantity was ever accepted")
	}
	t.Logf("先被受理的数量分布: %v", won)
}

// 首次拒绝同样占用请求标识：数量为 0 与数量为 2 同时使用同一标识。
// 若 0 先被受理，保存 invalid_quantity 拒绝（未确认、单价与总价为 0、调用无错误），
// 2 只能得到标识冲突；若 2 先被受理，保存 14 分确认，0 只能得到冲突。
// 两种顺序都允许，拒绝记录同样保留实际首次请求与受理时刻。
func TestConcurrentFirstRejectionHoldsID(t *testing.T) {
	const (
		itemID    = "seat"
		versionID = "v1"
		unitPrice = 7
		requestID = "race-reject"
		callers   = 8
	)
	quantities := []int64{0, 2}

	won := make(map[int64]bool)

	for round := 0; round < 50; round++ {
		now, setNow := fixedClock(at(5))
		b := NewBook(WithClock(now))
		if err := b.RegisterVersion(RegisterRequest{
			ItemID: itemID, VersionID: versionID, UnitPrice: unitPrice, Start: at(0),
		}); err != nil {
			t.Fatal(err)
		}
		req := QuoteRequest{RequestID: requestID, ItemID: itemID, VersionID: versionID}

		results := raceQuantities(b, req, callers, quantities...)

		stored, err := b.Lookup(requestID)
		if err != nil {
			t.Fatalf("round %d: lookup: %v", round, err)
		}
		winQty := stored.Request.Quantity
		if winQty != quantities[0] && winQty != quantities[1] {
			t.Fatalf("round %d: stored quantity %d is not one of %v", round, winQty, quantities)
		}
		won[winQty] = true

		// 保存的记录必须与实际首次请求和受理时刻对应。
		if stored.Request.ItemID != itemID || stored.Request.VersionID != versionID {
			t.Fatalf("round %d: stored source mismatch: %+v", round, stored.Request)
		}
		if !stored.AcceptedAt.Equal(at(5)) {
			t.Fatalf("round %d: stored acceptance time: %v", round, stored.AcceptedAt)
		}
		switch winQty {
		case 0:
			// 首次受理的是非法数量：保存 invalid_quantity 拒绝。
			if stored.Confirmed {
				t.Fatalf("round %d: invalid quantity must not be confirmed: %+v", round, stored)
			}
			if stored.Reason != ReasonInvalidQuantity {
				t.Fatalf("round %d: reason=%q, want %q", round, stored.Reason, ReasonInvalidQuantity)
			}
			if stored.UnitPrice != 0 || stored.Total != 0 {
				t.Fatalf("round %d: rejected amounts must be 0: price=%d total=%d",
					round, stored.UnitPrice, stored.Total)
			}
		case 2:
			// 首次受理的是合法数量：保存 14 分确认。
			if !stored.Confirmed || stored.UnitPrice != unitPrice || stored.Total != 14 {
				t.Fatalf("round %d: want confirmed 14-cent quote, got %+v", round, stored)
			}
			if stored.Reason != ReasonNone {
				t.Fatalf("round %d: reason on confirmed quote: %q", round, stored.Reason)
			}
		}

		for _, qty := range quantities {
			for i, res := range results[qty] {
				if qty == winQty {
					// 与首次内容相同（包括首次拒绝）：正常受理，调用本身没有错误。
					if res.err != nil {
						t.Fatalf("round %d: same-content caller %d (qty %d): %v", round, i, qty, res.err)
					}
					if res.outcome != stored {
						t.Fatalf("round %d: same-content caller %d (qty %d) diverged: %+v vs %+v",
							round, i, qty, res.outcome, stored)
					}
				} else {
					// 数量不同：只能得到标识冲突，不能补成确认报价或改写拒绝。
					if !errors.Is(res.err, ErrRequestIDConflict) {
						t.Fatalf("round %d: conflicting caller %d (qty %d): want ErrRequestIDConflict, got %v",
							round, i, qty, res.err)
					}
					if res.outcome != (Outcome{}) {
						t.Fatalf("round %d: conflicting caller %d (qty %d) produced an outcome: %+v",
							round, i, qty, res.outcome)
					}
				}
			}
		}

		// 并发结束后：查询与原样重试取回同一份首次结果，换数量仍是冲突。
		replay, err := b.Quote(QuoteRequest{RequestID: requestID, ItemID: itemID, VersionID: versionID, Quantity: winQty})
		if err != nil || replay != stored {
			t.Fatalf("round %d: replay of first content: %+v (%v), stored %+v", round, replay, err, stored)
		}
		loseQty := quantities[0] + quantities[1] - winQty
		if _, err := b.Quote(QuoteRequest{RequestID: requestID, ItemID: itemID, VersionID: versionID, Quantity: loseQty}); !errors.Is(err, ErrRequestIDConflict) {
			t.Fatalf("round %d: late conflicting quantity %d: %v", round, loseQty, err)
		}
		if got, _ := b.Lookup(requestID); got != stored {
			t.Fatalf("round %d: stored record changed after conflicts: %+v -> %+v", round, stored, got)
		}

		// 延后再次提交不改写受理时刻。
		setNow(at(50))
		late, err := b.Quote(QuoteRequest{RequestID: requestID, ItemID: itemID, VersionID: versionID, Quantity: winQty})
		if err != nil || late != stored {
			t.Fatalf("round %d: delayed replay rewrote the record: %+v -> %+v", round, stored, late)
		}
		if !late.AcceptedAt.Equal(at(5)) {
			t.Fatalf("round %d: acceptance time rewritten: %v", round, late.AcceptedAt)
		}
	}

	if len(won) == 0 {
		t.Fatal("no quantity was ever accepted")
	}
	t.Logf("先被受理的数量分布: %v", won)
}
