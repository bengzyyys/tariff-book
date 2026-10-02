package tariff

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(hours int) time.Time { return base.Add(time.Duration(hours) * time.Hour) }

func atPtr(hours int) *time.Time {
	t := at(hours)
	return &t
}

// fixedClock 返回可手动推进的时钟。
func fixedClock(start time.Time) (func() time.Time, func(time.Time)) {
	var mu sync.Mutex
	cur := start
	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return cur
		}, func(t time.Time) {
			mu.Lock()
			defer mu.Unlock()
			cur = t
		}
}

func TestRegisterValidation(t *testing.T) {
	b := NewBook()
	ok := RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 100, Start: at(0)}

	if err := b.RegisterVersion(RegisterRequest{VersionID: "v1", UnitPrice: 1, Start: at(0)}); !errors.Is(err, ErrEmptyItemID) {
		t.Fatalf("empty item: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", UnitPrice: 1, Start: at(0)}); !errors.Is(err, ErrEmptyVersionID) {
		t.Fatalf("empty version: %v", err)
	}
	r := ok
	r.UnitPrice = -1
	if err := b.RegisterVersion(r); !errors.Is(err, ErrInvalidUnitPrice) {
		t.Fatalf("negative price: %v", err)
	}
	r = ok
	r.UnitPrice = 0 // 零单价允许
	if err := b.RegisterVersion(r); err != nil {
		t.Fatalf("zero price should be allowed: %v", err)
	}
	r = ok
	r.VersionID = "v2"
	r.Start = time.Time{}
	if err := b.RegisterVersion(r); !errors.Is(err, ErrStartRequired) {
		t.Fatalf("missing start: %v", err)
	}
	r = ok
	r.VersionID = "v2"
	r.End = atPtr(0) // 结束必须晚于开始
	if err := b.RegisterVersion(r); !errors.Is(err, ErrInvalidInterval) {
		t.Fatalf("end == start: %v", err)
	}
	r.End = atPtr(-1)
	if err := b.RegisterVersion(r); !errors.Is(err, ErrInvalidInterval) {
		t.Fatalf("end < start: %v", err)
	}
	if err := b.RegisterVersion(ok); !errors.Is(err, ErrVersionExists) {
		t.Fatalf("duplicate version id: %v", err)
	}
	// 相同版本标识可用于另一费率项
	r = ok
	r.ItemID = "room"
	if err := b.RegisterVersion(r); err != nil {
		t.Fatalf("same version id on another item: %v", err)
	}
}

func TestRegisterOverlapAndAdjacency(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 1, Start: at(0), End: atPtr(10)})

	// 相邻交接（end == start）允许
	must(RegisterRequest{ItemID: "seat", VersionID: "v2", UnitPrice: 1, Start: at(10)})

	overlaps := []RegisterRequest{
		{ItemID: "seat", VersionID: "x1", UnitPrice: 1, Start: at(5), End: atPtr(15)},
		{ItemID: "seat", VersionID: "x2", UnitPrice: 1, Start: at(-5), End: atPtr(1)},
		{ItemID: "seat", VersionID: "x3", UnitPrice: 1, Start: at(5), End: atPtr(6)},
		{ItemID: "seat", VersionID: "x4", UnitPrice: 1, Start: at(-5), End: atPtr(20)},
		{ItemID: "seat", VersionID: "x5", UnitPrice: 1, Start: at(9)},
	}
	for _, r := range overlaps {
		if err := b.RegisterVersion(r); !errors.Is(err, ErrOverlap) {
			t.Fatalf("expected overlap for %+v, got %v", r, err)
		}
	}
	// 重叠失败不留下部分变更
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("failed registrations left changes behind: %d versions", len(views))
	}
	// 不同费率项互不影响
	must(RegisterRequest{ItemID: "room", VersionID: "v1", UnitPrice: 1, Start: at(0), End: atPtr(10)})
}

func TestRegisterTimezoneInstants(t *testing.T) {
	b := NewBook()
	// 同一时刻、不同时区偏移
	startUTC := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	endUTC := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 1,
		Start: startUTC, End: &endUTC,
	}); err != nil {
		t.Fatal(err)
	}
	// +08:00 的 18:00 即 UTC 10:00，与 v1 开始重叠，应被拒绝
	east := time.FixedZone("UTC+8", 8*3600)
	overlap := time.Date(2026, 3, 1, 18, 0, 0, 0, east)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 1, Start: overlap,
	}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("same instant in another offset must overlap: %v", err)
	}
	// +08:00 的 04:00(+1d) 即 UTC 20:00，恰好交接，允许
	adjacent := time.Date(2026, 3, 2, 4, 0, 0, 0, east)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 1, Start: adjacent,
	}); err != nil {
		t.Fatalf("adjacent handoff across offsets: %v", err)
	}
}

