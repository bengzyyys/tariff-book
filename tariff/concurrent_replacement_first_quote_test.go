package tariff

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// “补充费率替代登记与旧版首次报价同时发生”场景的固定时间线
// （全部按 UTC 理解，开始时刻含、结束时刻不含）：
//
//	旧版 seat/v1：单价 150 分，2026-03-01 00:00 起生效，登记结束 2026-03-31 00:00
//	新版 seat/v2：单价 180 分，2026-03-10 00:00 起替代 v1，不填结束时间
//	首次报价：数量 4，首次受理时刻固定在交接点 2026-03-10 00:00，
//	         使用非空且未使用过的请求标识，指定来源为 v1
//
// 报价与替代登记经同一屏障同时发往同一本账本，保存下来的首次结果只取决于
// 实际受理先后，两种顺序都允许：
//
//   - 报价先受理：v1 的实际有效区间尚未被截断，交接点本身仍落在 [03-01, 03-31)
//     内，按 150 分确认 600 分，拒绝原因为空；替代登记随后正常成功；
//   - 替代登记先完成：v1 的实际有效结束被截到交接点，该点不包含在有效期内，
//     报价正常受理但以 version_expired 拒绝，单价、总价均为零。
//
// 测试不断言固定一方先完成，但保存结果必须与实际受理先后一致：替代登记不能把
// 已受理的 600 分确认改写成另一份结果，也不能自动改用 v2 确认 720 分；确认状态、
// 拒绝原因与金额不得出现互相矛盾的组合。两个操作结束后，原标识查询与原内容重试
// 都取回同一份首次结果；再用全新标识以相同数量引用 v1，必然得到 version_expired
// 拒绝，原记录与新拒绝各自按标识保存，互不覆盖。
//
// 所有日期写死并注入固定时钟，不读系统时钟：检查可重复运行，结论与运行当天
// 无关，也不要求每轮并发都出现同一种受理顺序。
var (
	hfrOldStart = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	hfrOldEnd   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	hfrHandoff  = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	// 两个操作结束后推进到的不同时刻，覆盖交接点本身、稍后与很久以后，
	// 证明后续查询结论不随观察时刻变化。
	hfrPostTimes = []time.Time{
		hfrHandoff,
		hfrHandoff.Add(time.Hour),
		hfrHandoff.Add(24 * time.Hour),
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}
)

const (
	hfrItemID     = "seat"
	hfrOldVersion = "v1"
	hfrNewVersion = "v2"
	hfrQuantity   = int64(4)
	hfrRounds     = 16 // 并发轮数，提高两种受理顺序在同一进程内都被走到的机会
)

// hfrConcurrentResult 记录同时发生的两个操作各自拿到的真实返回。
type hfrConcurrentResult struct {
	quote    Outcome
	quoteErr error
	regErr   error
}

// hfrNewBaseBook 建立场景起点：账本中只有旧版 v1（150 分，3 月 1 日至 3 月 31 日），
// 受理时钟固定在交接点 3 月 10 日零点。
func hfrNewBaseBook(t *testing.T, clock time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(clock)
	b := NewBook(WithClock(now))
	oldEnd := hfrOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: hfrItemID, VersionID: hfrOldVersion, UnitPrice: 150,
		Start: hfrOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register seat/v1: %v", err)
	}
	return b, setNow
}

// hfrRaceQuoteAndReplacement 经同一屏障同时放行两个操作：一笔非空新标识、
// 数量 4、指定 v1 的首次报价，和一份 v2（180 分，交接点起替代 v1，无结束时间）
// 的替代登记。两个 goroutine 一起进入账本，实际谁先受理由调度决定。
// quoteFirst 只控制 goroutine 的发起位置，逐轮交替以提高两种受理顺序
// 都被实际走到的机会，本身不决定账本内的先后。
func hfrRaceQuoteAndReplacement(b *Book, req QuoteRequest, quoteFirst bool) hfrConcurrentResult {
	var res hfrConcurrentResult
	start := make(chan struct{})
	var wg sync.WaitGroup

	quote := func() {
		defer wg.Done()
		<-start // 两个操作都就绪后一起进入账本，制造真正的同时发生
		res.quote, res.quoteErr = b.Quote(req)
	}
	register := func() {
		defer wg.Done()
		<-start
		res.regErr = b.RegisterVersion(RegisterRequest{
			ItemID:    hfrItemID,
			VersionID: hfrNewVersion,
			UnitPrice: 180,
			Start:     hfrHandoff,
			Replaces:  hfrOldVersion,
		})
	}
	wg.Add(2)
	if quoteFirst {
		go quote()
		go register()
	} else {
		go register()
		go quote()
	}
	close(start)
	wg.Wait()
	return res
}

