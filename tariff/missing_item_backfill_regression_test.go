package tariff

import (
	"errors"
	"testing"
	"time"
)

// 本文件为“首次报价时整个费率项都尚未登记，随后才补登费率”的使用过程补充
// 回归保障。既有的 TestIdempotentReplay 已覆盖“费率项存在、只缺被引用版本”时
// 补登缺失版本后的原样重试；这里保护的是更靠前的情况：连费率项本身都还没登记过。
//
// 固定时间线（全部为 2026 年 UTC，结束时刻不含）：
//
//	seat/v1：单价 150 分，2026-03-01 00:00 至 2026-03-31 00:00（事后才补登记）
//	首次报价受理：2026-03-15 10:00 —— 此时 seat 项尚不存在，version_not_found 拒绝
//	补登记后的受理：2026-03-16 10:00 —— 新标识、同内容报价按 150 分确认 600 分
//
// 要保护的结论：
//   - 非空请求标识的 version_not_found 拒绝是正常受理结果（调用 err 为 nil），
//     在费率尚未登记时就能按请求标识查到，也会出现在该费率项的已受理报价列表中，
//     不能因为该费率项没有版本就把这份拒绝当作从未受理过；
//   - 补登记让“按时刻查生效版本”对过去时刻的答案改变（此后能取到 v1），
//     但已保存的首次拒绝不能被修正为确认：原标识原样重试与按标识查询都保持首次值；
//   - 登记本身不生成报价；同一费率项下旧拒绝与新确认同时保留，按首次受理时刻
//     先后排列；两笔分别按自己的标识查询都与各自首次返回值一致；旧拒绝的零金额
//     始终伴随 version_not_found，不能被解释成免费确认价。
var (
	mibStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mibEnd     = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 补登记 v1 的结束时刻（不含）
	mibFirstAt = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	mibLaterAt = time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)
)