func TestReplacement(t *testing.T) {
	now, setNow := fixedClock(at(0))
	b := NewBook(WithClock(now))
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 100, Start: at(0), End: atPtr(100)})
	must(RegisterRequest{ItemID: "seat", VersionID: "new", UnitPrice: 200, Start: at(40), Replaces: "old"})

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("want 2 versions, got %d", len(views))
	}
	old, nv := views[0], views[1]
	if old.VersionID != "old" || nv.VersionID != "new" {
		t.Fatalf("unexpected order: %v, %v", old.VersionID, nv.VersionID)
	}
	// 登记信息保留，替代关系可见
	if old.End == nil || !old.End.Equal(at(100)) {
		t.Fatalf("registered end lost: %v", old.End)
	}
	if nv.Replaces != "old" || old.SupersededBy != "new" {
		t.Fatalf("replacement relation missing: %+v %+v", old, nv)
	}
	// 实际有效区间：old 被截断到 new 的开始
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(at(40)) {
		t.Fatalf("old effective end: %v", old.EffectiveEnd)
	}
	if nv.EffectiveEnd != nil {
		t.Fatalf("new should be open-ended: %v", nv.EffectiveEnd)
	}

	// 新版本到期后，被替代的旧版本不会重新生效：
	// 用另一个项验证——new 到期后 old 的有效区间仍止于交接点。
	must(RegisterRequest{ItemID: "hall", VersionID: "old", UnitPrice: 1, Start: at(0), End: atPtr(100)})
	must(RegisterRequest{ItemID: "hall", VersionID: "new", UnitPrice: 1, Start: at(40), End: atPtr(50), Replaces: "old"})
	setNow(at(60)) // new 已到期
	out, err := b.Quote(QuoteRequest{RequestID: "q-hall-old", ItemID: "hall", VersionID: "old", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("superseded old version must not revive: %+v", out)
	}
	out, err = b.Quote(QuoteRequest{RequestID: "q-hall-new", ItemID: "hall", VersionID: "new", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("expired new version: %+v", out)
	}
}

func TestReplacementRejected(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 1, Start: at(10), End: atPtr(50)})
	must(RegisterRequest{ItemID: "room", VersionID: "other", UnitPrice: 1, Start: at(0)})

	cases := []struct {
		name string
		req  RegisterRequest
		want error
	}{
		{"target missing", RegisterRequest{ItemID: "seat", VersionID: "n1", UnitPrice: 1, Start: at(20), Replaces: "nope"}, ErrReplaceTargetNotFound},
		{"target in another item", RegisterRequest{ItemID: "seat", VersionID: "n2", UnitPrice: 1, Start: at(20), Replaces: "other"}, ErrReplaceTargetWrongItem},
		{"start not after old start", RegisterRequest{ItemID: "seat", VersionID: "n3", UnitPrice: 1, Start: at(10), Replaces: "old"}, ErrInvalidReplacement},
		{"start before old start", RegisterRequest{ItemID: "seat", VersionID: "n4", UnitPrice: 1, Start: at(5), Replaces: "old"}, ErrInvalidReplacement},
		{"start outside effective interval", RegisterRequest{ItemID: "seat", VersionID: "n5", UnitPrice: 1, Start: at(50), Replaces: "old"}, ErrInvalidReplacement},
	}
	for _, c := range cases {
		if err := b.RegisterVersion(c.req); !errors.Is(err, c.want) {
			t.Fatalf("%s: want %v, got %v", c.name, c.want, err)
		}
	}

	// 与项内其他版本冲突时整次登记被拒绝，旧版本有效区间不变
	must(RegisterRequest{ItemID: "seat", VersionID: "later", UnitPrice: 1, Start: at(60), End: atPtr(70)})
	conflict := RegisterRequest{ItemID: "seat", VersionID: "n6", UnitPrice: 1, Start: at(30), End: atPtr(65), Replaces: "old"}
	if err := b.RegisterVersion(conflict); !errors.Is(err, ErrOverlap) {
		t.Fatalf("want overlap, got %v", err)
	}
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.VersionID == "old" {
			if v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(at(50)) {
				t.Fatalf("rejected registration changed old effective interval: %v", v.EffectiveEnd)
			}
			if v.SupersededBy != "" {
				t.Fatalf("rejected registration left superseded-by: %v", v.SupersededBy)
			}
		}
		if v.VersionID == "n6" {
			t.Fatal("rejected registration left the new version behind")
		}
	}

	// 已被替代的版本当前有效区间已截断，替代窗口随之收窄
	must(RegisterRequest{ItemID: "seat", VersionID: "mid", UnitPrice: 1, Start: at(20), End: atPtr(60), Replaces: "old"})
	outsideTruncated := RegisterRequest{ItemID: "seat", VersionID: "n7", UnitPrice: 1, Start: at(25), Replaces: "old"}
	if err := b.RegisterVersion(outsideTruncated); !errors.Is(err, ErrInvalidReplacement) {
		t.Fatalf("start beyond truncated interval: %v", err)
	}
}

