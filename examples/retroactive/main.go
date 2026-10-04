// 命令 retroactive 是“补登记过去生效的替代版本后，怎样核对已有报价”的完整可运行示例：
// 报价按旧版确认后，仍在同一受理时刻补登记一个交接点早于登记当天的新版，
// 观察按时刻选版的答案改变，而已确认的报价保持首次受理结果不变。
//
// 运行：
//
//	go run ./examples/retroactive
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期与时刻均为 2026 年 UTC，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)   // 旧版生效
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)  // 旧版登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)  // 补登记的交接点：早于登记当天
	quoteAt = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC) // 报价受理与补登记发生的时刻
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记旧版 seat-v1：单价 150 分，2026-03-01 生效，登记结束 2026-03-31（不含）。
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
	fmt.Println("① 登记旧版 seat-v1：单价 150 分，[2026-03-01, 2026-03-31)，err=<nil>")

	// ② 2026-03-15 10:00，旧版仍在实际有效期内，用标识 quote-001 报价，数量 4。
	now = quoteAt
	first, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("② 3 月 15 日 10:00 首次报价（quote-001）", first)

	// ③ 补登记前，查询 3 月 15 日 10:00 的生效版本：是旧版。
	printEffective(book, "③ 补登记前查询 2026-03-15 10:00 的生效版本")

	// ④ 仍在 3 月 15 日 10:00 这个受理时刻，补登记新版 seat-v2：
	//    单价 180 分，2026-03-10 起替代旧版，不填结束时间。
	//    交接点（3 月 10 日）早于登记当天（3 月 15 日）也允许：
	//    替代规则只要求交接点晚于旧版开始、且落在旧版当前的实际有效区间内，
	//    不要求不早于登记时刻；同项也没有其他版本与 [03-10, ∞) 重叠。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	fmt.Println("④ 补登记 seat-v2（2026-03-10 起替代旧版，不填结束时间）：err=<nil>，登记生效")

	// ⑤ 版本视图：旧版的登记结束仍是 3 月 31 日（登记时填写的值，永不改变），
	//    实际有效结束已提前到 3 月 10 日——两者不是同一个值。
	printViews(book, "⑤ 补登记后的版本视图")

	// ⑥ 再次查询同一时刻：按时刻选版依据查询时账本已登记的实际有效区间，
	//    因此对过去时刻的回答从旧版变为新版。
	printEffective(book, "⑥ 补登记后查询同一时刻 2026-03-15 10:00 的生效版本")

	// ⑦ 但补登记不会重算已确认的报价：按原请求标识 Lookup，
	//    取回的仍是首次受理结果——旧版、数量 4、单价 150 分、总价 600 分、
	//    首次受理时刻 2026-03-15 10:00，确认状态不变。
	got, err := book.Lookup("quote-001")
	if err != nil {
		panic(err)
	}
	printOutcome("⑦ 补登记后按原标识 Lookup（quote-001）", got)
	fmt.Printf("   与首次结果完全一致：%v\n", got == first)

	// ⑧ 原样重试（相同标识、相同内容）同样返回首次结果。
	replay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑧ 原样重试（quote-001）", replay)
	fmt.Printf("   与首次结果完全一致：%v\n", replay == first)

	// ⑨ 用新标识引用旧版：旧版的实际有效期止于 3 月 10 日，
	//    在 3 月 15 日 10:00 的受理时刻已失效，得到 version_expired 拒绝。
	//    这是正常受理后的拒绝结果，调用的 err 为 nil。
	oldQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-002",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑨ 用新标识引用旧版（quote-002）", oldQuote)
	fmt.Printf("   调用 err==nil，但 Confirmed=%v、原因=%s：受理后的拒绝，不是调用错误\n",
		oldQuote.Confirmed, oldQuote.Reason)

	// ⑩ 用新标识引用新版：按 180 分确认，总价 720 分。
	newQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-003",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑩ 用新标识引用新版（quote-003）", newQuote)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 4 = 总价 %d 分\n",
		newQuote.Confirmed, newQuote.UnitPrice, newQuote.Total)

	// ⑪ 沿用原标识 quote-001、只把版本改成新版：请求内容冲突，
	//     调用本身返回 ErrRequestIDConflict，原 600 分记录不被覆盖。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	fmt.Printf("⑪ 沿用 quote-001 只把版本改成新版：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrRequestIDConflict) = %v\n",
		errors.Is(err, tariff.ErrRequestIDConflict))
	unchanged, err := book.Lookup("quote-001")
	if err != nil {
		panic(err)
	}
	fmt.Printf("   原记录未被覆盖：Confirmed=%v 版本=%s 数量=%d 单价=%d 总价=%d 首次受理时刻=%s\n",
		unchanged.Confirmed, unchanged.Request.VersionID, unchanged.Request.Quantity,
		unchanged.UnitPrice, unchanged.Total, unchanged.AcceptedAt.Format(time.RFC3339))
}

func printEffective(book *tariff.Book, title string) {
	v, err := book.EffectiveVersionAt("seat", quoteAt)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s：%s（单价=%d 分，实际有效结束=%s）\n",
		title, v.VersionID, v.UnitPrice, fmtEnd(v.EffectiveEnd))
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
