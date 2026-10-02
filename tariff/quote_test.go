package tariff

import (
	"math"
	"sync"
	"testing"
	"time"
)

func registerBasicItem(t *testing.T, b *Book, at time.Time) {
	t.Helper()
	registerVersion(t, b, RegisterVersionInput{
		Item: "power", ID: "v1", UnitPrice: 100, Start: at,
	})
}

func TestQuote_Success(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b, _ := newTestBook(t, at)
	registerBasicItem(t, b, at)

	res := quote(t, b, QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 3})
	if res.Rejected != nil {
		t.Fatalf("rejected: %+v", res.Rejected)
	}
	c := res.Confirmed
	if c == nil {
		t.Fatal("confirmed is nil")
	}
	if c.UnitPrice != 100 || c.TotalPrice != 300 || c.Quantity != 3 {
		t.Fatalf("price = %d, total = %d, qty = %d; want 100/300/3", c.UnitPrice, c.TotalPrice, c.Quantity)
	}
	if !c.AcceptedAt.Equal(at) {
		t.Fatalf("AcceptedAt = %v, want %v", c.AcceptedAt, at)
	}
	if c.Item != "power" || c.Version != "v1" || c.RequestID != "q1" {
		t.Fatalf("echo mismatch: %+v", c)
	}
}

func TestQuote_ZeroPrice(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b, _ := newTestBook(t, at)
	registerVersion(t, b, RegisterVersionInput{Item: "free", ID: "v1", UnitPrice: 0, Start: at})

	res := quote(t, b, QuoteRequest{RequestID: "q1", Item: "free", Version: "v1", Quantity: 1000})
	if res.Confirmed == nil {
		t.Fatalf("rejected: %+v", res.Rejected)
	}
	if res.Confirmed.TotalPrice != 0 {
		t.Fatalf("total = %d, want 0", res.Confirmed.TotalPrice)
	}
}

func TestQuote_Rejections(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	future := at.Add(24 * time.Hour)
	expired := at.Add(-24 * time.Hour)

	tests := []struct {
		name string
		req  QuoteRequest
		want Reason
	}{
		{"empty request id", QuoteRequest{RequestID: "", Item: "power", Version: "v1", Quantity: 1}, ReasonEmptyRequestID},
		{"zero quantity", QuoteRequest{RequestID: "q", Item: "power", Version: "v1", Quantity: 0}, ReasonInvalidQuantity},
		{"negative quantity", QuoteRequest{RequestID: "q", Item: "power", Version: "v1", Quantity: -2}, ReasonInvalidQuantity},
		{"item missing", QuoteRequest{RequestID: "q", Item: "nope", Version: "v1", Quantity: 1}, ReasonItemNotFound},
		{"version missing", QuoteRequest{RequestID: "q", Item: "power", Version: "nope", Quantity: 1}, ReasonVersionNotFound},
		{"not yet effective", QuoteRequest{RequestID: "q", Item: "power", Version: "future", Quantity: 1}, ReasonNotEffective},
		{"expired", QuoteRequest{RequestID: "q", Item: "power", Version: "old", Quantity: 1}, ReasonExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newTestBook(t, at)
			// v1: [at, at+24h)；future 与 old 分别在其两侧相邻交接
			registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "v1", UnitPrice: 100, Start: at, End: ptrTime(future)})
			registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "future", UnitPrice: 100, Start: future})
			registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "old", UnitPrice: 100, Start: expired, End: ptrTime(at)})

			res, err := b.Quote(tc.req)
			if err != nil {
				t.Fatalf("Quote error: %v", err)
			}
			if res.Confirmed != nil {
				t.Fatalf("confirmed = %+v, want rejection", res.Confirmed)
			}
			if res.Rejected == nil || res.Rejected.Reason != tc.want {
				t.Fatalf("rejection = %+v, want reason %s", res.Rejected, tc.want)
			}
			if !res.Rejected.AcceptedAt.Equal(at) {
				t.Fatalf("AcceptedAt = %v, want %v", res.Rejected.AcceptedAt, at)
			}
		})
	}
}

func TestQuote_Overflow(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b, _ := newTestBook(t, at)
	registerVersion(t, b, RegisterVersionInput{Item: "i", ID: "v1", UnitPrice: 2, Start: at})

	res := quote(t, b, QuoteRequest{RequestID: "q", Item: "i", Version: "v1", Quantity: math.MaxInt64})
	if res.Confirmed != nil {
		t.Fatalf("confirmed total = %d, want overflow rejection", res.Confirmed.TotalPrice)
	}
	if res.Rejected == nil || res.Rejected.Reason != ReasonOverflow {
		t.Fatalf("rejection = %+v, want %s", res.Rejected, ReasonOverflow)
	}

	// 边界：单价 1、数量 MaxInt64 不溢出
	b2, _ := newTestBook(t, at)
	registerVersion(t, b2, RegisterVersionInput{Item: "i", ID: "v1", UnitPrice: 1, Start: at})
	res2 := quote(t, b2, QuoteRequest{RequestID: "q", Item: "i", Version: "v1", Quantity: math.MaxInt64})
	if res2.Confirmed == nil || res2.Confirmed.TotalPrice != math.MaxInt64 {
		t.Fatalf("boundary total = %v, want %d", res2.Confirmed, math.MaxInt64)
	}
}

