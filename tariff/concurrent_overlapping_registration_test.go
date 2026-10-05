package tariff

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// 同一本尚未登记该费率项的账本，同时收到两份有效期重叠的登记时，
// 必须仍然只有一个版本入库——本文件保护这一现有规则在“同时发生”
// （并发）提交下成立，而不是先成功一版、再顺序提交另一版的情形。
//
// 固定时间线（全部为 UTC 零点，开始时刻含、结束时刻不含，两份登记
// 都不填写替代来源，其余内容合法）：
//
//	v-a：单价 150 分，2026-03-01 起，2026-03-20 结束（不含）
//	v-b：单价 180 分，2026-03-10 起，2026-03-31 结束（不含）
//
// 两版原计划共同有效的区间为 [2026-03-10, 2026-03-20)，
// 取其中的 2026-03-15 作为查询与报价受理时刻。
//
// 哪一版成功由实际受理顺序决定，测试不要求某个版本固定获胜，
// 但只接受两种对称结局之一：恰好一次成功、另一次返回可用 errors.Is
// 识别的 ErrOverlap；两份登记各自成功不是允许的结果。不同版本标识
// 不能绕过“同一项有效期不得重叠”的规则，失败方也不能覆盖成功方
// 已入库的登记信息。
//
// 所有时刻在代码中写死并通过 WithClock 注入固定时钟，并特意把“实际
// 运行日期”分别设为远早于、正落在、远晚于有效期的若干日期：登记结论
// 只取决于请求自带的有效期，报价受理时刻则固定拨到 3 月 15 日，
// 不依赖等待真实时间经过某个生效点，不同运行日期下业务结论一致。
const (
	coItemID   = "seat"
	coVersionA = "v-a"
	coVersionB = "v-b"
	coQuantity = int64(4)
	coRounds   = 12 // 每个运行日期下的独立账本轮数，提高两种受理顺序都被观测到的机会
	coPriceA   = int64(150)
	coPriceB   = int64(180)
)

var (
	coStartA     = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	coEndA       = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // 不含
	coStartB     = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	coEndB       = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 不含
	coOverlapDay = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // 两版原计划共同有效的一天
)

// coRunDates 是注入给账本的“实际运行日期”采样：远早于有效期、
// 落在共同有效期内、远晚于两版结束。结论不应随它变化。
var coRunDates = []struct {
	name string
	at   time.Time
}{
	{"运行日远早于生效期", time.Date(2026, 1, 5, 8, 30, 0, 0, time.UTC)},
	{"运行日落于两版共同有效期", time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)},
	{"运行日远晚于两版结束", time.Date(2028, 7, 9, 23, 59, 0, 0, time.UTC)},
}

// coSpec 描述参与同时竞争的一份登记。单价同时用于校验成功方报价。
type coSpec struct {
	versionID string
	unitPrice int64
	start     time.Time
	end       time.Time
}

func coSpecA() coSpec {
	return coSpec{versionID: coVersionA, unitPrice: coPriceA, start: coStartA, end: coEndA}
}

func coSpecB() coSpec {
	return coSpec{versionID: coVersionB, unitPrice: coPriceB, start: coStartB, end: coEndB}
}

// request 按本规格生成一次登记请求。结束时刻每次都取独立副本，
// 避免并发 goroutine 之间或与账本之间共享同一个指针。
func (s coSpec) request() RegisterRequest {
	end := s.end
	return RegisterRequest{
		ItemID:    coItemID,
		VersionID: s.versionID,
		UnitPrice: s.unitPrice,
		Start:     s.start,
		End:       &end,
	}
}

// registerConcurrently 经同一屏障同时放行两份登记，返回各自拿到的真实错误。
// 两份请求都已构造完毕、账本此前未登记该费率项，模拟真正的同时受理。
func registerConcurrently(b *Book, reqA, reqB RegisterRequest) (error, error) {
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, req := range []RegisterRequest{reqA, reqB} {
		wg.Add(1)
		go func(i int, req RegisterRequest) {
			defer wg.Done()
			<-start // 双方就绪后一起进入 RegisterVersion，制造同时竞争
			errs[i] = b.RegisterVersion(req)
		}(i, req)
	}
	close(start)
	wg.Wait()
	return errs[0], errs[1]
}