// hfrAssertReplacementSucceeded 校验替代登记本身成功且账本中恰好存在 v1/v2：
// v2 单价 180、从交接点起持续有效并保留替代来源；v1 登记结束仍是 3 月 31 日，
// 实际有效结束被截到交接点。任何其他错误或账本形态都视为破坏场景前提。
func hfrAssertReplacementSucceeded(t *testing.T, b *Book, regErr error) {
	t.Helper()
	if regErr != nil {
		t.Fatalf("替代登记本身应当成功，got %v", regErr)
	}
	views, err := b.ItemVersions(hfrItemID)
	if err != nil {
		t.Fatalf("查询费率项版本: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("替代登记后应恰好有 v1/v2 两个版本, got %d: %+v", len(views), views)
	}
	var old, nv *VersionView
	for i := range views {
		switch views[i].VersionID {
		case hfrOldVersion:
			old = &views[i]
		case hfrNewVersion:
			nv = &views[i]
		}
	}
	if old == nil || nv == nil {
		t.Fatalf("版本列表缺少 v1 或 v2: %+v", views)
	}
	if old.UnitPrice != 150 {
		t.Fatalf("v1 单价不应被替代登记改写: %d", old.UnitPrice)
	}
	if old.End == nil || !old.End.Equal(hfrOldEnd) {
		t.Fatalf("v1 登记结束应仍为 3 月 31 日: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(hfrHandoff) {
		t.Fatalf("v1 实际有效结束应被截到交接点 3 月 10 日零点: %v", old.EffectiveEnd)
	}
	if old.SupersededBy != hfrNewVersion {
		t.Fatalf("v1 应标记为被 v2 替代, SupersededBy=%q", old.SupersededBy)
	}
	if nv.UnitPrice != 180 || !nv.Start.Equal(hfrHandoff) || nv.Replaces != hfrOldVersion {
		t.Fatalf("v2 登记信息异常: %+v", nv)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("v2 不填结束时间，登记结束与实际结束都应为空: %+v", nv)
	}
}

// hfrAssertRaceQuoteResult 按实际受理先后校验首次报价结果，返回实际走中的分支：
// "confirmed"（报价先受理，600 分确认）或 "expired"（替代先完成，拒绝）。
//
// 两种结果共同要求：报价调用 err 为空；原请求标识、费率项、指定版本、数量
// 原样保留；首次受理时刻固定在 3 月 10 日零点。同时排除自相矛盾的组合：
// 不能自动改用 v2 确认 720 分；确认必须无拒绝原因且金额为 150/600；
// version_expired 拒绝必须未确认且单价、总价均为零。
func hfrAssertRaceQuoteResult(t *testing.T, req QuoteRequest, res hfrConcurrentResult) string {
	t.Helper()
	if res.quoteErr != nil {
		t.Fatalf("确认与 version_expired 拒绝都是正常受理，报价 err 应为空, got %v", res.quoteErr)
	}
	out := res.quote
	if out.Request != req {
		t.Fatalf("首次结果必须原样保留标识/费率项/指定版本/数量: %+v vs %+v", out.Request, req)
	}
	if out.Request.VersionID != hfrOldVersion {
		t.Fatalf("首次结果的指定版本必须仍是 v1，不能被换成 v2: %q", out.Request.VersionID)
	}
	if !out.AcceptedAt.Equal(hfrHandoff) {
		t.Fatalf("首次受理时刻=%v，want %v", out.AcceptedAt, hfrHandoff)
	}
	switch {
	case out.Confirmed:
		if out.Reason != ReasonNone {
			t.Fatalf("确认结果的拒绝原因应为空, got %q: %+v", out.Reason, out)
		}
		if out.UnitPrice != 150 || out.Total != 600 {
			t.Fatalf("报价先受理应按 v1 的 150 分确认 600 分，不能改用 v2 的 180/720: 单价=%d 总价=%d",
				out.UnitPrice, out.Total)
		}
		return "confirmed"
	case out.Reason == ReasonVersionExpired:
		if out.Confirmed {
			t.Fatalf("version_expired 拒绝不能同时带确认状态: %+v", out)
		}
		if out.UnitPrice != 0 || out.Total != 0 {
			t.Fatalf("version_expired 拒绝的单价/总价应均为零, got %d/%d", out.UnitPrice, out.Total)
		}
		return "expired"
	default:
		t.Fatalf("首次结果只允许 600 分确认或 version_expired 拒绝，得到矛盾或越界组合: %+v", out)
	}
	return ""
}

// hfrAssertStoredFirstResult 校验两个操作都结束后，按原标识 Lookup 与原内容重试
// 返回的都是同一份首次结果：先确认的 600 分继续有效，先拒绝的记录也必须能查到，
// 二者都不会因替代登记落地而按更新后的费率重新计算。
func hfrAssertStoredFirstResult(t *testing.T, b *Book, req QuoteRequest, first Outcome) {
	t.Helper()
	got, err := b.Lookup(req.RequestID)
	if err != nil {
		t.Fatalf("原标识查询首次结果: %v", err)
	}
	if got != first {
		t.Fatalf("查询未取回保存的首次结果: 首次=%+v 查询=%+v", first, got)
	}
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatalf("原内容重试不应返回错误: %v", err)
	}
	if replay != first {
		t.Fatalf("原内容重试必须返回同一份首次结果，不能按更新后的费率重算: 首次=%+v 重试=%+v",
			first, replay)
	}
}

// hfrAssertFreshV1Rejected 在替代登记必然已完成之后，用从未使用过的新标识、
// 相同数量 4 再次引用 v1：结果必须确定为 version_expired 拒绝，err 为空，
// 单价、总价为零，受理时刻取自注入时钟 at；不能沿用先前可能存在的 600 分
// 确认价，也不能自动改按 v2 确认。新拒绝按新标识保存，与原记录互不覆盖。
func hfrAssertFreshV1Rejected(t *testing.T, b *Book, firstReq QuoteRequest, first Outcome, freshID string, at time.Time) {
	t.Helper()
	freshReq := QuoteRequest{
		RequestID: freshID,
		ItemID:    hfrItemID,
		VersionID: hfrOldVersion,
		Quantity:  hfrQuantity,
	}
	fresh, err := b.Quote(freshReq)
	if err != nil {
		t.Fatalf("确认后的 version_expired 拒绝也是正常受理，err 应为空: %v", err)
	}
	if fresh.Confirmed {
		t.Fatalf("替代登记完成后新标识引用 v1 必须拒绝，不能沿用先前的确认价或改用 v2: %+v", fresh)
	}
	if fresh.Reason != ReasonVersionExpired {
		t.Fatalf("新报价拒绝原因=%q，want %q", fresh.Reason, ReasonVersionExpired)
	}
	if fresh.UnitPrice != 0 || fresh.Total != 0 {
		t.Fatalf("新拒绝的单价/总价应为零: %d/%d", fresh.UnitPrice, fresh.Total)
	}
	if fresh.Request != freshReq {
		t.Fatalf("新拒绝必须保留新标识与 v1 来源等原请求内容: %+v vs %+v", fresh.Request, freshReq)
	}
	if !fresh.AcceptedAt.Equal(at) {
		t.Fatalf("新拒绝受理时刻=%v，want %v", fresh.AcceptedAt, at)
	}

	// 新拒绝与原记录各按各的标识保存，互不覆盖。
	gotFresh, err := b.Lookup(freshID)
	if err != nil {
		t.Fatalf("新拒绝按标识查询: %v", err)
	}
	if gotFresh != fresh {
		t.Fatalf("查询未取回新拒绝记录: %+v vs %+v", gotFresh, fresh)
	}
	gotFirst, err := b.Lookup(firstReq.RequestID)
	if err != nil {
		t.Fatalf("原标识查询: %v", err)
	}
	if gotFirst != first {
		t.Fatalf("新拒绝覆盖了原首次记录: 首次=%+v 现查=%+v", first, gotFirst)
	}
}

// TestConcurrentReplacementRegistrationVersusFirstQuote 是本场景的核心回归：
// 替代登记与旧版首次报价同时发生，保存下来的结果必须符合实际受理先后。
//
// 多轮独立账本重复执行并交替两个操作的发起位置，但不断言某一轮固定谁先受理：
// 每轮只校验“实际走中的分支”与保存结果完全一致。两个顺序确定性的分支语义
// 另由 TestReplacementAfterFirstQuoteKeepsConfirmed600 与
// TestFirstQuoteAfterReplacementRejectsVersionExpired 在每次运行中固定覆盖。
func TestConcurrentReplacementRegistrationVersusFirstQuote(t *testing.T) {
	// 测试前提：交接点落在 v1 登记的半开有效期 [03-01, 03-31) 内，
	// 替代登记在该账本下合法；交接点本身在截断前对 v1 有效、截断后对 v1 失效。
	if !hfrHandoff.After(hfrOldStart) || !hfrHandoff.Before(hfrOldEnd) {
		t.Fatal("测试前提不成立：交接点应严格晚于 v1 开始且早于 v1 登记结束")
	}

	branches := map[string]int{}
	for round := 0; round < hfrRounds; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			// 并发批次的受理时钟始终固定在交接点，与运行当天无关；
			// 登记不读时钟，报价读这个注入时刻作为首次受理时刻。
			b, setNow := hfrNewBaseBook(t, hfrHandoff)
			req := QuoteRequest{
				RequestID: fmt.Sprintf("hfr-first-%02d", round),
				ItemID:    hfrItemID,
				VersionID: hfrOldVersion,
				Quantity:  hfrQuantity,
			}

			res := hfrRaceQuoteAndReplacement(b, req, round%2 == 0)
			hfrAssertReplacementSucceeded(t, b, res.regErr)
			branch := hfrAssertRaceQuoteResult(t, req, res)
			branches[branch]++
			first := res.quote

			// 替代登记必然落地：交接点此刻的当前生效版本已是 v2。
			// 即便账本已经“换版”，保存的首次结果（600 分确认或拒绝）都不得被改写。
			view, err := b.EffectiveVersionAt(hfrItemID, hfrHandoff)
			if err != nil {
				t.Fatalf("替代登记后交接点应能查到生效版本: %v", err)
			}
			if view.VersionID != hfrNewVersion || view.UnitPrice != 180 {
				t.Fatalf("替代登记后交接点应选中 v2/180, got %s/%d", view.VersionID, view.UnitPrice)
			}

			// 推进时钟后再查询与原样重试：仍是同一份首次结果，不按当前费率重算。
			postAt := hfrPostTimes[round%len(hfrPostTimes)]
			setNow(postAt)
			hfrAssertStoredFirstResult(t, b, req, first)

			// 全新标识、相同数量引用 v1：无论首次走中哪个分支，
			// 此时都确定为 version_expired 拒绝，与原记录分别保存。
			hfrAssertFreshV1Rejected(t, b, req, first, fmt.Sprintf("hfr-fresh-%02d", round), postAt)
		})
	}
	t.Logf("各轮实际受理先后分布: %v（confirmed=报价先受理的 600 分确认；expired=替代先完成的 version_expired 拒绝，两种都允许）",
		branches)
}

