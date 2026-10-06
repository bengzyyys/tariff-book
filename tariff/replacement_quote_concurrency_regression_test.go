package tariff

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// 补充费率替代登记与旧版首次报价“同时发生”时的回归保障。
//
// 固定时间线（全部按 UTC 理解，开始含、结束不含）：
//
//	旧版 seat-v1：单价 150 分，2026-03-01 零点起，登记结束 2026-03-31 零点
//	新版 seat-v2：单价 180 分，2026-03-10 零点起替代 v1，不填结束时间
//	首次报价：数量 4，指定来源为 seat 的 v1，受理时刻固定为 2026-03-10 零点
//
// 由于受理时刻恰好落在新版生效的同一瞬间（v1 实际有效期为 [03-01, 03-10)），
// 两操作实际受理先后决定保存下来的首次结果：
//
//   - 报价先受理：v1 仍有效，保存并返回 150 分单价、600 分总价的确认，
//     拒绝原因为空；随后完成的替代登记把 v1 的实际有效期截断到 03-10 零点，
//     但不会回去改写已经受理的报价；
//   - 替代登记先完成：v1 实际有效期止于 03-10 零点，报价正常受理（err 为空）
//     但不确认，以 version_expired 拒绝，单价与总价均为零。
//
// 两种顺序下登记都必须成功，报价都正常受理（无错误返回），结果各字段不得
// 互相矛盾；账本不自动按 v2 的 180 分确认 720 分。谁先受理都允许，不规定
// 并发时必须由某一方先完成。
//
// 所有时刻写死并注入固定时钟，结论不随实际运行日期变化，也不要求每次并发
// 都出现同一种受理顺序：除真正的并发批次外，另以两个确定性测试分别钉死
// 两种受理顺序的完整字段，并发多轮只按实际顺序校验、不挑选获胜方。

var (
	rcOldStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rcOldEnd     = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	rcHandoff    = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	rcAcceptedAt = rcHandoff // 首次报价受理时刻固定为交接瞬间（UTC 零点）
)

const (
	rcItemID     = "seat"
	rcOldVersion = "seat-v1"
	rcNewVersion = "seat-v2"
	rcQuantity   = int64(4)
	rcOldPrice   = int64(150)
	rcNewPrice   = int64(180)
)

// rcNewBaseBook 建立场景起点：账本时钟固定在 3 月 10 日零点（交接瞬间），
// 只登记了旧版 v1（单价 150 分，[03-01, 03-31)），尚未登记替代版本。
// 返回账本、推进时钟的函数和引用旧版、数量 4 的首次报价请求（带调用方指定的标识）。
func rcNewBaseBook(t *testing.T, requestID string) (*Book, func(time.Time), QuoteRequest) {
	t.Helper()
	// 场景前提：受理时刻恰好是 v1 登记有效期内的最后一个含端点，
	// 也是新版开始替代的同一瞬间，因此受理先后会改变结论。
	if rcAcceptedAt.Before(rcOldStart) || !rcAcceptedAt.Before(rcOldEnd) {
		t.Fatal("测试前提不成立：首次受理时刻应落在 v1 登记有效期 [03-01, 03-31) 内")
	}
	now, setNow := fixedClock(rcAcceptedAt)
	b := NewBook(WithClock(now))
	oldEnd := rcOldEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: rcItemID, VersionID: rcOldVersion, UnitPrice: rcOldPrice,
		Start: rcOldStart, End: &oldEnd,
	}); err != nil {
		t.Fatalf("register %s: %v", rcOldVersion, err)
	}
	req := QuoteRequest{
		RequestID: requestID,
		ItemID:    rcItemID,
		VersionID: rcOldVersion,
		Quantity:  rcQuantity,
	}
	return b, setNow, req
}

// rcRegisterReplacement 在当前时钟下登记新版 v2：180 分，从交接瞬间起
// 替代 v1，不填结束时间。同项没有其他版本占用其有效时间，必须成功。
func rcRegisterReplacement(t *testing.T, b *Book) {
	t.Helper()
	if err := b.RegisterVersion(RegisterRequest{
		ItemID:    rcItemID,
		VersionID: rcNewVersion,
		UnitPrice: rcNewPrice,
		Start:     rcHandoff,
		Replaces:  rcOldVersion,
	}); err != nil {
		t.Fatalf("替代登记本身应当成功: %v", err)
	}
}

