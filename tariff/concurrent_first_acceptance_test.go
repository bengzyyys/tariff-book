package tariff

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// 同一请求标识被多个调用方同时用于不同数量时的首次受理规则回归测试。
//
// 这些测试只通过公开的 Quote / Lookup 功能与既有错误语义施加负载，
// 不修改费率登记、金额计算或首次受理规则本身：
//
//   - 并发批次中两种数量各占一半，谁先抢到账本的受理权是调度决定的，
//     因此测试不断言某个固定数量必然先被受理，而是按实际首次内容校验；
//   - 与首次内容相同的请求全部原样返回同一份首次结果；
//   - 数量不同的请求全部返回 ErrRequestIDConflict，不产生各自的确认价，
//     也不把后到的数量写入查询记录。
//
// 为让结论可重复执行，批次运行在注入的固定时钟下；批次结束后再推进时钟，
// 验证延后重试不改写首次受理时刻。

const (
	raceItemID    = "seat"
	raceVersionID = "v1"
	raceUnitPrice = int64(7)
	racePerSide   = 32 // 每种数量的并发调用数
)

// raceCall 记录一次并发报价的实际返回及其数量。
type raceCall struct {
	quantity int64
	out      Outcome
	err      error
}

// newRaceBook 准备一本已生效的单价 7 分版本账本，版本自 at(0) 起持续有效，
// 受理时刻初始固定在 at(5)（版本有效期内）。返回账本和推进时钟的函数。
func newRaceBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: raceItemID, VersionID: raceVersionID,
		UnitPrice: raceUnitPrice, Start: at(0),
	}); err != nil {
		t.Fatalf("register %s/%s: %v", raceItemID, raceVersionID, err)
	}
	return b, setNow
}

// raceDifferentQuantities 同时提交 2*racePerSide 个相同标识、相同费率项与版本、
// 仅数量不同的报价（两种数量交替排列）。所有 goroutine 经同一屏障同时放行，
// 返回每一次调用拿到的真实结果。
func raceDifferentQuantities(b *Book, requestID string, qA, qB int64) []raceCall {
	calls := make([]raceCall, 2*racePerSide)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range calls {
		q := qA
		if i%2 == 1 {
			q = qB
		}
		wg.Add(1)
		go func(i int, q int64) {
			defer wg.Done()
			<-start // 全部就绪后一起进入 Quote，制造对同一标识的同时竞争
			out, err := b.Quote(QuoteRequest{
				RequestID: requestID,
				ItemID:    raceItemID,
				VersionID: raceVersionID,
				Quantity:  q,
			})
			calls[i] = raceCall{quantity: q, out: out, err: err}
		}(i, q)
	}
	close(start)
	wg.Wait()
	return calls
}

// checkFirstAcceptanceRace 校验并发批次的结果符合首次受理规则，
// 返回保存的首次结果及其数量。不断言哪种数量先被受理。
func checkFirstAcceptanceRace(t *testing.T, requestID string, qA, qB int64, calls []raceCall) (Outcome, int64) {
	t.Helper()

	var accepted, conflicts []raceCall
	for _, c := range calls {
		if c.err == nil {
			accepted = append(accepted, c)
		} else {
			conflicts = append(conflicts, c)
		}
	}

	// 恰好一半调用（与首次内容相同的那一半）正常受理，另一半全部冲突；
	// 不能两种数量各自产生确认价。
	if want, got := racePerSide, len(accepted); want != got {
		t.Fatalf("正常受理次数=%d，应恰好为 %d（同内容的一半）；冲突 %d 次",
			got, want, len(conflicts))
	}
	if want, got := racePerSide, len(conflicts); want != got {
		t.Fatalf("冲突次数=%d，应恰好为 %d（不同数量的一半）；正常受理 %d 次",
			got, want, len(accepted))
	}

	first := accepted[0].out
	winQ := first.Request.Quantity
	if winQ != qA && winQ != qB {
		t.Fatalf("首次结果数量=%d，应为两种竞争数量 %d/%d 之一", winQ, qA, qB)
	}
	loseQ := qA
	if winQ == qA {
		loseQ = qB
	}

	// 与首次内容相同的调用都必须取回完全一致的首次结果（含首次受理时刻），
	// 数量不同的调用都只能拿到 ErrRequestIDConflict 和零值结果，
	// 不能借冲突路径返回任何可用报价。
	for _, c := range accepted {
		if c.quantity != winQ {
			t.Fatalf("数量 %d 的请求也取得了受理结果，首次数量是 %d：%+v",
				c.quantity, winQ, c.out)
		}
		if c.out != first {
			t.Fatalf("同内容并发调用结果不一致：%+v vs %+v", first, c.out)
		}
	}
	for _, c := range conflicts {
		if !errors.Is(c.err, ErrRequestIDConflict) {
			t.Fatalf("数量 %d 应返回 ErrRequestIDConflict，got %v", c.quantity, c.err)
		}
		if c.quantity != loseQ {
			t.Fatalf("与首次同数量(%d)的请求不应冲突", winQ)
		}
		if c.out != (Outcome{}) {
			t.Fatalf("冲突调用不得携带可用结果：%+v", c.out)
		}
	}

	// 首次结果必须原样保留费率项、版本、标识和数量。
	wantReq := QuoteRequest{RequestID: requestID, ItemID: raceItemID, VersionID: raceVersionID, Quantity: winQ}
	if first.Request != wantReq {
		t.Fatalf("首次结果请求来源=%+v，want %+v", first.Request, wantReq)
	}
	if first.AcceptedAt.IsZero() {
		t.Fatal("首次结果缺少受理时刻")
	}
	return first, winQ
}

