// 命令 retroactive 是“补登记一个过去就已生效的替代版本后，怎样核对已有报价”
// 的完整可运行示例：旧版有效期内先确认 600 分报价，随后仍在同一受理时刻
// 补登 5 天前起替代旧版的新版本；演示按时刻选版的答案随之改变，
// 而已确认报价按请求标识取回时保持首次结果不变。
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

// 下述日期均指同一时区（UTC），结束时刻不含；全部写死，不依赖运行当天。
var (
	oldStart   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)   // 旧版生效
	oldEnd     = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)  // 旧版登记结束（不含）
	handoff    = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)  // 新版补登的交接点（早于登记时刻）
	acceptedAt = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC) // 报价首次受理时刻；补登后时钟也不推进
)

func main() {
	// 演示时钟：受理时刻取自变量 now。本例从头到尾停在 2026-03-15 10:00，
	// 补登发生在“现在”，交接点却在 5 天前——靠的是登记允许补登过去的区间，
	// 而不是时钟回拨。生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := acceptedAt
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// 登记旧版本 seat-v1：单价 150 分，2026-03-01 生效，登记结束 2026-03-31（不含）。
	registeredOldEnd := oldEnd
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     oldStart,
		End:       &registeredOldEnd,
	}))

	// ① 补登前查询 3 月 15 日 10 点的生效版本：当时账本里只有旧版，答案是 seat-v1。
	printEffectiveAt(book, "① 补登前：3 月 15 日 10 点的生效版本")

	// ② 同一时刻，用标识 retro-quote-001 按旧版报价，数量 4：
	//    150 分 × 4 = 600 分，确认。
	first, err := book.Quote(tariff.QuoteRequest{
		RequestID: "retro-quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	must(err)
	printOutcome("② 旧版有效期内首次报价（retro-quote-001）", first)

	// ③ 时钟仍停在 3 月 15 日 10 点，补登新版本 seat-v2：单价 180 分，
	//    填写 2026-03-10 00:00 起替代旧版，不填结束时间（持续有效）。
	//    交接点虽然早于登记当天 5 天，但它晚于旧版开始、且落在旧版当时的
	//    实际有效期 [03-01, 03-31) 内，同项也没有其他版本与新区间重叠，
	//    现有替代规则照样接受——补登成功。补登不会撤销或重算②的报价。
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     handoff,
		Replaces:  "seat-v1",
	}))
	fmt.Println("③ 仍在 3 月 15 日 10 点补登 seat-v2（交接点填 3 月 10 日，不填结束时间）：err=<nil>，登记生效")

	// ④ 补登后的版本视图：旧版“登记结束”仍是 3 月 31 日（永不改变），
	//    “实际有效结束”已被提前到 3 月 10 日；新版两栏结束均为空，持续有效。
	fmt.Println("④ 补登后的版本视图：")
	views, err := book.ItemVersions("seat")
	must(err)
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice, fmtEnd(v.End), fmtEnd(v.EffectiveEnd),
			v.Replaces, v.SupersededBy)
	}

	// ⑤ 对同一个过去时刻 3 月 15 日 10 点再查一次生效版本：答案从旧版变为新版。
	//    按时刻选版依据的是“查询时账本已登记的实际有效区间”，补登改写了
	//    旧版对过去时刻的实际有效期，因此同一时刻的答案随之改变。
	printEffectiveAt(book, "⑤ 补登后：同一时刻 3 月 15 日 10 点的生效版本")

	// ⑥ 但按请求标识取回的是首次受理结果：Lookup 与原样重试都仍是②——
	//    旧版、数量 4、单价 150 分、总价 600 分，首次受理时刻保持在 3 月 15 日 10 点，
	//    确认状态不变。补登不会重算已经确认的报价。
	got, err := book.Lookup("retro-quote-001")
	must(err)
	printOutcome("⑥ 补登后按原标识 Lookup（retro-quote-001）", got)
	fmt.Printf("   与首次结果完全一致：%v\n", got == first)

	replay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "retro-quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑦ 补登后原样重试（retro-quote-001）", replay)
	fmt.Printf("   与首次结果完全一致：%v\n", replay == first)

	// ⑧ 用新的请求标识、在当前受理时刻引用旧版：调用正常受理（err 为 nil），
	//    但旧版实际有效结束已提前到 3 月 10 日，结果是 version_expired 拒绝。
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "retro-quote-old",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑧ 新标识引用旧版（retro-quote-old）", expired)
	fmt.Printf("   err=%v（为空）但 Confirmed=%v：这是受理后的拒绝，不是调用错误\n",
		err, expired.Confirmed)

	// ⑨ 换另一个新标识引用新版：180 分 × 4 = 720 分，确认。
	fresh, err := book.Quote(tariff.QuoteRequest{
		RequestID: "retro-quote-new",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑨ 新标识引用新版（retro-quote-new）", fresh)

	// ⑩ 沿用原标识、只把版本改成新版：请求内容与首次不同，
	//    返回 ErrRequestIDConflict（调用本身不成立），不产生新报价，也不覆盖原记录。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "retro-quote-001",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	fmt.Printf("⑩ 沿用原标识只把版本改成新版：err=%v（errors.Is(err, tariff.ErrRequestIDConflict)=%v）\n",
		err, errors.Is(err, tariff.ErrRequestIDConflict))
	unchanged, err := book.Lookup("retro-quote-001")
	must(err)
	fmt.Printf("   原记录保留：Confirmed=%v version=%s 数量=%d 单价=%d 分 总价=%d 分\n",
		unchanged.Confirmed, unchanged.Request.VersionID, unchanged.Request.Quantity,
		unchanged.UnitPrice, unchanged.Total)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}

func printEffectiveAt(book *tariff.Book, title string) {
	fmt.Printf("%s：\n", title)
	v, err := book.EffectiveVersionAt("seat", acceptedAt)
	must(err)
	fmt.Printf("   查询时刻=%s → %s：单价=%d 分 实际有效区间=%s\n",
		acceptedAt.Format(time.RFC3339), v.VersionID, v.UnitPrice,
		fmtInterval(v.EffectiveStart, v.EffectiveEnd))
}

// fmtInterval 把半开区间 [start, end) 格式化为确定文本；end 为 nil 表示持续有效。
func fmtInterval(start time.Time, end *time.Time) string {
	if end == nil {
		return start.Format(time.RFC3339) + " 起持续有效"
	}
	return "[" + start.Format(time.RFC3339) + ", " + end.Format(time.RFC3339) + ")"
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