// rcAssertFirstQuote 按“实际受理先后”校验并发两操作结束后保存的首次报价结果：
//   - quoteFirst=true（报价先受理）：确认 150 分单价、600 分总价，拒绝原因为空；
//   - quoteFirst=false（替代登记先完成）：正常受理但拒绝，version_expired，
//     单价与总价均为零。
//
// 两种情况下 err 都应为空；返回结果都保留原请求标识、费率项、指定版本、
// 数量及 3 月 10 日零点的首次受理时刻；不能改用 v2 确认 720 分，也不能出现
// 确认状态、拒绝原因和金额互相矛盾的组合。
func rcAssertFirstQuote(t *testing.T, req QuoteRequest, out Outcome, err error, quoteFirst bool) {
	t.Helper()
	if err != nil {
		t.Fatalf("两种受理顺序下报价都是正常受理，错误应为空，got %v", err)
	}
	// 两种顺序共同的不变量：原请求内容（标识、费率项、指定版本、数量）
	// 与固定的首次受理时刻必须原样保留。
	if out.Request != req {
		t.Fatalf("结果必须保留原请求标识、费率项、指定版本与数量: %+v vs %+v", req, out.Request)
	}
	if !out.AcceptedAt.Equal(rcAcceptedAt) {
		t.Fatalf("首次受理时刻应为 3 月 10 日零点: %v", out.AcceptedAt)
	}

	if quoteFirst {
		if !out.Confirmed {
			t.Fatalf("旧版报价先受理应确认，不能被随后完成的替代登记改成拒绝: %+v", out)
		}
		if out.Reason != ReasonNone {
			t.Fatalf("先受理的确认结果拒绝原因应为空, got %q", out.Reason)
		}
		if out.UnitPrice != rcOldPrice {
			t.Fatalf("先受理应锁定 v1 单价 %d 分, got %d", rcOldPrice, out.UnitPrice)
		}
		if want := rcOldPrice * rcQuantity; out.Total != want {
			t.Fatalf("先受理应锁定总价 %d 分（150*4）, got %d", want, out.Total)
		}
	} else {
		if out.Confirmed {
			t.Fatalf("替代登记先完成时旧版报价不应确认，更不能自动按 v2 确认 720 分: %+v", out)
		}
		if out.Reason != ReasonVersionExpired {
			t.Fatalf("登记先完成时拒绝原因应为 version_expired, got %q", out.Reason)
		}
		if out.UnitPrice != 0 || out.Total != 0 {
			t.Fatalf("version_expired 拒绝的单价/总价均应为零: 单价=%d 总价=%d",
				out.UnitPrice, out.Total)
		}
	}
}