// checkStoredFirstResult 校验批次结束后 Lookup 与原样重试取回的是同一份保存结果，
// 换另一数量仍冲突，且延后提交不会改写首次受理时刻。
func checkStoredFirstResult(t *testing.T, b *Book, requestID string, first Outcome, winQ, loseQ int64) {
	t.Helper()
	winReq := QuoteRequest{RequestID: requestID, ItemID: raceItemID, VersionID: raceVersionID, Quantity: winQ}
	loseReq := winReq
	loseReq.Quantity = loseQ

	got, err := b.Lookup(requestID)
	if err != nil {
		t.Fatalf("按标识查询首次结果: %v", err)
	}
	if got != first {
		t.Fatalf("查询未取回保存的首次结果: %+v vs %+v", first, got)
	}

	// 原样再次提交首次内容：返回完全一致的首次结果，受理时刻不变。
	replay, err := b.Quote(winReq)
	if err != nil {
		t.Fatalf("原样重试首次内容: %v", err)
	}
	if replay != first {
		t.Fatalf("原样重试改写了首次结果: %+v vs %+v", first, replay)
	}
	if !replay.AcceptedAt.Equal(first.AcceptedAt) {
		t.Fatalf("延后重试改写了受理时刻: %v vs %v", replay.AcceptedAt, first.AcceptedAt)
	}

	// 换用另一数量仍是标识冲突，保存记录不变。
	if _, err := b.Quote(loseReq); !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("换数量提交应返回 ErrRequestIDConflict, got %v", err)
	}
	again, err := b.Lookup(requestID)
	if err != nil {
		t.Fatalf("冲突后查询: %v", err)
	}
	if again != first {
		t.Fatalf("冲突请求改写了保存记录: %+v vs %+v", first, again)
	}
}

// TestConcurrentConfirmedQuantitiesFirstWins：单价 7 分的有效版本下，
// 同一非空标识同时用于数量 2 和数量 5。
//
// 数量 2 先受理则保存并返回 14 分确认；数量 5 先受理则保存并返回 35 分确认，
// 单价都为 7 分。测试允许任意一种先被受理，两种路径都做完整校验。
// 多轮独立账本重复执行，提高两种调度顺序在多次运行中都被覆盖的机会，
// 但不要求同一轮内固定某一方获胜。
func TestConcurrentConfirmedQuantitiesFirstWins(t *testing.T) {
	const rounds = 8
	winners := map[int64]int{}
	for round := 0; round < rounds; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			b, setNow := newRaceBook(t, at(5))
			id := fmt.Sprintf("race-2-vs-5-%d", round)

			calls := raceDifferentQuantities(b, id, 2, 5)
			first, winQ := checkFirstAcceptanceRace(t, id, 2, 5, calls)
			winners[winQ]++

			// 费率项与版本相同、仅数量不同：确认结果必须与实际首次数量相互对应，
			// 不能返回值属于一种数量而查询内容属于另一种数量。
			if !first.Confirmed {
				t.Fatalf("有效版本下数量 %d 的首次结果应确认: %+v", winQ, first)
			}
			if first.Reason != ReasonNone {
				t.Fatalf("确认结果不应带拒绝原因: %q", first.Reason)
			}
			if first.UnitPrice != raceUnitPrice {
				t.Fatalf("单价=%d，want %d", first.UnitPrice, raceUnitPrice)
			}
			wantTotal := raceUnitPrice * winQ // 数量 2 → 14；数量 5 → 35
			if first.Total != wantTotal {
				t.Fatalf("数量 %d 的总价=%d，want %d", winQ, first.Total, wantTotal)
			}
			if !first.AcceptedAt.Equal(at(5)) {
				t.Fatalf("首次受理时刻=%v，want %v", first.AcceptedAt, at(5))
			}

			// 推进时钟后再查询/重试：仍取回 5 点保存的首次结果。
			setNow(at(50))
			loseQ := int64(2)
			if winQ == 2 {
				loseQ = 5
			}
			checkStoredFirstResult(t, b, id, first, winQ, loseQ)
		})
	}
	t.Logf("各轮首次受理数量分布: %v（两种都允许，测试不依赖固定一方获胜）", winners)
}

