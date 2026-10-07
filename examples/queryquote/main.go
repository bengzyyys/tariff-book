// 命令 queryquote 演示“查到过去生效的版本后，现在怎样报价”。
//
// EffectiveVersionAt 使用调用方传入的查询时刻，回答账本已登记的版本在那一刻
// 谁生效；Quote 对新请求使用账本的首次受理时刻（账本时钟）判断版本是否可用，
// 报价请求本身没有指定历史受理时间的参数。一次历史查询成功，既不会把账本时钟
// 拨回过去，也不会替随后提交的请求保留旧费率。
//
// 固定时间线（同一费率项 seat，均为 2026 年 UTC，结束时刻不含）：
//
//	旧版 seat-v1：单价 240 分，2026-07-01 生效，登记结束为 2026-07-31
//	新版 seat-v2：单价 300 分，2026-07-12 00:00 起替代旧版并持续有效
//	历史查询时刻：2026-07-08（旧版在那一刻生效）
//	报价首次受理时刻：2026-07-18 09:00（旧版实际有效期已止于 07-12）
//
// 先在 7 月 8 日查到旧版，随后（受理时刻 7 月 18 日）用新标识引用该旧版、
// 数量 5，得到 version_expired 拒绝——尽管旧版登记结束 7 月 31 日尚未到，
// 它的实际有效期已止于 7 月 12 日，账本也不会自动改用新版确认；
// 再按受理时刻查询得到新版，用另一个新标识引用新版、数量 5，
// 才确认单价 300 分、总价 1500 分。
//
// 运行：
//
//	go run ./examples/queryquote
package main

import (
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述时刻均为 2026 年 UTC；版本结束时刻不含。
var (
	oldStart  = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)  // 旧版生效起点（含）
	oldRegEnd = time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC) // 旧版登记结束（不含）
	handoff   = time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC) // 新版零点起替代旧版，也是旧版实际结束（不含）
	pastQuery = time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)  // 历史查询时刻：旧版在那一刻生效
	acceptAt  = time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC) // 两笔报价的首次受理时刻
)