// rcAssertV2Registered 校验替代登记在并发结束后确实已完成：
// v1 登记结束仍是 3 月 31 日但实际有效结束被截断到交接瞬间并标记由 v2 替代；
// v2 从交接瞬间起持续有效，单价 180 分，替代 v1。
func rcAssertV2Registered(t *testing.T, b *Book) {
	t.Helper()
	views, err := b.ItemVersions(rcItemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("替代登记后应有两个版本，got %d: %+v", len(views), views)
	}
	old, nv := views[0], views[1]
	if old.VersionID != rcOldVersion || nv.VersionID != rcNewVersion {
		t.Fatalf("版本应按生效时刻排列为 v1/v2: %q, %q", old.VersionID, nv.VersionID)
	}
	if old.UnitPrice != rcOldPrice || old.End == nil || !old.End.Equal(rcOldEnd) {
		t.Fatalf("v1 登记信息被改写: 单价=%d 登记结束=%v", old.UnitPrice, old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(rcHandoff) {
		t.Fatalf("v1 实际有效结束应截断到 3 月 10 日零点, got %v", old.EffectiveEnd)
	}
	if old.SupersededBy != rcNewVersion {
		t.Fatalf("v1 应标记由 v2 替代, got SupersededBy=%q", old.SupersededBy)
	}
	if nv.UnitPrice != rcNewPrice || !nv.Start.Equal(rcHandoff) || nv.End != nil {
		t.Fatalf("v2 应为 180 分、3 月 10 日零点起、不填结束时间: %+v", nv)
	}
	if nv.Replaces != rcOldVersion || nv.EffectiveEnd != nil {
		t.Fatalf("v2 应替代 v1 且持续有效: Replaces=%q 实际结束=%v",
			nv.Replaces, nv.EffectiveEnd)
	}
}

// rcRaceResult 记录并发两操作中报价一方的实际返回。
type rcRaceResult struct {
	out        Outcome
	err        error
	regErr     error
	quoteFirst bool
}

// rcRunConcurrent 让首次报价与替代登记在同一屏障后同时进入账本，
// 制造两操作“同时发生”的真正并发。时钟始终停在交接瞬间。
// quoteFirstPosition 控制报价 goroutine 的发起位置（true 时先发起报价、
// 后发起登记，false 时相反）；调用方各轮交替发起位置，尽量让两种受理
// 顺序都被实际走到，但返回结论只按实际保存的结果判定，不假定谁获胜。
// 返回报价的实际结果、登记错误以及实际检测到的受理先后
// （以保存的首次结果形态判定：确认 600 分即报价先受理）。
func rcRunConcurrent(b *Book, req QuoteRequest, quoteFirstPosition bool) rcRaceResult {
	type op struct {
		isQuote bool
	}
	ops := []op{{isQuote: quoteFirstPosition}, {isQuote: !quoteFirstPosition}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var quoteOut Outcome
	var quoteErr, regErr error
	for _, o := range ops {
		wg.Add(1)
		go func(o op) {
			defer wg.Done()
			<-start
			if o.isQuote {
				quoteOut, quoteErr = b.Quote(req)
				return
			}
			regErr = b.RegisterVersion(RegisterRequest{
				ItemID: rcItemID, VersionID: rcNewVersion, UnitPrice: rcNewPrice,
				Start: rcHandoff, Replaces: rcOldVersion,
			})
		}(o)
	}
	close(start)
	wg.Wait()
	return rcRaceResult{
		out:        quoteOut,
		err:        quoteErr,
		regErr:     regErr,
		quoteFirst: quoteOut.Confirmed,
	}
}

// rcAssertStoredFirstAndFreshReject 覆盖两操作结束后的持久化保证：
//
//   - 原标识的 Lookup 与原内容原样重试都返回同一份首次结果
//     （先确认的 600 分继续有效；先拒绝的记录同样必须能查到），
//     不按替代后的费率重新计算、不改写首次受理时刻；
//   - 用从未使用过的新标识、相同数量引用 v1，结果确定为 version_expired
//     拒绝（err 为空、不确认、单价总价为零），不能沿用先前可能存在的确认价；
//   - 原记录与这次新拒绝各自按标识保存，互不覆盖。
//
// 校验在时钟推进到交接点之后进行，证明结论与“当前时间”无关。
func rcAssertStoredFirstAndFreshReject(t *testing.T, b *Book, setNow func(time.Time), req QuoteRequest, first Outcome, freshID string) {
	t.Helper()
	// 推进到两操作都结束之后的任意时刻（写死为 4 月 2 日 UTC），
	// 保证查询与重试的判定不依赖运行当天，也不依赖仍停在交接瞬间的时钟。
	later := time.Date(2026, 4, 2, 8, 30, 0, 0, time.UTC)
	setNow(later)

	// 原标识查询：与并发结束时保存的首次结果逐字段相同。
	got, err := b.Lookup(req.RequestID)
	if err != nil {
		t.Fatalf("原标识查询应取回首次结果: %v", err)
	}
	if got != first {
		t.Fatalf("Lookup 未返回同一份首次结果: 首次=%+v 查询=%+v", first, got)
	}

	// 原内容重试：同样返回首次结果（同值比较），受理时刻仍是 3 月 10 日零点，
	// 不能因为此刻 v1 已到期、v2 在生效就改判或改用新版价格。
	replay, err := b.Quote(req)
	if err != nil {
		t.Fatalf("原内容重试不应返回错误: %v", err)
	}
	if replay != first {
		t.Fatalf("原内容重试改写了首次结果: 首次=%+v 重试=%+v", first, replay)
	}
	if !replay.AcceptedAt.Equal(rcAcceptedAt) {
		t.Fatalf("重试改写了首次受理时刻: %v", replay.AcceptedAt)
	}

	// 从未使用过的新标识、相同数量引用 v1：此刻 v1 实际有效期止于 3 月 10 日，
	// 结果必须确定为 version_expired 拒绝——即使首次结果曾是 600 分确认也不沿用。
	freshReq := QuoteRequest{
		RequestID: freshID, ItemID: rcItemID, VersionID: rcOldVersion, Quantity: rcQuantity,
	}
	fresh, err := b.Quote(freshReq)
	if err != nil {
		t.Fatalf("新标识引用旧版属于正常受理，错误应为空, got %v", err)
	}
	if fresh.Confirmed {
		t.Fatalf("替代完成后新标识引用 v1 不能确认（不能沿用先前的确认价或自动改用 v2）: %+v", fresh)
	}
	if fresh.Reason != ReasonVersionExpired {
		t.Fatalf("新标识引用旧版应确定拒绝为 version_expired, got %q", fresh.Reason)
	}
	if fresh.UnitPrice != 0 || fresh.Total != 0 {
		t.Fatalf("新拒绝的单价/总价应为零: 单价=%d 总价=%d", fresh.UnitPrice, fresh.Total)
	}
	if fresh.Request != freshReq {
		t.Fatalf("新拒绝必须保留新标识与指定来源 v1: %+v vs %+v", freshReq, fresh.Request)
	}
	if !fresh.AcceptedAt.Equal(later) {
		t.Fatalf("新拒绝的首次受理时刻应为其提交时刻: %v", fresh.AcceptedAt)
	}

	// 原记录与新拒绝各自按标识保存，互不覆盖；两标识都能查到各自的结果。
	againFirst, err := b.Lookup(req.RequestID)
	if err != nil {
		t.Fatalf("新拒绝后原标识仍应可查: %v", err)
	}
	if againFirst != first {
		t.Fatalf("新拒绝覆盖了原首次结果: 首次=%+v 现状=%+v", first, againFirst)
	}
	gotFresh, err := b.Lookup(freshID)
	if err != nil {
		t.Fatalf("新拒绝记录应能按自己的标识查到: %v", err)
	}
	if gotFresh != fresh {
		t.Fatalf("新拒绝按标识取回的内容不一致: %+v vs %+v", fresh, gotFresh)
	}
}

// TestReplacementConcurrentQuoteFirstDeterministic 钉死“报价先受理”这一顺序：
// 先在交接瞬间完成引用 v1 的首次报价（150/600 确认），再登记 v2 替代。
// 替代登记成功，且不回头改写已受理的 600 分结果。
func TestReplacementConcurrentQuoteFirstDeterministic(t *testing.T) {
	b, setNow, req := rcNewBaseBook(t, "rc-sequential-quote-first")

	first, err := b.Quote(req)
	if err != nil {
		t.Fatalf("替代登记前报价应正常受理: %v", err)
	}
	rcAssertFirstQuote(t, req, first, err, true)

	rcRegisterReplacement(t, b)
	rcAssertV2Registered(t, b)

	// 登记后同标识查询取回的仍是那份 600 分确认，未被截断改判。
	got, err := b.Lookup(req.RequestID)
	if err != nil {
		t.Fatalf("替代登记后原标识应仍可查询: %v", err)
	}
	if got != first {
		t.Fatalf("替代登记改写了先受理的报价: 首次=%+v 查询=%+v", first, got)
	}

	rcAssertStoredFirstAndFreshReject(t, b, setNow, req, first, "rc-sequential-quote-first-fresh")
}

// TestReplacementConcurrentRegisterFirstDeterministic 钉死“替代登记先完成”
// 这一顺序：先登记 v2（成功），再在交接瞬间以新标识引用 v1 报价。
// 报价正常受理但以 version_expired 拒绝，金额为零，不自动改用 v2。
func TestReplacementConcurrentRegisterFirstDeterministic(t *testing.T) {
	b, setNow, req := rcNewBaseBook(t, "rc-sequential-register-first")

	rcRegisterReplacement(t, b)
	rcAssertV2Registered(t, b)

	first, err := b.Quote(req)
	rcAssertFirstQuote(t, req, first, err, false)
	// 被拒绝的首次结果同样占用并保存在原标识下，必须能查到。
	got, err := b.Lookup(req.RequestID)
	if err != nil {
		t.Fatalf("先拒绝的记录必须能按原标识查到: %v", err)
	}
	if got != first {
		t.Fatalf("Lookup 未返回那份 version_expired 首次结果: 首次=%+v 查询=%+v", first, got)
	}

	rcAssertStoredFirstAndFreshReject(t, b, setNow, req, first, "rc-sequential-register-first-fresh")
}

// TestReplacementRacesWithFirstQuote 让替代登记与旧版首次报价真正同时发生，
// 多轮独立账本重复执行，提高两种受理顺序在多次运行中都被实际走到的机会，
// 但不断言某一轮必须由哪一方先受理：每轮按保存结果反映出的实际先后做完整校验。
func TestReplacementRacesWithFirstQuote(t *testing.T) {
	const rounds = 16
	orders := map[string]int{"quote-first": 0, "register-first": 0}
	for round := 0; round < rounds; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			id := fmt.Sprintf("rc-race-%d", round)
			b, setNow, req := rcNewBaseBook(t, id)

			// 交替两个 goroutine 的发起位置，尽量让两种受理顺序都被实际走到；
			// 真正的胜负仍由调度决定，断言只按实际保存的结果进行。
			res := rcRunConcurrent(b, req, round%2 == 0)
			if res.regErr != nil {
				t.Fatalf("两种受理顺序下替代登记本身都应成功, got %v", res.regErr)
			}

			// 以保存的首次结果判定实际受理先后：v1 在交接瞬间有效即报价先到，
			// 已被截断即登记先完成。据此校验，不挑选也不假定获胜方。
			rcAssertFirstQuote(t, req, res.out, res.err, res.quoteFirst)
			rcAssertV2Registered(t, b)
			if res.quoteFirst {
				orders["quote-first"]++
			} else {
				orders["register-first"]++
			}

			// 并发结束后：原标识查询/原样重试取回同一份首次结果；
			// 新标识引用 v1 确定 version_expired；两记录按标识各自保存。
			rcAssertStoredFirstAndFreshReject(
				t, b, setNow, req, res.out, fmt.Sprintf("rc-race-fresh-%d", round))
		})
	}
	t.Logf("各轮实际受理顺序分布: %v（两种都允许，测试不依赖固定一方先完成）", orders)
}

