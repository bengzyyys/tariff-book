package tariff

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// 同一本尚未登记 seat 的账本，同时收到两份有效期重叠的登记时的并发回归测试。
//
// 两份登记都不填写替代来源，其余内容合法（全部为 UTC 零点，开始含、结束不含）：
//
//	v-a：单价 150 分，2026-03-01 起、2026-03-20 结束
//	v-b：单价 180 分，2026-03-10 起、2026-03-31 结束
//
// 两者在 2026-03-10 至 2026-03-20 之间重叠。测试保护既有规则：并发受理顺序
// 决定哪一版入库，但无论谁先抢到账本锁，都必须恰好一版成功、另一版返回可用
// errors.Is 识别的 ErrOverlap；不同版本标识不能绕过“同项有效期不得重叠”，
// 也不允许两份登记各自成功。失败登记不能覆盖成功方的任何字段，也不会占用
// 落败版本标识。
//
// 这里强调“同时发生”：两个 goroutine 经同一屏障一起进入 RegisterVersion，
// 而不是先顺序登记一版、再提交另一版。业务结论只依赖登记区间与受理时刻，
// 因此多轮重复执行、并把账本时钟注入到不同的运行日期（早于生效点、共同
// 有效当天、两版都结束之后），结论都一致，不依赖等待真实时间流逝。

const (
	corItemID   = "seat"
	corVersionA = "v-a"
	corVersionB = "v-b"
	corRounds   = 12 // 每个运行日期下的并发轮数，提高两种受理顺序都被覆盖的机会
)

var (
	corVAStart    = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	corVAEnd      = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	corVBStart    = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	corVBEnd      = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	corCommonDay  = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // 两版原计划共同有效的一天
	corQuantity   = int64(4)
	corRunOffsets = []time.Time{
		time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC), // 远早于任一版本生效
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),    // 首版开始当天
		corCommonDay, // 两版共同有效当天
		time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), // 两版都已结束之后
	}
)

// corCandidate 描述一份参与并发竞争的登记内容。
type corCandidate struct {
	versionID string
	unitPrice int64
	start     time.Time
	end       time.Time
}

func corCandidates() (corCandidate, corCandidate) {
	return corCandidate{
			versionID: corVersionA,
			unitPrice: 150,
			start:     corVAStart,
			end:       corVAEnd,
		}, corCandidate{
			versionID: corVersionB,
			unitPrice: 180,
			start:     corVBStart,
			end:       corVBEnd,
		}
}

// corResult 记录某一份并发登记实际拿到的返回。
type corResult struct {
	versionID string
	err       error
}

