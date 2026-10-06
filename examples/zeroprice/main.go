// 命令 zeroprice 是“单价为零时怎样判断报价是否成立”的完整可运行示例：
// 登记一个零单价版本后，在生效起点以有符号 64 位整数最大数量报价，
// 得到单价、总价均为零的已确认结果；同一版本有效期内提交数量零与负数，
// 零单价不放宽数量必须为正整数的要求，得到 invalid_quantity 拒绝；
// 在版本结束时刻（不含）再提交合法正数量，得到 version_expired 拒绝。
// 这些结果的金额都是零、调用错误都为空，区分依据是 Confirmed 与 Reason，
// 而不是金额大小或 err 是否为空。
//
// 运行：
//
//	go run ./examples/zeroprice
package main

import (
	"fmt"
	"math"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述时刻均为 2026 年 UTC，版本有效期为半开区间 [freeStart, freeEnd)：
// 开始时刻包含在内，结束时刻不包含。
var (
	freeStart = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	freeEnd   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := freeStart
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记零单价版本 seat-free：单价 0 分是合法登记（只有负数单价非法），
	//    有效期 [2026-03-01, 2026-03-31)（UTC，结束时刻不含）。
	registeredEnd := freeEnd
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-free",
		UnitPrice: 0,
		Start:     freeStart,
		End:       &registeredEnd,
	}); err != nil {
		panic(err)
	}
	printViews(book, "① 登记零单价版本 seat-free（单价=0 分）后的版本视图")

	// 以下每笔报价都使用各自从未使用过的非空请求标识，输出反映的都是首次受理的判断。

	// ② 生效起点 2026-03-01 00:00（开始时刻含在有效期内），用第一个新标识
	//    提交合法正数量——取有符号 64 位整数最大值。零单价下 0×任何数量都是 0，
	//    不会因为数量很大而总价溢出；也不能把零总价理解成“数量没有被记录”。
	now = freeStart
	confirmed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zero-price-at-start",
		ItemID:    "seat",
		VersionID: "seat-free",
		Quantity:  math.MaxInt64,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("② 生效起点以最大正数量报价（zero-price-at-start）", confirmed, err)
	fmt.Printf("   Confirmed=%v 且拒绝原因为%q：这是已确认的零价报价；结果中的费率项、版本、数量=%d、受理时刻=%s 都与本次请求一致\n",
		confirmed.Confirmed, confirmed.Reason, confirmed.Request.Quantity,
		confirmed.AcceptedAt.Format(time.RFC3339))

	// ③ 有效期内（2026-03-15 12:00）换一个新标识提交数量 0。
	//    零单价不放宽“数量必须是正整数”的要求：err 为 nil，但结果未确认、
	//    原因 invalid_quantity，单价和总价仍为零，原始数量 0 原样保留。
	now = time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	zeroQty, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zero-price-zero-qty",
		ItemID:    "seat",
		VersionID: "seat-free",
		Quantity:  0,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("③ 有效期内提交数量 0（zero-price-zero-qty）", zeroQty, err)

	// ④ 负数量同样不合法：再换一个新标识提交数量 -1，仍是 invalid_quantity，
	//    拒绝结果保留提交的原始数量 -1，不会被改成零。
	negQty, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zero-price-neg-qty",
		ItemID:    "seat",
		VersionID: "seat-free",
		Quantity:  -1,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("④ 有效期内提交数量 -1（zero-price-neg-qty）", negQty, err)

	// ⑤ 结束时刻 2026-03-31 00:00（不含在有效期内），再用一个新标识提交合法正数量。
	//    零单价不会让已到期版本继续接受新报价：err 为 nil，但得到 version_expired 拒绝，
	//    单价和总价为零。
	now = freeEnd
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zero-price-at-end",
		ItemID:    "seat",
		VersionID: "seat-free",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑤ 结束时刻以合法正数量报价（zero-price-at-end）", expired, err)
}

func printViews(book *tariff.Book, title string) {
	fmt.Printf("%s：\n", title)
	views, err := book.ItemVersions("seat")
	if err != nil {
		panic(err)
	}
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 开始=%s 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice, v.Start.Format(time.RFC3339),
			fmtEnd(v.End), fmtEnd(v.EffectiveEnd), v.Replaces, v.SupersededBy)
	}
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}

// printOutcome 把一笔受理结果的五个判断要素一次展示清楚：
// 调用错误、确认状态、拒绝原因、金额（单价/总价）、请求来源与首次受理时刻。
func printOutcome(title string, o tariff.Outcome, err error) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   调用错误 err=%v\n", err)
	fmt.Printf("   请求来源 item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	fmt.Printf("   确认状态=%v 拒绝原因=%q 单价=%d 分 总价=%d 分\n",
		o.Confirmed, o.Reason, o.UnitPrice, o.Total)
}