// TestReplacementRaceReplayReturnsFirstAcrossRounds 确保并发批次中无论谁先受理，
// 原标识的再次并发/连续重试都稳定返回同一份首次结果，不会在重试时按更新后的
// 费率重新计算，也不会出现两次重试结果不一致。
func TestReplacementRaceReplayReturnsFirstAcrossRounds(t *testing.T) {
	const rounds = 10
	for round := 0; round < rounds; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			id := fmt.Sprintf("rc-race-replay-%d", round)
			b, _, req := rcNewBaseBook(t, id)

			res := rcRunConcurrent(b, req, round%2 == 0)
			if res.regErr != nil {
				t.Fatalf("替代登记应成功: %v", res.regErr)
			}
			first := res.out

			// 再并发提交两份相同内容的重试：都必须返回同一份首次结果。
			start := make(chan struct{})
			var wg sync.WaitGroup
			replays := make([]Outcome, 2)
			errs := make([]error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					replays[i], errs[i] = b.Quote(req)
				}(i)
			}
			close(start)
			wg.Wait()
			for i := 0; i < 2; i++ {
				if errs[i] != nil {
					t.Fatalf("第 %d 份重试不应返回错误: %v", i, errs[i])
				}
				if replays[i] != first {
					t.Fatalf("第 %d 份并发重试未返回同一份首次结果: 首次=%+v 重试=%+v",
						i, first, replays[i])
				}
			}

			got, err := b.Lookup(id)
			if err != nil || got != first {
				t.Fatalf("重试批次后原标识记录应保持为首次结果: %+v (%v)", got, err)
			}
		})
	}
}