func TestQuoteConfirmed(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 150, Start: at(0), End: atPtr(10)}); err != nil {
		t.Fatal(err)
	}
	out, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Confirmed || out.UnitPrice != 150 || out.Total != 600 {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if !out.AcceptedAt.Equal(at(5)) {
		t.Fatalf("acceptance time: %v", out.AcceptedAt)
	}
	if out.Reason != ReasonNone {
		t.Fatalf("reason on confirmed quote: %v", out.Reason)
	}
	// 边界：开始时刻含、结束时刻不含
	if err := b.RegisterVersion(RegisterRequest{ItemID: "edge", VersionID: "v2", UnitPrice: 1, Start: at(5), End: atPtr(6)}); err != nil {
		t.Fatal(err)
	}
	out, _ = b.Quote(QuoteRequest{RequestID: "q2", ItemID: "edge", VersionID: "v2", Quantity: 1})
	if !out.Confirmed {
		t.Fatalf("start instant inclusive: %+v", out)
	}
}

func TestQuoteRejections(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "future", UnitPrice: 1, Start: at(10)}); err != nil {
		t.Fatal(err)
	}
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "past", UnitPrice: 1, Start: at(0), End: atPtr(5)}); err != nil {
		t.Fatal(err)
	}
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "live", UnitPrice: math.MaxInt64, Start: at(5), End: atPtr(9)}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		req  QuoteRequest
		want RejectReason
	}{
		{"empty id", QuoteRequest{RequestID: "", ItemID: "seat", VersionID: "live", Quantity: 1}, ReasonEmptyRequestID},
		{"zero quantity", QuoteRequest{RequestID: "r1", ItemID: "seat", VersionID: "live", Quantity: 0}, ReasonInvalidQuantity},
		{"negative quantity", QuoteRequest{RequestID: "r2", ItemID: "seat", VersionID: "live", Quantity: -3}, ReasonInvalidQuantity},
		{"version missing", QuoteRequest{RequestID: "r3", ItemID: "seat", VersionID: "nope", Quantity: 1}, ReasonVersionNotFound},
		{"item missing", QuoteRequest{RequestID: "r4", ItemID: "nope", VersionID: "v", Quantity: 1}, ReasonVersionNotFound},
		{"not yet effective", QuoteRequest{RequestID: "r5", ItemID: "seat", VersionID: "future", Quantity: 1}, ReasonVersionNotYetEffective},
		{"expired", QuoteRequest{RequestID: "r6", ItemID: "seat", VersionID: "past", Quantity: 1}, ReasonVersionExpired},
		{"overflow", QuoteRequest{RequestID: "r7", ItemID: "seat", VersionID: "live", Quantity: 2}, ReasonTotalOverflow},
	}
	for _, c := range cases {
		out, err := b.Quote(c.req)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if out.Confirmed || out.Reason != c.want {
			t.Fatalf("%s: want reason %v, got %+v", c.name, c.want, out)
		}
	}
	// 空标识的拒绝无法保存、也不应出现在查询中
	if _, err := b.Lookup(""); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("empty id lookup: %v", err)
	}
	// 恰好等于上限时不溢出
	out, _ := b.Quote(QuoteRequest{RequestID: "r8", ItemID: "seat", VersionID: "live", Quantity: 1})
	if !out.Confirmed || out.Total != math.MaxInt64 {
		t.Fatalf("max int64 total: %+v", out)
	}
}

