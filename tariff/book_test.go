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

// 登记成功后，调用方改写请求中的结束时间（改早或改晚）都不能改变账本：
// 查询仍显示登记时的边界，报价仍按该边界判断。
func TestRegisteredEndImmuneToRequestMutation(t *testing.T) {
	now, _ := fixedClock(at(11))
	b := NewBook(WithClock(now))

	oldEnd := atPtr(10)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "old", UnitPrice: 1, Start: at(0), End: oldEnd,
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "new", UnitPrice: 1, Start: at(10),
	}); err != nil {
		t.Fatal(err)
	}

	// 登记成功后，调用方把旧版本的结束时间从十点改到十二点。
	*oldEnd = at(12)

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	old, nv := views[0], views[1]
	if !old.End.Equal(at(10)) {
		t.Fatalf("registered end changed to %v", old.End)
	}
	if !old.EffectiveEnd.Equal(at(10)) {
		t.Fatalf("effective end changed to %v", old.EffectiveEnd)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("new version should stay open-ended: end=%v eff=%v", nv.End, nv.EffectiveEnd)
	}

	// 十一点：旧版本不能因为结束时间被改晚而重新可报价；新版本本就从十点开始有效。
	out, _ := b.Quote(QuoteRequest{RequestID: "q-old", ItemID: "seat", VersionID: "old", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("old version must stay expired at 11: %+v", out)
	}
	out, _ = b.Quote(QuoteRequest{RequestID: "q-new", ItemID: "seat", VersionID: "new", Quantity: 1})
	if !out.Confirmed {
		t.Fatalf("new version should be live at 11: %+v", out)
	}

	// 改早也不行：再登记一个结束于二十点的版本，随后把它的结束改到十八点，
	// 二十点的报价仍应被拒绝（边界保持为二十点）。
	now2, _ := fixedClock(at(20))
	b2 := NewBook(WithClock(now2))
	lateEnd := atPtr(20)
	if err := b2.RegisterVersion(RegisterRequest{
		ItemID: "room", VersionID: "v1", UnitPrice: 1, Start: at(10), End: lateEnd,
	}); err != nil {
		t.Fatal(err)
	}
	*lateEnd = at(18)
	out, _ = b2.Quote(QuoteRequest{RequestID: "q-room", ItemID: "room", VersionID: "v1", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("end moved earlier must not extend quote window: %+v", out)
	}
}

// 版本查询返回的信息可由调用方修改，但改动登记结束时间或实际结束时间都不能改变账本。
func TestVersionViewsAreIndependentCopies(t *testing.T) {
	b := NewBook()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 1, Start: at(0), End: atPtr(10),
	}); err != nil {
		t.Fatal(err)
	}

	first, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	// 调用方为了展示修改查询结果中的两个结束时间。
	*first[0].End = at(1)
	*first[0].EffectiveEnd = at(1)

	again, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if !again[0].End.Equal(at(10)) || !again[0].EffectiveEnd.Equal(at(10)) {
		t.Fatalf("ledger changed through view mutation: end=%v eff=%v", again[0].End, again[0].EffectiveEnd)
	}

	// 分别取得的查询结果互不影响。
	second, _ := b.ItemVersions("seat")
	*second[0].End = at(2)
	if !again[0].End.Equal(at(10)) {
		t.Fatalf("separate query results must not share state: %v", again[0].End)
	}

	// 同次查询中不同版本的时间数据互不影响；登记结束与实际结束即使同值也各自独立。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v2", UnitPrice: 1, Start: at(10),
	}); err != nil {
		t.Fatal(err)
	}
	views, _ := b.ItemVersions("seat")
	*views[0].End = at(3) // 只改 v1 的登记结束时间
	if !views[0].EffectiveEnd.Equal(at(10)) {
		t.Fatalf("changing registered end must not touch effective end: %v", views[0].EffectiveEnd)
	}
	if views[1].End != nil || views[1].EffectiveEnd != nil {
		t.Fatalf("other version's open ends must stay nil: %v %v", views[1].End, views[1].EffectiveEnd)
	}
}