// TestReplacementAfterFirstQuoteKeepsConfirmed600 固定覆盖“报价先受理”分支：
// 交接点本身先取得 v1 的 600 分确认，随后替代登记成功，已受理的确认结果
// 不被改判或换版；之后新标识引用 v1 必然 version_expired。
func TestReplacementAfterFirstQuoteKeepsConfirmed600(t *testing.T) {
	b, setNow := hfrNewBaseBook(t, hfrHandoff)
	req := QuoteRequest{
		RequestID: "hfr-seq-confirmed", ItemID: hfrItemID, VersionID: hfrOldVersion, Quantity: hfrQuantity,
	}

	first, err := b.Quote(req)
	if err != nil {
		t.Fatalf("替代登记前的首次报价是正常受理，err 应为空: %v", err)
	}
	if !first.Confirmed || first.UnitPrice != 150 || first.Total != 600 || first.Reason != ReasonNone {
		t.Fatalf("交接点截断前 v1 仍有效，应 150/600 确认且无拒绝原因: %+v", first)
	}
	if !first.AcceptedAt.Equal(hfrHandoff) || first.Request != req {
		t.Fatalf("首次结果的受理时刻/请求内容异常: %+v", first)
	}

	if err := b.RegisterVersion(RegisterRequest{
		ItemID: hfrItemID, VersionID: hfrNewVersion, UnitPrice: 180,
		Start: hfrHandoff, Replaces: hfrOldVersion,
	}); err != nil {
		t.Fatalf("替代登记本身应当成功: %v", err)
	}

	// 账本已换版，已受理的 600 分确认原样保留：查询、原样重试都是同一份结果。
	setNow(hfrHandoff.Add(24 * time.Hour))
	hfrAssertStoredFirstResult(t, b, req, first)

	// 新标识引用 v1 确定拒绝，不能沿用 600 分确认价，也不能改按 v2 出 720 分。
	hfrAssertFreshV1Rejected(t, b, req, first, "hfr-seq-confirmed-fresh", hfrHandoff.Add(24*time.Hour))
}