func TestQuoteNoVersionSwitching(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "expired", UnitPrice: 1, Start: at(0), End: atPtr(4)}); err != nil {
		t.Fatal(err)
	}
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "live", UnitPrice: 1, Start: at(4)}); err != nil {
		t.Fatal(err)
	}
	out, _ := b.Quote(QuoteRequest{RequestID: "q", ItemID: "seat", VersionID: "expired", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("must not fall back to another version: %+v", out)
	}
}

func TestConfirmedRecordSurvivesRateChanges(t *testing.T) {
	now, setNow := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 100, Start: at(0), End: atPtr(10)}); err != nil {
		t.Fatal(err)
	}
	first, err := b.Quote(QuoteRequest{RequestID: "q1", ItemID: "seat", VersionID: "v1", Quantity: 3})
	if err != nil || !first.Confirmed {
		t.Fatalf("quote: %v %+v", err, first)
	}

	// 版本被替代、随后到期，确认记录不变
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v2", UnitPrice: 999, Start: at(6), Replaces: "v1"}); err != nil {
		t.Fatal(err)
	}
	setNow(at(20))
	got, err := b.Lookup("q1")
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("confirmed record changed: %+v -> %+v", first, got)
	}
	// 此刻 v1 已失效，新报价被拒绝，但旧确认仍可查
	out, _ := b.Quote(QuoteRequest{RequestID: "q2", ItemID: "seat", VersionID: "v1", Quantity: 3})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("expected expired: %+v", out)
	}
}

func TestIdempotentReplay(t *testing.T) {
	now, setNow := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 100, Start: at(0), End: atPtr(10)}); err != nil {
		t.Fatal(err)
	}

	// 确认结果的重试：即使版本后来失效也返回首次结果
	first, _ := b.Quote(QuoteRequest{RequestID: "ok", ItemID: "seat", VersionID: "v1", Quantity: 2})
	setNow(at(50))
	replay, err := b.Quote(QuoteRequest{RequestID: "ok", ItemID: "seat", VersionID: "v1", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}
	if replay != first {
		t.Fatalf("replay recomputed: %+v vs %+v", first, replay)
	}

	// 拒绝结果的重试：即使补登了版本也返回首次拒绝
	rej, _ := b.Quote(QuoteRequest{RequestID: "rej", ItemID: "seat", VersionID: "ghost", Quantity: 1})
	if rej.Confirmed || rej.Reason != ReasonVersionNotFound {
		t.Fatalf("want not-found rejection: %+v", rej)
	}
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "ghost", UnitPrice: 1, Start: at(10)}); err != nil {
		t.Fatal(err)
	}
	rejReplay, err := b.Quote(QuoteRequest{RequestID: "rej", ItemID: "seat", VersionID: "ghost", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rejReplay != rej {
		t.Fatalf("rejection replay recomputed: %+v vs %+v", rej, rejReplay)
	}

	// 查询可分别得到确认记录与拒绝记录
	if got, _ := b.Lookup("ok"); got != first {
		t.Fatalf("lookup confirmed: %+v", got)
	}
	if got, _ := b.Lookup("rej"); got != rej {
		t.Fatalf("lookup rejected: %+v", got)
	}
	if _, err := b.Lookup("never-seen"); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestRequestIDConflict(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 1, Start: at(0)}); err != nil {
		t.Fatal(err)
	}
	first, _ := b.Quote(QuoteRequest{RequestID: "id", ItemID: "seat", VersionID: "v1", Quantity: 1})

	conflicts := []QuoteRequest{
		{RequestID: "id", ItemID: "other", VersionID: "v1", Quantity: 1},
		{RequestID: "id", ItemID: "seat", VersionID: "v2", Quantity: 1},
		{RequestID: "id", ItemID: "seat", VersionID: "v1", Quantity: 2},
	}
	for _, req := range conflicts {
		if _, err := b.Quote(req); !errors.Is(err, ErrRequestIDConflict) {
			t.Fatalf("want conflict for %+v", req)
		}
	}
	// 首次记录未被覆盖
	got, err := b.Lookup("id")
	if err != nil || got != first {
		t.Fatalf("first record overwritten: %+v (%v)", got, err)
	}
}