// 替代关系形成的交接点不受查询结果修改的影响：
// 旧版本登记结束保持原值、实际结束在新版本开始时截断；新版本到期后旧版本不恢复。
func TestReplacementBoundaryImmuneToViewMutation(t *testing.T) {
	now, _ := fixedClock(at(60))
	b := NewBook(WithClock(now))
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 100, Start: at(0), End: atPtr(100)})
	must(RegisterRequest{ItemID: "seat", VersionID: "new", UnitPrice: 200, Start: at(40), End: atPtr(50), Replaces: "old"})

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	old, nv := views[0], views[1]
	// 登记结束保持请求中的原值，实际结束被截断到交接点。
	if !old.End.Equal(at(100)) || !old.EffectiveEnd.Equal(at(40)) {
		t.Fatalf("old ends: reg=%v eff=%v", old.End, old.EffectiveEnd)
	}
	if !nv.End.Equal(at(50)) || !nv.EffectiveEnd.Equal(at(50)) {
		t.Fatalf("new ends: reg=%v eff=%v", nv.End, nv.EffectiveEnd)
	}

	// 调用方肆意修改查询结果中的时间。
	*old.End = at(1)
	*old.EffectiveEnd = at(1)
	*nv.End = at(1)
	*nv.EffectiveEnd = at(1)

	again, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	old2, nv2 := again[0], again[1]
	if !old2.End.Equal(at(100)) || !old2.EffectiveEnd.Equal(at(40)) {
		t.Fatalf("old boundary shifted: reg=%v eff=%v", old2.End, old2.EffectiveEnd)
	}
	if !nv2.End.Equal(at(50)) || !nv2.EffectiveEnd.Equal(at(50)) {
		t.Fatalf("new boundary shifted: reg=%v eff=%v", nv2.End, nv2.EffectiveEnd)
	}

	// 新版本已到期（六十点），被替代的旧版本不能恢复生效。
	out, _ := b.Quote(QuoteRequest{RequestID: "q-old", ItemID: "seat", VersionID: "old", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("superseded old must not revive: %+v", out)
	}
	out, _ = b.Quote(QuoteRequest{RequestID: "q-new", ItemID: "seat", VersionID: "new", Quantity: 1})
	if out.Confirmed || out.Reason != ReasonVersionExpired {
		t.Fatalf("expired new: %+v", out)
	}
}

// 未提供结束时间且未被替代的版本持续有效，查询的两种结束时间都应为空；
// 被替代后登记结束仍为空，实际结束显示交接点。
func TestOpenEndedVersionReplacement(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 1, Start: at(0)})

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if views[0].End != nil || views[0].EffectiveEnd != nil {
		t.Fatalf("open-ended version must have nil ends: %v %v", views[0].End, views[0].EffectiveEnd)
	}

	must(RegisterRequest{ItemID: "seat", VersionID: "new", UnitPrice: 1, Start: at(40), Replaces: "old"})
	views, err = b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	old, nv := views[0], views[1]
	if old.End != nil {
		t.Fatalf("registered end of replaced open-ended version must stay nil: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(at(40)) {
		t.Fatalf("effective end should be the handoff point: %v", old.EffectiveEnd)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("new open-ended version must have nil ends: %v %v", nv.End, nv.EffectiveEnd)
	}

	// 修改查询结果后重新查询，边界不变。
	if old.EffectiveEnd != nil {
		*old.EffectiveEnd = at(1)
	}
	again, _ := b.ItemVersions("seat")
	if again[0].EffectiveEnd == nil || !again[0].EffectiveEnd.Equal(at(40)) {
		t.Fatalf("handoff point shifted: %v", again[0].EffectiveEnd)
	}
}

// 登记时的区间重叠与替代窗口检查不受调用方事后改写时间的影响。
func TestOverlapCheckImmuneToRequestMutation(t *testing.T) {
	b := NewBook()
	oldEnd := atPtr(10)
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 1, Start: at(0), End: oldEnd})
	must(RegisterRequest{ItemID: "seat", VersionID: "new", UnitPrice: 1, Start: at(10)})

	// 登记后再试图重叠登记，仍按账本真实区间判断。
	*oldEnd = at(12)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "x", UnitPrice: 1, Start: at(10), End: atPtr(11),
	}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("overlap check must use ledger boundaries: %v", err)
	}
}

// 查询返回的实际结束时间被调用方改早后，后续的合法替代登记仍应成功：
// 版本信息只随合法的版本操作改变，视图修改不影响账本。
func TestReplacementWindowImmuneToViewMutation(t *testing.T) {
	b := NewBook()
	must := func(r RegisterRequest) {
		t.Helper()
		if err := b.RegisterVersion(r); err != nil {
			t.Fatalf("register %+v: %v", r, err)
		}
	}
	must(RegisterRequest{ItemID: "seat", VersionID: "old", UnitPrice: 1, Start: at(0), End: atPtr(100)})

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	// 调用方把实际结束时间从一百点改到三十点。
	*views[0].EffectiveEnd = at(30)

	// 四十点的替代登记落在真实有效区间内，应当成功。
	must(RegisterRequest{ItemID: "seat", VersionID: "new", UnitPrice: 1, Start: at(40), Replaces: "old"})

	again, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if again[0].EffectiveEnd == nil || !again[0].EffectiveEnd.Equal(at(40)) {
		t.Fatalf("replacement window used mutated boundary: %v", again[0].EffectiveEnd)
	}
}