// assertExactlyOneRegistrationSucceeds 校验并发批次恰好一次成功、
// 一次返回 ErrOverlap，返回实际成功方与失败方规格（不预设谁获胜）。
func assertExactlyOneRegistrationSucceeds(t *testing.T, errA, errB error) (coSpec, coSpec) {
	t.Helper()
	specA, specB := coSpecA(), coSpecB()

	var winner, loser coSpec
	successes := 0
	if errA == nil {
		successes++
		winner, loser = specA, specB
	} else if !errors.Is(errA, ErrOverlap) {
		t.Fatalf("v-a 登记失败只允许 ErrOverlap，got %v", errA)
	}
	if errB == nil {
		successes++
		winner, loser = specB, specA
	} else if !errors.Is(errB, ErrOverlap) {
		t.Fatalf("v-b 登记失败只允许 ErrOverlap，got %v", errB)
	}
	if successes != 1 {
		t.Fatalf("同时登记应恰好一次成功、一次 ErrOverlap（成功次数=%d）：errA=%v errB=%v",
			successes, errA, errB)
	}
	return winner, loser
}

// assertLedgerMatchesOnlyWinner 校验操作结束后账本里只有实际成功方：
// 单价、登记起止与实际有效区间与成功请求一致，两种替代关系均为空，
// 失败方的任何字段（版本标识、单价、边界）都不得混入。
// 同时校验 3 月 15 日 EffectiveVersionAt 必须选中成功方。
func assertLedgerMatchesOnlyWinner(t *testing.T, b *Book, winner, loser coSpec) {
	t.Helper()

	views, err := b.ItemVersions(coItemID)
	if err != nil {
		t.Fatalf("成功登记后查询费率项: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("同时竞争后应只有一个版本入库，got %d: %+v", len(views), views)
	}
	v := views[0]

	// 入库版本必须逐项等于成功方的登记内容；两种替代关系都为空。
	if v.ItemID != coItemID {
		t.Fatalf("入库版本费率项=%q，want %q", v.ItemID, coItemID)
	}
	if v.VersionID != winner.versionID {
		t.Fatalf("入库版本标识=%q，应为实际成功方 %q", v.VersionID, winner.versionID)
	}
	if v.UnitPrice != winner.unitPrice {
		t.Fatalf("入库版本单价=%d，应为成功方 %d（失败方单价 %d 不得混入）",
			v.UnitPrice, winner.unitPrice, loser.unitPrice)
	}
	if !v.Start.Equal(winner.start) || !v.EffectiveStart.Equal(winner.start) {
		t.Fatalf("入库版本开始/实际开始异常: 登记=%v 实际=%v，want %v",
			v.Start, v.EffectiveStart, winner.start)
	}
	if v.End == nil || !v.End.Equal(winner.end) {
		t.Fatalf("入库版本登记结束=%v，want %v", v.End, winner.end)
	}
	if v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(winner.end) {
		t.Fatalf("入库版本实际有效结束=%v，want %v（未填替代来源，实际区间即登记区间）",
			v.EffectiveEnd, winner.end)
	}
	if v.Replaces != "" || v.SupersededBy != "" {
		t.Fatalf("入库版本两种替代关系都应为空: Replaces=%q SupersededBy=%q",
			v.Replaces, v.SupersededBy)
	}
	// 显式点一遍边界：失败方的版本标识与时间边界都不能出现。
	if v.VersionID == loser.versionID {
		t.Fatalf("失败方 %s 的标识混入了查询结果", loser.versionID)
	}
	if v.Start.Equal(loser.start) || (v.End != nil && v.End.Equal(loser.end)) {
		t.Fatalf("失败方 %s 的时间边界混入了入库版本: %+v", loser.versionID, v)
	}

	// 两版原计划共同有效的 3 月 15 日，查询必须选中实际成功方这一版。
	at315, err := b.EffectiveVersionAt(coItemID, coOverlapDay)
	if err != nil {
		t.Fatalf("3 月 15 日应能选到实际成功的版本: %v", err)
	}
	if at315.VersionID != winner.versionID || at315.UnitPrice != winner.unitPrice {
		t.Fatalf("3 月 15 日选中 %s（%d 分），应为实际成功方 %s（%d 分）",
			at315.VersionID, at315.UnitPrice, winner.versionID, winner.unitPrice)
	}
	if !at315.Start.Equal(winner.start) ||
		at315.EffectiveEnd == nil || !at315.EffectiveEnd.Equal(winner.end) {
		t.Fatalf("3 月 15 日选中版本的有效区间与成功方登记不一致: %+v", at315)
	}
}

// TestConcurrentOverlappingRegistrationsExactlyOnePersists 保护核心规则：
// 同一本空账本同时收到 v-a、v-b 两份有效期重叠的登记时，恰好一次成功、
// 另一次 errors.Is(ErrOverlap)；不同版本标识不能绕过同项不重叠规则。
//
// 测试不固定获胜方（v-a、v-b 先被受理都允许），但两种结局都做同样完整
// 的账本校验：只有一版入库，字段全部属于成功方，3 月 15 日选到它。
// 多个运行日期下结论一致；多轮独立账本重复执行，覆盖两种调度顺序。
func TestConcurrentOverlappingRegistrationsExactlyOnePersists(t *testing.T) {
	for _, runDate := range coRunDates {
		t.Run(runDate.name, func(t *testing.T) {
			winners := map[string]int{}
			for round := 0; round < coRounds; round++ {
				t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
					now, _ := fixedClock(runDate.at)
					b := NewBook(WithClock(now))

					errA, errB := registerConcurrently(b, coSpecA().request(), coSpecB().request())
					winner, loser := assertExactlyOneRegistrationSucceeds(t, errA, errB)
					winners[winner.versionID]++

					assertLedgerMatchesOnlyWinner(t, b, winner, loser)
				})
			}
			t.Logf("运行日 %v 各轮成功方分布: %v（v-a、v-b 谁先被受理都允许，不要求固定一方获胜）",
				runDate.at.Format("2006-01-02"), winners)
		})
	}
}