// TestConcurrentFirstRejectionAlsoClaimsID 保护“首次拒绝同样占用标识”这一边界：
// 同一非空标识同时用于数量 0（invalid_quantity 拒绝）和数量 2（14 分确认）。
//
//   - 数量 0 先受理：保存 invalid_quantity 拒绝（未确认、单价与总价为 0、调用无错误），
//     数量 2 只能得到标识冲突，不能补成确认报价；
//   - 数量 2 先受理：保存 14 分确认结果，数量 0 只能得到冲突，
//     不能覆盖记录或把它改成非法数量拒绝。
func TestConcurrentFirstRejectionAlsoClaimsID(t *testing.T) {
	const rounds = 8
	winners := map[int64]int{}
	for round := 0; round < rounds; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			b, setNow := newRaceBook(t, at(5))
			id := fmt.Sprintf("race-0-vs-2-%d", round)

			calls := raceDifferentQuantities(b, id, 0, 2)
			first, winQ := checkFirstAcceptanceRace(t, id, 0, 2, calls)
			winners[winQ]++

			// 保存内容只能二选一，且各字段必须与实际首次数量相互对应。
			switch winQ {
			case 0:
				if first.Confirmed {
					t.Fatalf("数量 0 先受理应为拒绝，不能确认: %+v", first)
				}
				if first.Reason != ReasonInvalidQuantity {
					t.Fatalf("拒绝原因=%q，want %q", first.Reason, ReasonInvalidQuantity)
				}
				if first.UnitPrice != 0 || first.Total != 0 {
					t.Fatalf("非法数量拒绝的单价/总价应为 0: %d/%d", first.UnitPrice, first.Total)
				}
			case 2:
				if !first.Confirmed {
					t.Fatalf("数量 2 先受理应确认: %+v", first)
				}
				if first.Reason != ReasonNone {
					t.Fatalf("确认结果不应带拒绝原因: %q", first.Reason)
				}
				if first.UnitPrice != raceUnitPrice || first.Total != 14 {
					t.Fatalf("数量 2 的单价/总价=%d/%d，want %d/%d",
						first.UnitPrice, first.Total, raceUnitPrice, 14)
				}
			}
			if !first.AcceptedAt.Equal(at(5)) {
				t.Fatalf("首次受理时刻=%v，want %v", first.AcceptedAt, at(5))
			}

			// 无论首次是拒绝还是确认，批次结束后查询与原样重试都取回同一份记录；
			// 另一数量始终冲突，拒绝记录也保留实际首次请求和受理时刻。
			setNow(at(50))
			loseQ := int64(0)
			if winQ == 0 {
				loseQ = 2
			}
			checkStoredFirstResult(t, b, id, first, winQ, loseQ)

			// 再点一次边界：数量 0 先受理时，后续数量 2 即使在更晚时刻单独重试，
			// 拿到的仍是冲突而不是新确认；数量 2 先受理时同理，数量 0 不会变成拒绝。
			if _, err := b.Quote(QuoteRequest{
				RequestID: id, ItemID: raceItemID, VersionID: raceVersionID, Quantity: loseQ,
			}); !errors.Is(err, ErrRequestIDConflict) {
				t.Fatalf("落败数量 %d 延后单独提交应冲突, got %v", loseQ, err)
			}
			got, err := b.Lookup(id)
			if err != nil || got != first {
				t.Fatalf("落败数量的延后提交改写了记录: %+v (%v)", got, err)
			}
		})
	}
	t.Logf("各轮首次受理数量分布: %v（数量 0 的拒绝与数量 2 的确认都允许先占用标识）", winners)
}
