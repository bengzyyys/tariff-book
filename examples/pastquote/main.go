// 命令 pastquote 是“查到过去生效的版本后，现在怎样报价”的完整可运行示例：
// 同一费率项的旧版（单价 240 分，2026-07-01 生效、登记结束 2026-07-31）
// 被新版（单价 300 分，2026-07-12 起替代旧版并持续有效）截短实际有效期。
// 把报价受理时刻固定在 2026-07-18 09:00：查询 7 月 8 日得到旧版，
// 随后用一个未使用过的请求标识引用旧版、数量 5，得到 version_expired 拒绝；
// 同一受理时刻按该时刻查询得到新版，再用另一个未使用过的标识引用新版、
// 数量 5，确认单价 300 分、总价 1500 分。两次报价的受理时刻完全相同，
// 区别只在引用的版本于受理时刻是否仍在实际有效期内。
//
// 运行：
//
//	go run ./examples/pastquote
package main

import (
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC，结束时刻不含。
var (
	v1Start   = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)  // 旧版生效起点（含）
	v1RegEnd  = time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC) // 旧版登记结束（不含）
	v2Start   = time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC) // 新版替代旧版的交接点，即旧版实际有效结束（不含）
	acceptAt  = time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC) // 报价首次受理时刻（演示时钟固定在此）
	pastQuery = time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)  // 历史查询时刻：当时旧版仍生效
)

func main() {
	// 演示时钟：把受理时刻固定在 2026-07-18 09:00，全程不推进。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，
	// 无论在哪一天运行都不必等待真实时间。
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return acceptAt }))

	// ① 登记旧版 seat-v1：单价 240 分，2026-07-01 生效，登记结束 2026-07-31（不含）。
	oldEnd := v1RegEnd
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 240,
		Start:     v1Start,
		End:       &oldEnd,
	}); err != nil {
		panic(err)
	}

	// ② 登记新版 seat-v2：单价 300 分，2026-07-12 00:00 起替代旧版并持续有效。
	//    旧版的登记结束仍是 7 月 31 日，实际有效结束从这一刻起被截短到 7 月 12 日。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 300,
		Start:     v2Start,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	printViews(book, "①② 两版登记完成后的版本视图")

	// ③ 历史查询：2026-07-08 00:00 生效的是旧版。
	//    EffectiveVersionAt 使用调用方传入的查询时刻，回答账本已登记的版本
	//    在那一刻谁生效；这次查询成功不会把账本时钟拨回 7 月 8 日，
	//    也不会替随后提交的请求保留旧费率。
	old, err := book.EffectiveVersionAt("seat", pastQuery)
	if err != nil {
		panic(err)
	}
	printQuery("③ 按过去时刻查询", pastQuery, old)
	fmt.Println("   查询成功只说明旧版在 7 月 8 日生效；账本时钟仍在 2026-07-18 09:00，没有被拨回过去")
	fmt.Println()

	// ④ 用一个从未使用过的请求标识引用查到的旧版，数量 5。
	//    报价请求本身没有指定历史受理时间的参数：Quote 对这笔新请求只认
	//    账本时钟给出的首次受理时刻 2026-07-18 09:00。旧版的实际有效期
	//    已止于 7 月 12 日——虽然它登记的结束 7 月 31 日尚未到，
	//    仍得到 version_expired 拒绝；账本也不会自动改用新版确认这份请求。
	rejected, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-old-after-past-query",
		ItemID:    "seat",
		VersionID: old.VersionID,
		Quantity:  5,
	})
	printOutcome("④ 受理时刻引用查到的旧版（quote-old-after-past-query）", rejected, err)
	fmt.Println("   旧版登记结束 2026-07-31 尚未到，但实际有效结束 2026-07-12 已过；")
	fmt.Println("   结果的请求来源仍是旧版 seat-v1，账本没有自动改用新版确认")
	fmt.Println()

	// ⑤ 同一受理时刻，按该时刻查询：2026-07-18 09:00 生效的是新版。
	current, err := book.EffectiveVersionAt("seat", acceptAt)
	if err != nil {
		panic(err)
	}
	printQuery("⑤ 按受理时刻查询", acceptAt, current)
	fmt.Println()

	// ⑥ 换另一个从未使用过的标识引用新版，数量 5：
	//    新版在受理时刻有效，确认单价 300 分、总价 1500 分。
	confirmed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-new-at-acceptance",
		ItemID:    "seat",
		VersionID: current.VersionID,
		Quantity:  5,
	})
	printOutcome("⑥ 受理时刻引用新版（quote-new-at-acceptance）", confirmed, err)
}

// printViews 打印登记完成后账本保存的完整版本视图，
// 登记区间与实际有效区间并排显示，便于看出两者不是同一个边界。
func printViews(book *tariff.Book, title string) {
	fmt.Printf("%s：\n", title)
	views, err := book.ItemVersions("seat")
	if err != nil {
		panic(err)
	}
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 分 登记区间=[%s, %s) 实际有效区间=[%s, %s) 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice,
			v.Start.Format(time.RFC3339), fmtEnd(v.End),
			v.EffectiveStart.Format(time.RFC3339), fmtEnd(v.EffectiveEnd),
			v.Replaces, v.SupersededBy)
	}
	fmt.Println()
}

// printQuery 打印一次按时刻查询：查询时刻由调用方给出，
// 返回的是该时刻生效版本的视图（含实际有效结束）。
func printQuery(title string, at time.Time, v tariff.VersionView) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   查询时刻=%s → 生效版本=%s 单价=%d 分 实际有效结束=%s\n",
		at.Format(time.RFC3339), v.VersionID, v.UnitPrice, fmtEnd(v.EffectiveEnd))
}

// printOutcome 打印一笔报价的首次受理结果：请求来源、首次受理时刻、
// 调用错误与确认/拒绝结论。拒绝的调用错误为 nil，单价总价为零；
// 确认结果的拒绝原因为空。
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
		fmt.Printf("   结果=被拒绝 Confirmed=false 原因=%s 单价=%d 总价=%d（零金额不是免费确认价）\n",
			o.Reason, o.UnitPrice, o.Total)
	}
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}