func TestQuote_UsesAcceptanceMoment(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	future := at.Add(time.Hour)
	b, clk := newTestBook(t, at)
	registerVersion(t, b, RegisterVersionInput{Item: "i", ID: "v1", UnitPrice: 100, Start: future})

	// 受理时刻早于生效时刻 → 拒绝
	res := quote(t, b, QuoteRequest{RequestID: "q1", Item: "i", Version: "v1", Quantity: 1})
	if res.Rejected == nil || res.Rejected.Reason != ReasonNotEffective {
		t.Fatalf("at %v: rejection = %+v, want %s", at, res.Rejected, ReasonNotEffective)
	}

	// 受理时刻到达生效时刻 → 确认
	clk.set(future)
	res = quote(t, b, QuoteRequest{RequestID: "q2", Item: "i", Version: "v1", Quantity: 1})
	if res.Confirmed == nil {
		t.Fatalf("at %v: rejected = %+v, want confirmed", future, res.Rejected)
	}
}

func TestIdempotent_RetryReturnsFirstResult(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	later := at.Add(365 * 24 * time.Hour)
	b, clk := newTestBook(t, at)
	registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "v1", UnitPrice: 100, Start: at, End: ptrTime(later)})

	first := quote(t, b, QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 3})
	if first.Confirmed == nil {
		t.Fatalf("first rejected: %+v", first.Rejected)
	}

	// 时钟推进、新版本在相邻时刻登记，重试仍返回首次结果
	clk.set(later)
	registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "v2", UnitPrice: 999, Start: later})

	retry := quote(t, b, QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 3})
	if retry.Confirmed == nil {
		t.Fatalf("retry rejected: %+v", retry.Rejected)
	}
	if retry.Confirmed.TotalPrice != first.Confirmed.TotalPrice ||
		retry.Confirmed.UnitPrice != first.Confirmed.UnitPrice ||
		!retry.Confirmed.AcceptedAt.Equal(first.Confirmed.AcceptedAt) {
		t.Fatalf("retry = %+v, want first result %+v", retry.Confirmed, first.Confirmed)
	}

	// GetQuote 与首次结果一致
	got, err := b.GetQuote("q1")
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}
	if got.Confirmed.TotalPrice != 300 || got.Confirmed.UnitPrice != 100 {
		t.Fatalf("stored = %+v, want original 100/300", got.Confirmed)
	}
}

func TestIdempotent_RejectedFirstResultIsKept(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	future := at.Add(time.Hour)
	b, clk := newTestBook(t, at)
	// 首次报价时费率项不存在 → 拒绝
	first := quote(t, b, QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 1})
	if first.Rejected == nil || first.Rejected.Reason != ReasonItemNotFound {
		t.Fatalf("first = %+v, want %s", first.Rejected, ReasonItemNotFound)
	}

	// 补登版本并推进时钟后重试：仍返回首次拒绝结果，不重新计算
	registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "v1", UnitPrice: 100, Start: future})
	clk.set(future.Add(time.Hour))
	retry := quote(t, b, QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 1})
	if retry.Confirmed != nil {
		t.Fatalf("retry confirmed = %+v, want first rejection", retry.Confirmed)
	}
	if retry.Rejected == nil || retry.Rejected.Reason != ReasonItemNotFound {
		t.Fatalf("retry = %+v, want first reason %s", retry.Rejected, ReasonItemNotFound)
	}
	if !retry.Rejected.AcceptedAt.Equal(at) {
		t.Fatalf("retry AcceptedAt = %v, want first acceptance %v", retry.Rejected.AcceptedAt, at)
	}

	// 版本已失效后重试同样返回首次结果
	clk.set(future.Add(48 * time.Hour))
	retry2 := quote(t, b, QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 1})
	if retry2.Rejected == nil || retry2.Rejected.Reason != ReasonItemNotFound {
		t.Fatalf("retry2 = %+v, want first reason %s", retry2.Rejected, ReasonItemNotFound)
	}
}