func TestConcurrentDuplicateSubmissions(t *testing.T) {
	now, _ := fixedClock(at(5))
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "v1", UnitPrice: 7, Start: at(0)}); err != nil {
		t.Fatal(err)
	}
	req := QuoteRequest{RequestID: "dup", ItemID: "seat", VersionID: "v1", Quantity: 3}

	const n = 32
	outs := make([]Outcome, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = b.Quote(req)
		}(i)
	}
	wg.Wait()
	for i := range outs {
		if errs[i] != nil {
			t.Fatalf("concurrent quote %d: %v", i, errs[i])
		}
		if outs[i] != outs[0] {
			t.Fatalf("divergent first result: %+v vs %+v", outs[0], outs[i])
		}
	}
	if !outs[0].Confirmed || outs[0].Total != 21 {
		t.Fatalf("unexpected outcome: %+v", outs[0])
	}
	got, err := b.Lookup("dup")
	if err != nil || got != outs[0] {
		t.Fatalf("stored record: %+v (%v)", got, err)
	}
}

func TestItemNotFound(t *testing.T) {
	b := NewBook()
	if _, err := b.ItemVersions("nope"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("want ErrItemNotFound, got %v", err)
	}
}

// 登记成功后，调用方复用并改写原请求中的结束时间，不能影响账本。
func TestRegisterEndImmuneToRequestMutation(t *testing.T) {
	now, setNow := fixedClock(at(0))
	b := NewBook(WithClock(now))

	end := at(10)
	req := RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 100, Start: at(0), End: &end}
	if err := b.RegisterVersion(req); err != nil {
		t.Fatal(err)
	}
	// 旧版十点结束，新版十点开始（相邻交接）
	if err := b.RegisterVersion(RegisterRequest{ItemID: "seat", VersionID: "new", UnitPrice: 200, Start: at(10)}); err != nil {
		t.Fatal(err)
	}

	// 把原请求中的结束时刻改晚到十二点
	*req.End = at(12)
	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.VersionID != "old" {
			continue
		}
		if v.End == nil || !v.End.Equal(at(10)) {
			t.Fatalf("registered end changed by request mutation: %v", v.End)
		}
		if v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(at(10)) {
			t.Fatalf("effective end changed by request mutation: %v", v.EffectiveEnd)
		}
	}
	// 十一点旧版仍不可报价，新版可以——不能出现两版同时有效
	setNow(at(11))
	out, _ := b.Quote(QuoteRequest{RequestID: "q-old", ItemID: "seat", VersionID: "old", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("old version must still expire at 10: %+v", out)
	}
	out, _ = b.Quote(QuoteRequest{RequestID: "q-new", ItemID: "seat", VersionID: "new", Quantity: 1})
	if !out.Confirmed {
		t.Fatalf("new version should be quotable at 11: %+v", out)
	}

	// 改早到八点也不能让已登记版本提前到期
	*req.End = at(8)
	views, _ = b.ItemVersions("seat")
	if views[0].End == nil || !views[0].End.Equal(at(10)) {
		t.Fatalf("registered end moved earlier by request mutation: %v", views[0].End)
	}
}

// 查询返回的登记结束时间与实际结束时间可被调用方随意修改，但账本不变。
func TestQueryMutationDoesNotChangeLedger(t *testing.T) {
	now, setNow := fixedClock(at(0))
	b := NewBook(WithClock(now))
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 100, Start: at(0), End: atPtr(100)})
	must(RegisterRequest{ItemID: "seat", VersionID: "new", UnitPrice: 200, Start: at(40), Replaces: "old"})

	first, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	// old 的登记结束为 100，实际结束被截断到 40
	var old, nv *VersionView
	for i := range first {
		switch first[i].VersionID {
		case "old":
			old = &first[i]
		case "new":
			nv = &first[i]
		}
	}
	if old.End == nil || !old.End.Equal(at(100)) || old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(at(40)) {
		t.Fatalf("unexpected old view: %+v", old)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("new should be open-ended: %+v", nv)
	}

	// 调用方为展示目的改写这份查询结果
	*old.End = at(99)
	*old.EffectiveEnd = at(98)
	twelve := at(12)
	nv.End = &twelve
	nv.EffectiveEnd = &twelve

	// 再次查询仍是账本中的真实边界
	second, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	var old2, nv2 *VersionView
	for i := range second {
		switch second[i].VersionID {
		case "old":
			old2 = &second[i]
		case "new":
			nv2 = &second[i]
		}
	}
	if old2.End == nil || !old2.End.Equal(at(100)) {
		t.Fatalf("registered end changed through query mutation: %v", old2.End)
	}
	if old2.EffectiveEnd == nil || !old2.EffectiveEnd.Equal(at(40)) {
		t.Fatalf("effective end changed through query mutation: %v", old2.EffectiveEnd)
	}
	if nv2.End != nil || nv2.EffectiveEnd != nil {
		t.Fatalf("open-ended version got an end through another view: %+v", nv2)
	}

	// 第一份结果内部：登记结束与实际结束即使值相同，修改一项也不连带另一项
	equalEnd := at(100)
	must(RegisterRequest{ItemID: "hall", VersionID: "v", UnitPrice: 1, Start: at(0), End: &equalEnd})
	h1, _ := b.ItemVersions("hall")
	h2, _ := b.ItemVersions("hall")
	if h1[0].End == h1[0].EffectiveEnd {
		t.Fatal("registered end and effective end share one pointer")
	}
	if h1[0].End == h2[0].End {
		t.Fatal("separate queries share the registered-end pointer")
	}
	if h1[0].EffectiveEnd == h2[0].EffectiveEnd {
		t.Fatal("separate queries share the effective-end pointer")
	}
	*h1[0].End = at(50)
	if !h1[0].EffectiveEnd.Equal(at(100)) {
		t.Fatalf("editing registered end leaked to effective end: %v", h1[0].EffectiveEnd)
	}
	if !h2[0].End.Equal(at(100)) {
		t.Fatalf("editing one query result leaked to another: %v", h2[0].End)
	}

	// 报价仍按真实区间处理：40 起 old 已失效
	setNow(at(45))
	out, _ := b.Quote(QuoteRequest{RequestID: "q-old", ItemID: "seat", VersionID: "old", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("quote must use ledger boundary: %+v", out)
	}
}