func main() {
	// 演示时钟：受理时刻取自变量 now，全程固定在 2026-07-18 09:00。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := acceptAt
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记旧版 seat-v1：单价 240 分，2026-07-01 生效，登记结束 2026-07-31（不含）。
	oldEnd := oldRegEnd
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 240,
		Start:     oldStart,
		End:       &oldEnd,
	}); err != nil {
		panic(err)
	}

	// ② 登记新版 seat-v2：单价 300 分，2026-07-12 00:00 起替代旧版、持续有效。
	//    旧版登记结束仍是 7 月 31 日，但实际有效结束从交接时刻起被截短到 7 月 12 日。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 300,
		Start:     handoff,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	printViews(book)

	// ③ 历史查询：调用方显式传入 2026-07-08。EffectiveVersionAt 回答的是
	//    “账本已登记的版本在查询那一刻谁生效”，那一刻旧版尚未被替代，返回 seat-v1。
	old, err := book.EffectiveVersionAt("seat", pastQuery)
	if err != nil {
		panic(err)
	}
	printView("③ 按调用方给出的历史时刻查询", pastQuery, old)
	fmt.Printf("   这次查询之后，账本时钟（新请求的首次受理时刻来源）仍是 %s——历史查询不拨钟、不留时间状态\n\n",
		now.Format(time.RFC3339))

	// ④ 用一个从未使用过的请求标识，引用③查到的旧版 seat-v1，数量 5。
	//    QuoteRequest 只有请求标识、费率项、版本、数量，没有“历史受理时间”参数；
	//    版本是否可用按账本的首次受理时刻 2026-07-18 09:00 判断。
	//    旧版实际有效期止于 7 月 12 日（尽管登记结束 7 月 31 日尚未到），
	//    因此这是一次正常受理但被拒绝：err 为 nil、Confirmed 为 false、
	//    原因 version_expired、单价和总价为零；账本不会自动改用新版确认这份请求。
	oldReq := tariff.QuoteRequest{
		RequestID: "quote-found-old-version",
		ItemID:    "seat",
		VersionID: old.VersionID,
		Quantity:  5,
	}
	rejected, err := book.Quote(oldReq)
	if err != nil {
		panic(err)
	}
	printOutcome("④ 受理时刻引用历史查询得到的旧版（quote-found-old-version）", rejected, err)

	// ⑤ 在同一受理时刻，按“现在”（受理时刻本身）查询：这一刻生效的是新版 seat-v2。
	current, err := book.EffectiveVersionAt("seat", acceptAt)
	if err != nil {
		panic(err)
	}
	printView("⑤ 按报价受理时刻查询", acceptAt, current)

	// ⑥ 再用另一个从未使用过的请求标识引用查到的新版，数量 5：
	//    新版在首次受理时刻有效，确认单价 300 分、总价 300×5=1500 分，
	//    确认结果的拒绝原因为空。
	newReq := tariff.QuoteRequest{
		RequestID: "quote-current-version",
		ItemID:    "seat",
		VersionID: current.VersionID,
		Quantity:  5,
	}
	confirmed, err := book.Quote(newReq)
	if err != nil {
		panic(err)
	}
	printOutcome("⑥ 受理时刻引用当前生效的新版（quote-current-version）", confirmed, err)

	// ⑦ 并排对照：两次报价的首次受理时刻相同，差别只在“所引用版本在该时刻是否可用”。
	fmt.Println("⑦ 两笔报价并排对照：")
	fmt.Printf("   历史查询时刻=%s 只决定③选中 %s；它不进入任何一笔报价\n",
		pastQuery.Format(time.RFC3339), old.VersionID)
	fmt.Printf("   两笔报价的首次受理时刻都是=%s（账本时钟，报价请求无历史受理时间参数）\n",
		acceptAt.Format(time.RFC3339))
	fmt.Printf("   旧版 %s 登记结束=%s 尚未到，但实际有效结束=%s 已过 → %s（err=%v，Confirmed=%v，零金额不是免费确认价）\n",
		old.VersionID, fmtEnd(old.End), fmtEnd(old.EffectiveEnd), rejected.Reason, nil, rejected.Confirmed)
	fmt.Printf("   新版 %s 在受理时刻持续有效 → 已确认 单价=%d 分 总价=%d 分 拒绝原因=%q\n",
		current.VersionID, confirmed.UnitPrice, confirmed.Total, string(confirmed.Reason))
}

// printViews 打印登记完成后账本保存的完整版本视图，
// 让读者在报价前先看到旧版的登记结束（7 月 31 日）与实际有效结束（7 月 12 日）并不相同。
func printViews(book *tariff.Book) {
	fmt.Println("①② 两版登记完成后的版本视图：")
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

// printView 打印一次 EffectiveVersionAt 查询的查询时刻与选中版本，
// 同时列出登记结束和实际有效结束，方便对照“查到的是哪一版、它实际上有效到何时”。
func printView(title string, at time.Time, v tariff.VersionView) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   查询时刻=%s；选中版本=%s 单价=%d 分\n",
		at.Format(time.RFC3339), v.VersionID, v.UnitPrice)
	fmt.Printf("   登记结束=%s；实际有效结束=%s 替代=%q 被替代=%q\n\n",
		fmtEnd(v.End), fmtEnd(v.EffectiveEnd), v.Replaces, v.SupersededBy)
}

func printOutcome(title string, o tariff.Outcome, err error) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求标识=%s item=%s version=%s 数量=%d（请求中没有任何历史受理时间参数）\n",
		o.Request.RequestID, o.Request.ItemID, o.Request.VersionID, o.Request.Quantity)
	fmt.Printf("   首次受理时刻=%s；调用错误 err=%v\n", o.AcceptedAt.Format(time.RFC3339), err)
	if o.Confirmed {
		fmt.Printf("   结果=已确认 Confirmed=true 拒绝原因=%q 单价=%d 分 总价=%d 分\n\n",
			string(o.Reason), o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 Confirmed=false 拒绝原因=%s 单价=%d 总价=%d（零金额不是免费确认价）\n\n",
			o.Reason, o.UnitPrice, o.Total)
	}
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}