func TestRequest_ConflictDoesNotOverwrite(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	later := at.Add(24 * time.Hour)
	b, _ := newTestBook(t, at)
	registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "v1", UnitPrice: 100, Start: at, End: ptrTime(later)})
	registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "v2", UnitPrice: 200, Start: later})

	first := quote(t, b, QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 2})
	if first.Confirmed == nil {
		t.Fatalf("first rejected: %+v", first.Rejected)
	}

	// 沿用标识改变数量 → 冲突
	_, err := b.Quote(QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 3})
	if !IsReason(err, ReasonRequestConflict) {
		t.Fatalf("err = %v, want %s", err, ReasonRequestConflict)
	}
	// 沿用标识改变版本 → 冲突
	_, err = b.Quote(QuoteRequest{RequestID: "q1", Item: "power", Version: "v2", Quantity: 2})
	if !IsReason(err, ReasonRequestConflict) {
		t.Fatalf("err = %v, want %s", err, ReasonRequestConflict)
	}
	// 沿用标识改变费率项 → 冲突
	_, err = b.Quote(QuoteRequest{RequestID: "q1", Item: "other", Version: "v1", Quantity: 2})
	if !IsReason(err, ReasonRequestConflict) {
		t.Fatalf("err = %v, want %s", err, ReasonRequestConflict)
	}

	// 首次记录未被覆盖
	got, _ := b.GetQuote("q1")
	if got.Confirmed == nil || got.Confirmed.Quantity != 2 || got.Confirmed.TotalPrice != 200 {
		t.Fatalf("stored = %+v, want original qty 2 / total 200", got.Confirmed)
	}
}

func TestGetQuote_NotFound(t *testing.T) {
	b, _ := newTestBook(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	_, err := b.GetQuote("never-seen")
	if !IsReason(err, ReasonRequestNotFound) {
		t.Fatalf("err = %v, want %s", err, ReasonRequestNotFound)
	}
}

func TestGetQuote_RejectedEchoesOriginalRequest(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b, _ := newTestBook(t, at)

	req := QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 7}
	res, err := b.Quote(req)
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if res.Rejected == nil || res.Rejected.Reason != ReasonItemNotFound {
		t.Fatalf("rejection = %+v, want %s", res.Rejected, ReasonItemNotFound)
	}

	got, err := b.GetQuote("q1")
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}
	if got.Confirmed != nil {
		t.Fatalf("got confirmed = %+v, want rejection", got.Confirmed)
	}
	r := got.Rejected
	if r == nil {
		t.Fatal("rejection is nil")
	}
	if r.Reason != ReasonItemNotFound || !r.AcceptedAt.Equal(at) {
		t.Fatalf("reason/accepted = %+v, want %s/%v", r, ReasonItemNotFound, at)
	}
	if r.Item != req.Item || r.Version != req.Version || r.Quantity != req.Quantity {
		t.Fatalf("echo = %s/%s/%d, want %s/%s/%d", r.Item, r.Version, r.Quantity, req.Item, req.Version, req.Quantity)
	}
}

func TestConfirmedPrice_SurvivesLaterChange(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b, _ := newTestBook(t, at)
	registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "v1", UnitPrice: 100, Start: at})

	res := quote(t, b, QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 5})
	if res.Confirmed == nil {
		t.Fatalf("rejected: %+v", res.Rejected)
	}

	// 后续替代升级：v1 被 v2 替代
	v2Start := at.Add(30 * 24 * time.Hour)
	registerVersion(t, b, RegisterVersionInput{
		Item: "power", ID: "v2", UnitPrice: 120, Start: v2Start, Replaces: "v1",
	})

	// 原确认价与来源不变
	got, _ := b.GetQuote("q1")
	if got.Confirmed == nil {
		t.Fatal("confirmed record missing")
	}
	if got.Confirmed.UnitPrice != 100 || got.Confirmed.TotalPrice != 500 || got.Confirmed.Version != "v1" {
		t.Fatalf("confirmed = %+v, want original 100/500/v1", got.Confirmed)
	}
}

func TestConcurrent_SameRequestLeavesOneResult(t *testing.T) {
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b, _ := newTestBook(t, at)
	registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "v1", UnitPrice: 100, Start: at})

	const n = 64
	var wg sync.WaitGroup
	results := make([]*QuoteResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = b.Quote(QuoteRequest{
				RequestID: "same", Item: "power", Version: "v1", Quantity: 1,
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	first := results[0]
	if first.Confirmed == nil {
		t.Fatalf("rejected: %+v", first.Rejected)
	}
	for i, res := range results {
		if res.Confirmed == nil {
			t.Fatalf("goroutine %d rejected: %+v", i, res.Rejected)
		}
		if res.Confirmed.AcceptedAt != first.Confirmed.AcceptedAt ||
			res.Confirmed.TotalPrice != first.Confirmed.TotalPrice {
			t.Fatalf("goroutine %d result = %+v, want first %+v", i, res.Confirmed, first.Confirmed)
		}
	}

	got, err := b.GetQuote("same")
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}
	if got.Confirmed.TotalPrice != first.Confirmed.TotalPrice {
		t.Fatalf("stored total = %d, want %d", got.Confirmed.TotalPrice, first.Confirmed.TotalPrice)
	}
}