// 外部修改旧版或新版查询中的时间，不得改变替代交接点；新版到期后旧版不恢复生效。
func TestReplacementHandoffImmuneToViewMutation(t *testing.T) {
	now, setNow := fixedClock(at(0))
	b := NewBook(WithClock(now))
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 1, Start: at(0), End: atPtr(100)})
	must(RegisterRequest{ItemID: "seat", VersionID: "new", UnitPrice: 1, Start: at(40), End: atPtr(60), Replaces: "old"})

	views, _ := b.ItemVersions("seat")
	for i := range views {
		if views[i].VersionID == "old" {
			*views[i].End = at(1000)
			*views[i].EffectiveEnd = at(1000)
		}
		if views[i].VersionID == "new" {
			*views[i].End = at(1000)
			*views[i].EffectiveEnd = at(1000)
		}
	}

	again, _ := b.ItemVersions("seat")
	for _, v := range again {
		switch v.VersionID {
		case "old":
			if v.End == nil || !v.End.Equal(at(100)) {
				t.Fatalf("old registered end changed: %v", v.End)
			}
			if v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(at(40)) {
				t.Fatalf("handoff point changed: %v", v.EffectiveEnd)
			}
		case "new":
			if v.End == nil || !v.End.Equal(at(60)) {
				t.Fatalf("new registered end changed: %v", v.End)
			}
			if v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(at(60)) {
				t.Fatalf("new effective end changed: %v", v.EffectiveEnd)
			}
		}
	}

	// 新版到期后，旧版仍止于交接点，不能恢复生效
	setNow(at(70))
	out, _ := b.Quote(QuoteRequest{RequestID: "q-old", ItemID: "seat", VersionID: "old", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("superseded old must not revive: %+v", out)
	}
}

// 未提供结束时间且未被替代的版本持续有效，两种结束时间都为空；
// 之后被替代，登记结束仍为空，实际结束显示交接点。
func TestOpenEndedKeepsNilRegisteredEnd(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 1, Start: at(0)})
	views, _ := b.ItemVersions("seat")
	if views[0].End != nil || views[0].EffectiveEnd != nil {
		t.Fatalf("open-ended version must report nil ends: %+v", views[0])
	}

	must(RegisterRequest{ItemID: "seat", VersionID: "new", UnitPrice: 1, Start: at(40), Replaces: "old"})
	views, _ = b.ItemVersions("seat")
	// 即使调用方给返回结果中的登记结束塞了值，再次查询仍应为 nil
	for i := range views {
		if views[i].VersionID == "old" {
			late := at(99)
			views[i].End = &late
			*views[i].EffectiveEnd = at(99)
		}
	}
	again, _ := b.ItemVersions("seat")
	for _, v := range again {
		if v.VersionID == "old" {
			if v.End != nil {
				t.Fatalf("registered end must stay nil after replacement: %v", v.End)
			}
			if v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(at(40)) {
				t.Fatalf("effective end should show handoff: %v", v.EffectiveEnd)
			}
		}
	}
}