// corRegisterBoth 把两份登记同时发往同一本账本：goroutine 在同一屏障后一起
// 进入 RegisterVersion，制造对同一空费率项的真正并发；每份请求使用独立的
// 结束时刻副本，互不共享指针。调用方通过 first/second 控制发起顺序，
// 以便各轮交替两个版本的发起位置，让两种受理顺序都被实际走到；
// 返回值按版本标识归集，顺序本身不参与任何断言。
func corRegisterBoth(b *Book, first, second corCandidate) []corResult {
	ordered := []corCandidate{first, second}
	results := make([]corResult, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for idx, cand := range ordered {
		wg.Add(1)
		go func(idx int, cand corCandidate) {
			defer wg.Done()
			<-start // 两份都就绪后一起进入登记
			end := cand.end
			err := b.RegisterVersion(RegisterRequest{
				ItemID:    corItemID,
				VersionID: cand.versionID,
				UnitPrice: cand.unitPrice,
				Start:     cand.start,
				End:       &end, // Replaces 留空：两份登记都不填写替代来源
			})
			results[idx] = corResult{versionID: cand.versionID, err: err}
		}(idx, cand)
	}
	close(start)
	wg.Wait()
	return results
}

func corCandidateByID(t *testing.T, cands []corCandidate, id string) corCandidate {
	t.Helper()
	for _, c := range cands {
		if c.versionID == id {
			return c
		}
	}
	t.Fatalf("未知候选版本 %q", id)
	return corCandidate{}
}

// corAssertExactlyOneWins 校验并发批次恰好一次成功、一次 ErrOverlap，
// 返回实际成功方与落败方的登记内容。两份各自成功、两份都失败或失败方拿到
// ErrOverlap 以外的错误，都视为破坏既有规则。
func corAssertExactlyOneWins(t *testing.T, results []corResult, cands []corCandidate) (corCandidate, corCandidate) {
	t.Helper()
	var success, rejected []corResult
	for _, r := range results {
		if r.err == nil {
			success = append(success, r)
			continue
		}
		if !errors.Is(r.err, ErrOverlap) {
			t.Fatalf("版本 %s 并发登记落败时必须返回 ErrOverlap, got %v", r.versionID, r.err)
		}
		rejected = append(rejected, r)
	}
	if len(success) != 1 {
		t.Fatalf("成功登记次数=%d，应恰好为 1（不能两版各自成功，也不能都失败）；结果: %+v",
			len(success), results)
	}
	if len(rejected) != 1 {
		t.Fatalf("ErrOverlap 次数=%d，应恰好为 1；结果: %+v", len(rejected), results)
	}
	winner := corCandidateByID(t, cands, success[0].versionID)
	loser := corCandidateByID(t, cands, rejected[0].versionID)
	if winner.versionID == loser.versionID {
		t.Fatalf("成功方与落败方不应是同一版本: %q", winner.versionID)
	}
	return winner, loser
}

// corAssertOnlyWinnerStored 校验 ItemVersions 只列出实际成功的那一版：
// 单价、登记起止、实际有效区间都与该请求一致，两种替代关系均为空，
// 结果中不能混入落败方的任何字段。
func corAssertOnlyWinnerStored(t *testing.T, b *Book, winner corCandidate) {
	t.Helper()
	views, err := b.ItemVersions(corItemID)
	if err != nil {
		t.Fatalf("查询成功登记版本: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("并发登记后应只有一版入库，got %d: %+v", len(views), views)
	}
	v := views[0]
	if v.ItemID != corItemID || v.VersionID != winner.versionID {
		t.Fatalf("入库版本=%s/%s，want %s/%s（落败方不得残留）",
			v.ItemID, v.VersionID, corItemID, winner.versionID)
	}
	if v.UnitPrice != winner.unitPrice {
		t.Fatalf("入库单价=%d，want %d；失败登记不能覆盖成功方单价",
			v.UnitPrice, winner.unitPrice)
	}
	if !v.Start.Equal(winner.start) || !v.EffectiveStart.Equal(winner.start) {
		t.Fatalf("入库版本起点异常: 登记=%v 实际=%v, want %v",
			v.Start, v.EffectiveStart, winner.start)
	}
	if v.End == nil || !v.End.Equal(winner.end) ||
		v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(winner.end) {
		t.Fatalf("入库版本终点异常: 登记=%v 实际=%v, want %v",
			v.End, v.EffectiveEnd, winner.end)
	}
	if v.Replaces != "" || v.SupersededBy != "" {
		t.Fatalf("两份登记都未填写替代来源，入库版本两种替代关系应为空: Replaces=%q SupersededBy=%q",
			v.Replaces, v.SupersededBy)
	}
}

// corAssertLoserLeftNoTrace 用落败方的同一内容再顺序提交一次：
// 仍应因区间重叠返回 ErrOverlap，而不是 ErrVersionExists 或成功——
// 失败的并发登记既没有把版本存进去，也没有占用落败版本标识。
func corAssertLoserLeftNoTrace(t *testing.T, b *Book, loser corCandidate) {
	t.Helper()
	end := loser.end
	err := b.RegisterVersion(RegisterRequest{
		ItemID: corItemID, VersionID: loser.versionID, UnitPrice: loser.unitPrice,
		Start: loser.start, End: &end,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("落败版本 %s 重提应仍为 ErrOverlap（标识未被占用、区间仍冲突）, got %v",
			loser.versionID, err)
	}
}

// corAssertEffectiveVersionAtCommonDay 校验在两版原计划共同有效的 3 月 15 日，
// EffectiveVersionAt 必须选中实际成功方；同一瞬间换时区表示结论一致。
// 落败方在任何时刻都不得被选中：2 月 25 日两版都未生效；3 月 31 日两版区间
// 都已结束（结束时刻不含）；3 月 25 日只有当成功方是 v-b 时才可能选中 v-b。
func corAssertEffectiveVersionAtCommonDay(t *testing.T, b *Book, winner, loser corCandidate) {
	t.Helper()

	view, err := b.EffectiveVersionAt(corItemID, corCommonDay)
	if err != nil {
		t.Fatalf("3 月 15 日应选中成功方 %s: %v", winner.versionID, err)
	}
	if view.VersionID != winner.versionID || view.UnitPrice != winner.unitPrice {
		t.Fatalf("3 月 15 日选中 %s（%d 分），want %s（%d 分）",
			view.VersionID, view.UnitPrice, winner.versionID, winner.unitPrice)
	}

	// 同一瞬间的 +08:00 表示（3 月 15 日 08:00）必须得到同一个版本。
	east := time.FixedZone("UTC+8", 8*3600)
	sameInstant := time.Date(2026, 3, 15, 8, 0, 0, 0, east)
	other, err := b.EffectiveVersionAt(corItemID, sameInstant)
	if err != nil {
		t.Fatalf("同一瞬间换时区表示也应选中成功方: %v", err)
	}
	if other.VersionID != winner.versionID {
		t.Fatalf("同一瞬间不同时区表示选中了不同版本: %q vs %q",
			other.VersionID, winner.versionID)
	}

	// 早于任一版本开始（2 月 25 日）：无生效版本，不能补位。
	if _, err := b.EffectiveVersionAt(corItemID, time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("2 月 25 日应返回 ErrNoEffectiveVersion, got %v", err)
	}
	// 3 月 31 日零点：v-a 早已结束，v-b 的结束时刻也不含该点。
	if _, err := b.EffectiveVersionAt(corItemID, corVBEnd); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("3 月 31 日应返回 ErrNoEffectiveVersion, got %v", err)
	}
	// 3 月 25 日：成功方为 v-a 时已无生效版本；成功方为 v-b 时只能选中 v-b。
	got, err := b.EffectiveVersionAt(corItemID, time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC))
	switch winner.versionID {
	case corVersionA:
		if !errors.Is(err, ErrNoEffectiveVersion) {
			t.Fatalf("v-a 成功时 3 月 25 日应无生效版本, got %+v (%v)", got, err)
		}
	case corVersionB:
		if err != nil || got.VersionID != corVersionB {
			t.Fatalf("v-b 成功时 3 月 25 日应仍选中 v-b, got %+v (%v)", got, err)
		}
	}
	if got.VersionID == loser.versionID {
		t.Fatalf("3 月 25 日选中了落败版本 %s", loser.versionID)
	}
}

// corAssertQuotesFollowWinner 把新报价的受理时刻设在 3 月 15 日，分别用
// 未使用过的请求标识引用两个版本，数量均为 4：成功方按其单价确认
// （150 分 → 600 分；180 分 → 720 分）并保留对应版本来源；落败方正常受理
// 但以 version_not_found 拒绝，单价与总价均为零，调用本身不返回错误。
func corAssertQuotesFollowWinner(t *testing.T, b *Book, winner, loser corCandidate, runLabel string, round int) {
	t.Helper()
	winReq := QuoteRequest{
		RequestID: fmt.Sprintf("cor-quote-win-%s-%d", runLabel, round),
		ItemID:    corItemID, VersionID: winner.versionID, Quantity: corQuantity,
	}
	winOut, err := b.Quote(winReq)
	if err != nil {
		t.Fatalf("引用成功方 %s 的报价是正常受理，err 应为空: %v", winner.versionID, err)
	}
	if !winOut.Confirmed {
		t.Fatalf("引用成功方 %s 应确认, got %+v", winner.versionID, winOut)
	}
	if winOut.Reason != ReasonNone {
		t.Fatalf("成功方确认结果不应带拒绝原因: %q", winOut.Reason)
	}
	if winOut.UnitPrice != winner.unitPrice {
		t.Fatalf("成功方报价单价=%d，want %d", winOut.UnitPrice, winner.unitPrice)
	}
	wantTotal := winner.unitPrice * corQuantity // 150*4=600 或 180*4=720
	if wantTotal != 600 && wantTotal != 720 {
		t.Fatalf("测试数据总价应为 600 或 720, got %d", wantTotal)
	}
	if winOut.Total != wantTotal {
		t.Fatalf("成功方报价总价=%d，want %d", winOut.Total, wantTotal)
	}
	if winOut.Request != winReq {
		t.Fatalf("成功方报价来源必须保留原请求: %+v vs %+v", winOut.Request, winReq)
	}
	if !winOut.AcceptedAt.Equal(corCommonDay) {
		t.Fatalf("成功方受理时刻=%v，want %v", winOut.AcceptedAt, corCommonDay)
	}

	loseReq := QuoteRequest{
		RequestID: fmt.Sprintf("cor-quote-lose-%s-%d", runLabel, round),
		ItemID:    corItemID, VersionID: loser.versionID, Quantity: corQuantity,
	}
	loseOut, err := b.Quote(loseReq)
	if err != nil {
		t.Fatalf("引用落败方 %s 的拒绝也是正常受理，调用不应返回错误, got %v", loser.versionID, err)
	}
	if loseOut.Confirmed {
		t.Fatalf("落败版本未入库，不能确认报价: %+v", loseOut)
	}
	if loseOut.Reason != ReasonVersionNotFound {
		t.Fatalf("落败版本报价原因=%q，want %q", loseOut.Reason, ReasonVersionNotFound)
	}
	if loseOut.UnitPrice != 0 || loseOut.Total != 0 {
		t.Fatalf("落败版本报价单价/总价应均为零: %d/%d", loseOut.UnitPrice, loseOut.Total)
	}
	if loseOut.Request != loseReq {
		t.Fatalf("拒绝结果必须保留调用方指定的落败版本来源: %+v vs %+v", loseOut.Request, loseReq)
	}
	if !loseOut.AcceptedAt.Equal(corCommonDay) {
		t.Fatalf("落败方受理时刻=%v，want %v", loseOut.AcceptedAt, corCommonDay)
	}

	// 首次受理结果各自按请求标识保存，查询结论与本次实际受理一致。
	storedWin, err := b.Lookup(winReq.RequestID)
	if err != nil || storedWin != winOut {
		t.Fatalf("成功方报价未按标识保存: %+v (%v)", storedWin, err)
	}
	storedLose, err := b.Lookup(loseReq.RequestID)
	if err != nil || storedLose != loseOut {
		t.Fatalf("落败方拒绝未按标识保存: %+v (%v)", storedLose, err)
	}
}

// TestConcurrentOverlappingRegistrationSingleVersionStored：空账本同时接收
// v-a、v-b 两份有效期重叠的登记，保护“同一项同时刻至多一版入库”的既有规则。
// 哪一版成功由实际受理顺序决定，测试不断言固定获胜方，但对任一获胜结果都
// 完整校验登记视图、时刻选版与两条报价结论；多轮执行并注入不同运行日期，
// 业务结论必须相同。
func TestConcurrentOverlappingRegistrationSingleVersionStored(t *testing.T) {
	allCands := []corCandidate{}
	a, c := corCandidates()
	allCands = append(allCands, a, c)

	winners := map[string]int{}
	for _, runDate := range corRunOffsets {
		runLabel := runDate.Format("2006-01-02")
		t.Run("rundate-"+runLabel, func(t *testing.T) {
			for round := 0; round < corRounds; round++ {
				t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
					// 账本时钟被固定在某个“运行日期”上：登记不看时钟，
					// 报价受理时刻随后显式设到 3 月 15 日，无需等待真实时间。
					now, setNow := fixedClock(runDate)
					b := NewBook(WithClock(now))

					// 两个 goroutine 经屏障同时放行；逐轮交替发起位置只是让两种
					// 受理顺序都有机会被实际走到，不断言哪一版必然获胜。
					first, second := a, c
					if round%2 == 1 {
						first, second = c, a
					}
					results := corRegisterBoth(b, first, second)
					winner, loser := corAssertExactlyOneWins(t, results, allCands)
					winners[winner.versionID]++

					corAssertOnlyWinnerStored(t, b, winner)
					corAssertLoserLeftNoTrace(t, b, loser)
					corAssertEffectiveVersionAtCommonDay(t, b, winner, loser)

					// 新报价的受理时刻统一设在两版原计划共同有效的 3 月 15 日。
					setNow(corCommonDay)
					corAssertQuotesFollowWinner(t, b, winner, loser, runLabel, round)
				})
			}
		})
	}
	t.Logf("各轮实际受理成功方分布: %v（v-a 与 v-b 都允许先入库，测试只要求恰好一版）", winners)
}