// TestRejectWholeItemMissingThenBackfillVersion 按使用顺序走完整条时间线：
// 项未登记先报价被拒 → 拒绝在补登记前即可查 → 补登记覆盖旧受理时刻的 v1 →
// 按时刻查询的答案改变但拒绝不可修正 → 推进时钟后原标识重试仍是首次拒绝、
// 新标识同内容得到确认 → 旧拒绝与新确认同时可核对。
func TestRejectWholeItemMissingThenBackfillVersion(t *testing.T) {
	// 固定时钟：受理时刻全部由测试显式拨表，不等待真实时间。
	var clock time.Time
	setNow := func(at time.Time) { clock = at }
	b := NewBook(WithClock(func() time.Time { return clock }))

	firstReq := QuoteRequest{
		RequestID: "q-before-seat-registered",
		ItemID:    "seat",
		VersionID: "v1",
		Quantity:  4,
	}

	// ① 2026-03-15 10:00：seat 项连一个版本都还没登记，用非空标识引用 seat/v1。
	setNow(mibFirstAt)
	first, err := b.Quote(firstReq)
	if err != nil {
		t.Fatalf("version_not_found 是正常受理后的拒绝，调用 err 应为空: %v", err)
	}
	if first.Confirmed || first.Reason != ReasonVersionNotFound {
		t.Fatalf("尚未登记的费率项应 version_not_found 拒绝: %+v", first)
	}
	if first.UnitPrice != 0 || first.Total != 0 {
		t.Fatalf("拒绝结果单价与总价均应为零，got 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	if first.Request != firstReq {
		t.Fatalf("拒绝结果必须保留原请求内容: got %+v, want %+v", first.Request, firstReq)
	}
	if !first.AcceptedAt.Equal(mibFirstAt) {
		t.Fatalf("拒绝结果必须保留首次受理时刻: got %v, want %v", first.AcceptedAt, mibFirstAt)
	}

	// ② 费率尚未登记时，这份拒绝就应能按请求标识查到。
	if got, err := b.Lookup(firstReq.RequestID); err != nil || got != first {
		t.Fatalf("补登记前按标识应查到首次拒绝: %v %+v vs %+v", err, got, first)
	}

	// ③ 它也应出现在 seat 项的已受理报价列表中：项没有版本不代表拒绝未受理过。
	if got, err := b.ItemOutcomes("seat"); err != nil || len(got) != 1 || got[0] != first {
		t.Fatalf("补登记前 seat 列表就应包含这笔拒绝: %v %+v", err, got)
	}

	// ④ 同一时刻按费率项查生效版本仍承认“项不存在”：查询口径与受理记录并存，
	//    查询返回 ErrItemNotFound 不能反推出那份报价没被受理。
	if _, err := b.EffectiveVersionAt("seat", mibFirstAt); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("补登记前查 3 月 15 日生效版本应 ErrItemNotFound, got %v", err)
	}

	// ⑤ 在同一本账本内给 seat 补登记 v1：单价 150 分，[2026-03-01, 2026-03-31)，
	//    有效期覆盖先前报价的受理时刻，登记应成功。
	v1End := mibEnd
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v1", UnitPrice: 150,
		Start: mibStart, End: &v1End,
	}); err != nil {
		t.Fatalf("有效期覆盖先前受理时刻的补登记应成功: %v", err)
	}

	// ⑥ 登记本身不生成报价：seat 项的已受理报价仍只有先前那一笔拒绝。
	if got, err := b.ItemOutcomes("seat"); err != nil || len(got) != 1 || got[0] != first {
		t.Fatalf("补登记不得产生报价记录: %v %+v", err, got)
	}

	// ⑦ 此后按先前的 3 月 15 日 10:00 查询生效版本，能够取得 v1。
	view, err := b.EffectiveVersionAt("seat", mibFirstAt)
	if err != nil {
		t.Fatalf("补登记后旧受理时刻应能查到 v1: %v", err)
	}
	if view.VersionID != "v1" || view.UnitPrice != 150 {
		t.Fatalf("旧受理时刻的生效版本应为 v1/150, got %s/%d", view.VersionID, view.UnitPrice)
	}
	if !view.Start.Equal(mibStart) || view.End == nil || !view.End.Equal(mibEnd) {
		t.Fatalf("v1 登记区间应为 [03-01, 03-31): 起点=%v 结束=%v", view.Start, view.End)
	}
	// 结束时刻不含：3 月 31 日 00:00 整已无生效版本，其前一纳秒仍取 v1，
	// 借此坐实补登的有效期确实覆盖 3 月 15 日 10:00 这一受理时刻。
	if atEnd, err := b.EffectiveVersionAt("seat", mibEnd); !errors.Is(err, ErrNoEffectiveVersion) {
		t.Fatalf("结束时刻不含，03-31 00:00 应 ErrNoEffectiveVersion, got %+v err=%v", atEnd, err)
	}
	beforeEnd, err := b.EffectiveVersionAt("seat", mibEnd.Add(-time.Nanosecond))
	if err != nil || beforeEnd.VersionID != "v1" {
		t.Fatalf("结束前一纳秒应仍取 v1: %+v err=%v", beforeEnd, err)
	}

	// ⑧ 但“按时刻查询的答案改变”不表示当时的拒绝可以被修正为确认：
	//    补登记后按原标识查询，取回的仍是 3 月 15 日那份首次拒绝。
	if got, err := b.Lookup(firstReq.RequestID); err != nil || got != first {
		t.Fatalf("补登记不得把旧拒绝修正成确认: %v %+v vs %+v", err, got, first)
	}

	// ⑨ 受理时刻推进到 3 月 16 日 10:00，原标识、原内容再次提交：
	//    原样重试只返回首次结果，连受理时刻都不更新。
	setNow(mibLaterAt)
	replay, err := b.Quote(firstReq)
	if err != nil {
		t.Fatalf("原标识原样重试不应报错: %v", err)
	}
	if replay != first {
		t.Fatalf("原样重试必须返回首次拒绝: got %+v, want %+v", replay, first)
	}
	if got, err := b.Lookup(firstReq.RequestID); err != nil || got != first {
		t.Fatalf("重试后按标识查询仍应为首次拒绝: %v %+v vs %+v", err, got, first)
	}

	// ⑩ 仅换一个从未使用过的请求标识，仍引用 seat/v1、数量 4：
	//    此刻 v1 已生效，应按 150 分确认总价 600 分。
	secondReq := QuoteRequest{
		RequestID: "q-after-seat-registered",
		ItemID:    "seat",
		VersionID: "v1",
		Quantity:  4,
	}
	second, err := b.Quote(secondReq)
	if err != nil {
		t.Fatalf("新标识报价应正常受理: %v", err)
	}
	if !second.Confirmed || second.Reason != ReasonNone {
		t.Fatalf("补登记后的新标识报价应确认且无拒绝原因: %+v", second)
	}
	if second.UnitPrice != 150 || second.Total != 600 {
		t.Fatalf("新确认应为单价 150、总价 600, got 单价=%d 总价=%d", second.UnitPrice, second.Total)
	}
	if second.Request != secondReq {
		t.Fatalf("新确认必须保留其请求来源: got %+v, want %+v", second.Request, secondReq)
	}
	if !second.AcceptedAt.Equal(mibLaterAt) {
		t.Fatalf("新确认的受理时刻应为 3 月 16 日 10:00: got %v, want %v",
			second.AcceptedAt, mibLaterAt)
	}

	// ⑪ 按 seat 查看时，旧拒绝与新确认必须同时保留，按首次受理时刻先后排列。
	//    map 遍历顺序随机：重复列取多轮，每轮数量与顺序都必须一致。
	wantIDs := []string{firstReq.RequestID, secondReq.RequestID}
	var listed []Outcome
	for round := 0; round < 30; round++ {
		got, err := b.ItemOutcomes("seat")
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if len(got) != 2 {
			t.Fatalf("round %d: 旧拒绝与新确认应同时保留为 2 条, got %d: %+v",
				round, len(got), got)
		}
		if ids := requestIDs(got); !equalStrings(ids, wantIDs) {
			t.Fatalf("round %d: 应按首次受理时刻列为旧拒绝、新确认, want %v, got %v",
				round, wantIDs, ids)
		}
		if !got[0].AcceptedAt.Before(got[1].AcceptedAt) {
			t.Fatalf("round %d: 顺序不满足受理时刻从早到晚: %v !< %v",
				round, got[0].AcceptedAt, got[1].AcceptedAt)
		}
		if round == 0 {
			listed = got
		}
	}

	// 较早一条：3 月 15 日的首次拒绝，原因、零金额、请求来源、受理时刻全部保持原值。
	oldRejection := listed[0]
	if oldRejection != first {
		t.Fatalf("列表中的旧拒绝应与首次返回值完全一致: got %+v, want %+v", oldRejection, first)
	}
	if oldRejection.Confirmed || oldRejection.Reason != ReasonVersionNotFound {
		t.Fatalf("旧拒绝仍应是 version_not_found 拒绝: %+v", oldRejection)
	}
	if oldRejection.UnitPrice != 0 || oldRejection.Total != 0 {
		t.Fatalf("旧拒绝的零金额不能被解释成免费确认价: %+v", oldRejection)
	}
	if oldRejection.Request != firstReq || !oldRejection.AcceptedAt.Equal(mibFirstAt) {
		t.Fatalf("旧拒绝的请求来源与 3 月 15 日受理时刻必须保留: %+v", oldRejection)
	}

	// 较晚一条：3 月 16 日的新确认，与它自己的首次返回值一致。
	newConfirmation := listed[1]
	if newConfirmation != second {
		t.Fatalf("列表中的新确认应与首次返回值完全一致: got %+v, want %+v",
			newConfirmation, second)
	}

	// ⑫ 两笔分别按自己的标识查询，结果与各自首次返回值一致；
	//    原样重试（任意一笔）都不增加记录，登记也没有生成过记录。
	if got, err := b.Lookup(firstReq.RequestID); err != nil || got != first {
		t.Fatalf("旧拒绝按标识查询应与首次值一致: %v %+v vs %+v", err, got, first)
	}
	if got, err := b.Lookup(secondReq.RequestID); err != nil || got != second {
		t.Fatalf("新确认按标识查询应与首次值一致: %v %+v vs %+v", err, got, second)
	}
	if _, err := b.Quote(firstReq); err != nil {
		t.Fatalf("旧标识再次原样重试不应报错: %v", err)
	}
	if _, err := b.Quote(secondReq); err != nil {
		t.Fatalf("新标识再次原样重试不应报错: %v", err)
	}
	got, err := b.ItemOutcomes("seat")
	if err != nil || len(got) != 2 {
		t.Fatalf("原样重试不得增加记录，仍应为 2 条: %v %+v", err, got)
	}
}