// TestQuotesAfterConcurrentOverlappingRegistration 在并发登记结束后，
// 把新报价的受理时刻固定拨到两版原计划共同有效的 3 月 15 日，
// 分别用未使用过的请求标识、数量 4 引用两个版本：
//
//   - 引用实际成功方：按其单价确认——v-a 为 150×4=600 分，
//     v-b 为 180×4=720 分——并保留对应的版本来源；
//   - 引用实际失败方：正常受理但拒绝，原因 version_not_found，
//     单价与总价均为零，Quote 调用本身不返回错误，且保留原请求来源。
//
// 查询与报价的结论必须对应本次实际登记成功的一方，不能引用失败方
// 得到确认价。多个运行日期下业务结论一致。
func TestQuotesAfterConcurrentOverlappingRegistration(t *testing.T) {
	for _, runDate := range coRunDates {
		t.Run(runDate.name, func(t *testing.T) {
			dateKey := runDate.at.Format("20060102")
			winners := map[string]int{}
			for round := 0; round < coRounds; round++ {
				t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
					now, setNow := fixedClock(runDate.at)
					b := NewBook(WithClock(now))

					errA, errB := registerConcurrently(b, coSpecA().request(), coSpecB().request())
					winner, loser := assertExactlyOneRegistrationSucceeds(t, errA, errB)
					winners[winner.versionID]++
					assertLedgerMatchesOnlyWinner(t, b, winner, loser)

					// 新报价的受理时刻固定在两版原计划共同有效的 3 月 15 日，
					// 不依赖真实时间流逝。
					setNow(coOverlapDay)

					// 引用实际成功方：按其单价确认，金额只可能是 600 或 720 分。
					winReqID := fmt.Sprintf("co-quote-%s-win-%d", dateKey, round)
					winReq := QuoteRequest{
						RequestID: winReqID, ItemID: coItemID,
						VersionID: winner.versionID, Quantity: coQuantity,
					}
					winOut, err := b.Quote(winReq)
					if err != nil {
						t.Fatalf("引用成功方 %s 是正常受理，不应返回错误: %v", winner.versionID, err)
					}
					if !winOut.Confirmed {
						t.Fatalf("引用成功方 %s 应确认: %+v", winner.versionID, winOut)
					}
					if winOut.Reason != ReasonNone {
						t.Fatalf("成功方确认结果不应带拒绝原因: %q", winOut.Reason)
					}
					if winOut.UnitPrice != winner.unitPrice {
						t.Fatalf("成功方报价单价=%d，want %d", winOut.UnitPrice, winner.unitPrice)
					}
					wantTotal := winner.unitPrice * coQuantity // 150×4=600 或 180×4=720
					if winOut.Total != wantTotal {
						t.Fatalf("成功方 %s 报价总价=%d，want %d", winner.versionID, winOut.Total, wantTotal)
					}
					if winOut.Request != winReq {
						t.Fatalf("成功方报价未保留对应版本来源: %+v vs %+v", winOut.Request, winReq)
					}
					if !winOut.AcceptedAt.Equal(coOverlapDay) {
						t.Fatalf("成功方报价受理时刻=%v，want %v", winOut.AcceptedAt, coOverlapDay)
					}

					// 引用实际失败方：正常受理（无错误）但以 version_not_found 拒绝，
					// 单价、总价均为零；它本来就没入库，不能在两版共同有效期内被确认。
					loseReqID := fmt.Sprintf("co-quote-%s-lose-%d", dateKey, round)
					loseReq := QuoteRequest{
						RequestID: loseReqID, ItemID: coItemID,
						VersionID: loser.versionID, Quantity: coQuantity,
					}
					loseOut, err := b.Quote(loseReq)
					if err != nil {
						t.Fatalf("失败方 %s 未入库的报价应正常受理并返回拒绝，不应有错误: %v",
							loser.versionID, err)
					}
					if loseOut.Confirmed {
						t.Fatalf("引用未入库的失败方 %s 不应确认: %+v", loser.versionID, loseOut)
					}
					if loseOut.Reason != ReasonVersionNotFound {
						t.Fatalf("失败方 %s 拒绝原因=%q，want %q",
							loser.versionID, loseOut.Reason, ReasonVersionNotFound)
					}
					if loseOut.UnitPrice != 0 || loseOut.Total != 0 {
						t.Fatalf("version_not_found 拒绝的单价/总价都应为零: %d/%d",
							loseOut.UnitPrice, loseOut.Total)
					}
					if loseOut.Request != loseReq {
						t.Fatalf("失败方拒绝结果也必须保留原请求来源: %+v vs %+v",
							loseOut.Request, loseReq)
					}
					if !loseOut.AcceptedAt.Equal(coOverlapDay) {
						t.Fatalf("失败方报价受理时刻=%v，want %v", loseOut.AcceptedAt, coOverlapDay)
					}

					// 两份首次受理结果分别按各自标识保存，结论与本次实际成功方对应；
					// 报价不改变登记状态，账本里仍只有成功方一版。
					storedWin, err := b.Lookup(winReqID)
					if err != nil || storedWin != winOut {
						t.Fatalf("成功方受理结果未按标识保存: %+v (%v)", storedWin, err)
					}
					storedLose, err := b.Lookup(loseReqID)
					if err != nil || storedLose != loseOut {
						t.Fatalf("失败方拒绝结果未按标识保存: %+v (%v)", storedLose, err)
					}
					assertLedgerMatchesOnlyWinner(t, b, winner, loser)
				})
			}
			t.Logf("运行日 %v 各轮成功方分布: %v（报价结论必须随实际成功方而定）",
				runDate.at.Format("2006-01-02"), winners)
		})
	}
}