// TestFirstQuoteAfterReplacementRejectsVersionExpired 固定覆盖“替代登记先完成”
// 分支：交接点报价正常受理但以 version_expired 拒绝，金额为零且来源仍是 v1；
// 拒绝记录按标识保存并可原样重试，之后任何新标识引用 v1 仍得到同样的拒绝。
func TestFirstQuoteAfterReplacementRejectsVersionExpired(t *testing.T) {
	b, _ := hfrNewBaseBook(t, hfrHandoff)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: hfrItemID, VersionID: hfrNewVersion, UnitPrice: 180,
		Start: hfrHandoff, Replaces: hfrOldVersion,
	}); err != nil {
		t.Fatalf("替代登记本身应当成功: %v", err)
	}

	req := QuoteRequest{
		RequestID: "hfr-seq-expired", ItemID: hfrItemID, VersionID: hfrOldVersion, Quantity: hfrQuantity,
	}
	first, err := b.Quote(req)
	if err != nil {
		t.Fatalf("version_expired 拒绝是正常受理，err 应为空: %v", err)
	}
	if first.Confirmed {
		t.Fatalf("v1 实际有效结束已截到交接点（不含该点），不能确认: %+v", first)
	}
	if first.Reason != ReasonVersionExpired {
		t.Fatalf("拒绝原因=%q，want %q", first.Reason, ReasonVersionExpired)
	}
	if first.UnitPrice != 0 || first.Total != 0 {
		t.Fatalf("拒绝结果单价/总价应均为零: %d/%d", first.UnitPrice, first.Total)
	}
	if first.Request != req || !first.AcceptedAt.Equal(hfrHandoff) {
		t.Fatalf("拒绝结果必须保留原请求内容与交接点受理时刻: %+v", first)
	}

	// 拒绝同样占用标识：查询与原样重试都取回同一份首次拒绝，不重算、不换版。
	hfrAssertStoredFirstResult(t, b, req, first)

	// 又一个全新标识、相同数量引用 v1：仍是确定的 version_expired 拒绝，
	// 与先前的拒绝记录各自按标识保存，互不覆盖。
	hfrAssertFreshV1Rejected(t, b, req, first, "hfr-seq-expired-fresh", hfrHandoff)
}
