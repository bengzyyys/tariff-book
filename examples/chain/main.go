// 命令 chain 是“连续调整费率时如何选对被替代版本”的完整可运行示例：
// 同一费率项已完成甲→乙一次替代后，登记丙版时误把被替代版本填成甲版，
// 被 ErrInvalidReplacement 拒绝；沿用丙版标识改为替代乙版后成功。
//
// 运行：
//
//	go run ./examples/chain
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // 甲版生效
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 甲版登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 乙版替代甲版的交接点
	v2End   = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC) // 乙版登记结束（不含）
	v3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // 丙版计划的交接点
	v3End   = time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC) // 丙版登记结束（不含）
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记甲版 seat-v1：单价 150 分，2026-03-01 生效，登记结束 2026-03-31（不含）。
	v1RegisteredEnd := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     v1Start,
		End:       &v1RegisteredEnd,
	}); err != nil {
		panic(err)
	}

	// ② 登记乙版 seat-v2：单价 180 分，2026-03-10 起替代甲版，登记结束 2026-03-25（不含）。
	//    从这一刻起甲版的实际有效期被截短为 [03-01, 03-10)，
	//    但它登记时填写的结束 3 月 31 日仍然保留。
	v2RegisteredEnd := v2End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2RegisteredEnd,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}

	printViews(book, "①② 甲、乙两版登记完成后的版本视图")

	// ③ 准备登记丙版 seat-v3：单价 200 分，2026-03-20 开始、2026-03-24 结束。
	//    误把被替代版本填成甲版：甲版的登记结束（3 月 31 日）虽然尚未到，
	//    但它的实际有效期已经在 3 月 10 日被乙版截短，
	//    3 月 20 日不在甲版当前的实际有效期 [03-01, 03-10) 内，
	//    不能因为登记结束仍是 3 月 31 日就把 3 月 20 日当作可用的替代时刻，
	//    因此返回 ErrInvalidReplacement。
	v3RegisteredEnd := v3End
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
		End:       &v3RegisteredEnd,
		Replaces:  "seat-v1",
	})
	fmt.Printf("③ 登记 seat-v3（误填替代甲版）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v；",
		errors.Is(err, tariff.ErrInvalidReplacement))
	fmt.Println("登记失败是调用本身返回错误，整次登记未生效")

	// ④ 失败后查询：仍只有甲、乙两版；甲版的实际结束仍是 3 月 10 日、
	//    甲乙之间的替代关系不变；乙版也没有被这次失败的登记截短。
	printViews(book, "④ 登记失败后的版本视图（应与①②完全一致）")

	// ⑤ 沿用同一个版本标识 seat-v3，只把被替代版本改为乙版后再次登记。
	//    失败的登记不占用版本标识；3 月 20 日晚于乙版开始、
	//    且落在乙版当前的实际有效期 [03-10, 03-25) 内，登记成功。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
		End:       &v3RegisteredEnd,
		Replaces:  "seat-v2",
	}); err != nil {
		panic(err)
	}
	fmt.Println("⑤ 沿用标识 seat-v3、改为替代乙版后再次登记：err=<nil>，登记生效")

	// ⑥ 成功后的查询：乙版既替代甲版又被丙版替代，实际结束变为 3 月 20 日，
	//    而它登记时填写的 3 月 25 日仍然保留；甲版的第一次交接关系也继续保留。
	printViews(book, "⑥ 登记成功后的版本视图")

	// ⑦⑧ 把报价受理时刻推进到第二次交接点 2026-03-20 00:00（结束时刻不含该点），
	//     对比两类“没成功”：③的登记失败是调用返回错误，
	//     而引用已失效版本的报价是正常受理后返回的拒绝结果（err 为 nil）。
	now = v3Start

	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑦ 第二次交接点用新标识引用乙版（quote-v2-at-handoff）", expired)
	fmt.Printf("   调用 err==nil，但 Confirmed=%v、原因=%s：受理后的拒绝，与③的调用错误含义不同\n",
		expired.Confirmed, expired.Reason)

	confirmed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v3-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v3",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑧ 第二次交接点用新标识引用丙版（quote-v3-at-handoff）", confirmed)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 4 = 总价 %d 分\n",
		confirmed.Confirmed, confirmed.UnitPrice, confirmed.Total)
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

func printOutcome(title string, o tariff.Outcome) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求 item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	if o.Confirmed {
		fmt.Printf("   结果=已确认 单价=%d 分 总价=%d 分\n", o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 原因=%s（err 为 nil，拒绝不是调用错误）\n", o.Reason)
	}
}
