// 命令 overflow 演示怎样区分“指定版本在受理时刻不能使用”和
// “版本有效但单价×数量超出有符号 64 位整数上限（total_overflow）”。
//
// 同一费率项 seat 的两版单价都登记为 math.MaxInt64 分（有符号 64 位整数最大值），
// 报价数量均为 2，数学总价 2×MaxInt64 无法用 int64 表示：
//
//	旧版 seat-v1：2026-03-01 00:00 起生效，登记结束为 2026-03-31 00:00（不含）
//	新版 seat-v2：2026-03-10 00:00 起替代旧版，不填结束时间、持续有效
//
// 账本对合法正整数数量先判版本在首次受理时刻是否有效，只有有效版本才可能因
// total_overflow 被拒绝：
//
//	交接前引用新版 → version_not_yet_effective
//	交接时刻引用旧版 → version_expired（尽管它登记的结束日期 3 月 31 日尚未到）
//	交接时刻引用新版 → total_overflow
//
// 另附一笔数量为 1 的报价：金额恰好等于上限 MaxInt64，上限本身是合法金额，仍可确认。
//
// 运行：
//
//	go run ./examples/overflow
package main

import (
	"fmt"
	"math"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均为 2026 年 UTC 零点（交接前的报价时刻除外），结束时刻不含。
var (
	oldStart = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // 旧版生效起点（含）
	oldEnd   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 旧版登记结束（不含）
	handoff  = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 新版生效、旧版实际结束的交接点
	before   = time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC) // 交接前：旧版有效、新版尚未生效
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := oldStart
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记旧版 seat-v1：单价 math.MaxInt64 分，
	//    登记的有效期为 2026-03-01 至 2026-03-31（结束时刻不含）。
	oldRegisteredEnd := oldEnd
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: math.MaxInt64,
		Start:     oldStart,
		End:       &oldRegisteredEnd,
	}))

	// 登记新版 seat-v2：单价同为 math.MaxInt64 分，
	// 2026-03-10 00:00 起替代旧版，不填结束时间、持续有效。
	// 登记后旧版的实际有效结束被截断到交接点，但它登记的结束时间仍是 3 月 31 日。
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: math.MaxInt64,
		Start:     handoff,
		Replaces:  "seat-v1",
	}))
	printViews(book, "① 两版登记完成后的版本视图")

	// ② 交接前（2026-03-09 12:00）用新标识引用新版 seat-v2，数量 2。
	//    新版在受理时刻尚未生效：账本先判版本可用性，直接给
	//    version_not_yet_effective，不会继续计算必然溢出的总价，
	//    也不会自动改用当时有效的旧版 seat-v1。
	now = before
	out2, err2 := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v2-before-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  2,
	})
	printQuote("② 交接前引用新版（quote-v2-before-handoff）", out2, err2)

	// ③ 把受理时刻推进到交接点本身 2026-03-10 00:00（半开区间，结束时刻不含）。
	//    用新标识引用旧版 seat-v1：旧版已被替代，实际有效期止于交接点，
	//    尽管它登记的结束时间 3 月 31 日尚未到，仍得到 version_expired；
	//    账本不会改用同项当时已生效的新版，也不会报 total_overflow。
	now = handoff
	out3, err3 := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v1-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  2,
	})
	printQuote("③ 交接时刻引用旧版（quote-v1-at-handoff）", out3, err3)

	// ④ 同一交接时刻用新标识引用新版 seat-v2，数量 2。
	//    开始时刻包含在有效期内，新版当时有效；数量是合法正整数，
	//    但 2×MaxInt64 超出 int64 上限（MaxInt64/单价 = 1，数量 2 > 1），
	//    这才因 total_overflow 被拒绝。
	out4, err4 := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  2,
	})
	printQuote("④ 交接时刻引用新版、数量 2（quote-v2-at-handoff）", out4, err4)

	// ⑤ 同一时刻再用新标识、数量 1 引用新版：
	//    1×MaxInt64 恰好等于上限，上限本身是合法金额，报价确认，
	//    单价与总价都为 MaxInt64——不能把上限本身描述成非法金额。
	out5, err5 := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v2-one-unit",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  1,
	})
	printQuote("⑤ 交接时刻引用新版、数量 1（quote-v2-one-unit）", out5, err5)

	// 汇总：②③④三笔拒绝的调用错误都为空，单价、总价都为零。
	type result struct {
		out tariff.Outcome
		err error
	}
	fmt.Println("汇总：")
	for _, r := range []result{{out2, err2}, {out3, err3}, {out4, err4}} {
		fmt.Printf("   %s：err=%v，Confirmed=%v，原因=%s，单价=%d，总价=%d\n",
			r.out.Request.RequestID, r.err, r.out.Confirmed, r.out.Reason,
			r.out.UnitPrice, r.out.Total)
	}
	fmt.Printf("   ⑤数量1：err=%v，Confirmed=%v，单价=%d，总价=%d（恰好等于上限，仍可确认）\n",
		err5, out5.Confirmed, out5.UnitPrice, out5.Total)
	fmt.Println("   拒绝结果中的零金额只是“未给出金额”，不代表登记的费率为零——两版登记单价见①；")
	fmt.Println("   ②③④调用的 err 都为 nil，说明它们是受理后的拒绝，err==nil 不等于确认报价。")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func printViews(book *tariff.Book, title string) {
	fmt.Printf("%s：\n", title)
	views, err := book.ItemVersions("seat")
	must(err)
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

func printQuote(title string, o tariff.Outcome, err error) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求 item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	fmt.Printf("   调用错误 err=%v\n", err)
	if o.Confirmed {
		fmt.Printf("   结果=已确认 单价=%d 分 总价=%d 分\n", o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 原因=%s 单价=%d 分 总价=%d 分\n",
			o.Reason, o.UnitPrice, o.Total)
	}
}
