// 命令 zeroprice 是“总价为零时怎样判断报价是否成立”的完整可运行示例：
// 同一个零单价版本 seat-v1（单价 0 分）在生效起点以有符号 64 位整数最大值
// 为数量报价，得到已确认、单价和总价均为零的结果；有效期内换标识提交数量零，
// 得到 invalid_quantity 拒绝；结束时刻再换标识提交合法正数量，得到
// version_expired 拒绝。三笔结果的单价和总价都是零、调用错误都是 nil，
// 只有 Confirmed 与 Reason 能区分“已确认的零价报价”和“没有可用金额的拒绝”。
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

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // 零价版生效起点（含）
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 零价版结束时刻（不含）
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记零单价版本 seat-v1：单价为零是合法登记（单价可以为零、不能为负），
	//    2026-03-01 00:00 起生效（含），2026-03-31 00:00 结束（不含）。
	v1RegisteredEnd := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 0,
		Start:     v1Start,
		End:       &v1RegisteredEnd,
	}); err != nil {
		panic(err)
	}
	printViews(book, "① 零单价版本登记完成后的版本视图")

	// 以下每笔报价都使用各自从未使用过的非空请求标识，
	// 因此输出反映的都是首次受理的判断。

	// ② 生效起点（2026-03-01 00:00，开始时刻含在有效期内）提交数量为
	//    math.MaxInt64 的报价：单价为零时 0×MaxInt64 仍是零，不会溢出，
	//    得到已确认结果——单价、总价均为零，拒绝原因为空，
	//    结果中的费率项、版本、数量和受理时刻与本次请求一致。
	now = v1Start
	confirmed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zeroprice-at-start",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  math.MaxInt64,
	})
	printOutcome("② 生效起点提交最大数量（zeroprice-at-start）", confirmed, err)

	// ③ 有效期内（2026-03-15 00:00）换一个新标识提交数量零：
	//    零单价不放宽数量必须为正整数的要求（负数量同样不合法），
	//    得到 invalid_quantity 拒绝，单价、总价仍为零，
	//    提交的原始数量 0 原样保留在结果中。
	now = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	zeroQty, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zeroprice-quantity-zero",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  0,
	})
	printOutcome("③ 有效期内提交数量零（zeroprice-quantity-zero）", zeroQty, err)

	// ④ 结束时刻（2026-03-31 00:00，结束时刻不含在有效期内）再换一个新标识
	//    提交合法正数量：版本已到期，得到 version_expired 拒绝——
	//    零单价不会让已到期版本继续接受新报价。
	now = v1End
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zeroprice-at-end",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  1,
	})
	printOutcome("④ 结束时刻提交合法数量（zeroprice-at-end）", expired, err)
}

func printViews(book *tariff.Book, title string) {
	fmt.Printf("%s：\n", title)
	views, err := book.ItemVersions("seat")
	if err != nil {
		panic(err)
	}
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice, fmtEnd(v.End), fmtEnd(v.EffectiveEnd),
			v.Replaces, v.SupersededBy)
	}
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}

func printOutcome(title string, o tariff.Outcome, err error) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求 item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	fmt.Printf("   调用错误 err=%v\n", err)
	if o.Confirmed {
		fmt.Printf("   结果=已确认 Confirmed=true 原因=%q 单价=%d 分 总价=%d 分\n",
			string(o.Reason), o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 Confirmed=false 原因=%s 单价=%d 总价=%d\n",
			o.Reason, o.UnitPrice, o.Total)
	}
}
