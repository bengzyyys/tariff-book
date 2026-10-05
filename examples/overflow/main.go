// 命令 overflow 是“区分指定版本不能使用与版本有效但总价无法表示”的完整可运行示例：
// 同一费率项的两个版本单价都是有符号 64 位整数最大值（分），数量为 2 时
// 数学总价超出该类型上限。交接前引用新版得到 version_not_yet_effective，
// 交接时刻引用旧版得到 version_expired（尽管它登记的结束日期尚未到），
// 交接时刻引用新版才得到 total_overflow；同刻以数量 1 引用新版则恰好
// 等于上限、仍可确认。
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

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // 旧版生效
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 旧版登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 新版替代旧版的交接点
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记旧版 seat-v1：单价为有符号 64 位整数最大值（分），
	//    2026-03-01 生效，登记结束 2026-03-31（不含）。
	v1RegisteredEnd := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: math.MaxInt64,
		Start:     v1Start,
		End:       &v1RegisteredEnd,
	}); err != nil {
		panic(err)
	}

	// ② 登记新版 seat-v2：单价同样为最大值（分），2026-03-10 起替代旧版、持续有效。
	//    旧版的登记结束仍是 3 月 31 日，实际有效结束从这一刻起被截短到 3 月 10 日。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: math.MaxInt64,
		Start:     v2Start,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	printViews(book, "①② 两版登记完成后的版本视图")

	// 以下每笔报价都使用各自从未使用过的非空请求标识，数量为 2，
	// 因此输出反映的都是首次受理的判断。
	// 数量为合法正整数时，账本先判断指定版本在受理时刻是否有效，
	// 只有有效版本才会进入金额判断、因总价超过上限被拒绝。

	// ③ 交接前（2026-03-09 00:00）引用新版：新版尚未生效，
	//    得到 version_not_yet_effective——即使 2×MaxInt64 在数学上必然溢出，
	//    版本不可用优先于金额判断，也不会自动改用当时有效的旧版。
	now = time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	early, err := book.Quote(tariff.QuoteRequest{
		RequestID: "overflow-v2-before-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  2,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("③ 交接前引用新版（overflow-v2-before-handoff）", early)

	// ④ 交接时刻（2026-03-10 00:00）引用旧版：旧版的实际有效区间止于交接点
	//    （结束时刻不含），尽管它登记的结束 3 月 31 日尚未到，仍得到 version_expired。
	now = v2Start
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "overflow-v1-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  2,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("④ 交接时刻引用旧版（overflow-v1-at-handoff）", expired)

	// ⑤ 交接时刻引用新版：新版有效（开始时刻含在有效期内），
	//    同一超大金额请求这时才因总价无法表示得到 total_overflow。
	overflow, err := book.Quote(tariff.QuoteRequest{
		RequestID: "overflow-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  2,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑤ 交接时刻引用新版（overflow-v2-at-handoff）", overflow)

	// ⑥ 同刻以数量 1 引用新版：总价恰好等于有符号 64 位整数最大值，
	//    上限本身不是非法金额，仍可确认。
	exact, err := book.Quote(tariff.QuoteRequest{
		RequestID: "overflow-v2-quantity-one",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  1,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑥ 交接时刻引用新版、数量 1（overflow-v2-quantity-one）", exact)
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

func printOutcome(title string, o tariff.Outcome) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求 item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	if o.Confirmed {
		fmt.Printf("   结果=已确认 单价=%d 分 总价=%d 分\n", o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 原因=%s 单价=%d 总价=%d（err 为 nil，拒绝不是调用错误）\n",
			o.Reason, o.UnitPrice, o.Total)
	}
}
