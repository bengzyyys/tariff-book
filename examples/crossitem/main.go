// 命令 crossitem 是“不同费率项使用同名版本时，怎样登记替代版本”的完整可运行示例：
// 同一本账本里 seat 与 room 都登记名为 v1 的版本；seat 再登记 v2 替代“本项的”v1，
// room 的同名 v1 完全不受影响。随后 seat 登记 v3 时误把已被替代的 v1 填作来源，
// 即使 room/v1 当时仍有效，也返回 ErrInvalidReplacement，不会借用另一项的同名版本。
//
// 运行：
//
//	go run ./examples/crossitem
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // seat/v1、room/v1 同时生效
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 两项 v1 的登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // seat/v2 替代本项 v1 的交接点
	v3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // seat/v3 计划的交接点
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记 seat/v1：单价 150 分，2026-03-01 生效，登记结束 2026-03-31（不含）。
	seatEnd := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v1",
		UnitPrice: 150,
		Start:     v1Start,
		End:       &seatEnd,
	}); err != nil {
		panic(err)
	}

	// ② 同一本账本再登记 room/v1：版本标识同样叫 v1，单价 300 分、有效期完全相同。
	//    版本标识只在费率项内唯一；跨费率项重名、跨费率项有效期重叠都允许。
	roomEnd := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "room",
		VersionID: "v1",
		UnitPrice: 300,
		Start:     v1Start,
		End:       &roomEnd,
	}); err != nil {
		panic(err)
	}
	printViews(book, "seat", "①② 两项同名 v1 登记完成后的版本视图（seat）")
	printViews(book, "room", "   （room）")

	// ③ 给 seat 登记 v2：单价 180 分，2026-03-10 起替代 v1，不填结束时间。
	//    Replaces 的来源只按“本次登记指定的费率项 seat”查找，截短的是 seat/v1；
	//    room 下同名的 v1 既不是替代来源，也不参与同项重叠判断，更不会被截断。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "v1",
	}); err != nil {
		panic(err)
	}
	fmt.Println("③ 登记 seat/v2（2026-03-10 起替代 seat 本项的 v1）：err=<nil>，登记生效")
	printViews(book, "seat", "   seat 版本视图")
	printViews(book, "room", "   room 版本视图（单价、两种结束时间、替代关系均不变）")

	// ④⑤ 把报价受理时刻推进到交接点 2026-03-10 00:00（结束时刻不含该点），
	//     两项各用一个从未使用过的新请求标识、数量 4 分别报价。
	now = v2Start

	seatQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-seat-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "v2",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("④ 交接点引用 seat/v2（quote-seat-v2-at-handoff）", seatQuote)

	roomQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-room-v1-at-handoff",
		ItemID:    "room",
		VersionID: "v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑤ 交接点引用 room/v1（quote-room-v1-at-handoff）", roomQuote)

	// ⑥ 一次错误的替代登记：给 seat 登记 v3（单价 200 分，2026-03-20 开始），
	//    却仍把 v1 填作替代来源。seat/v1 的实际有效期已被 v2 截断到 3 月 10 日，
	//    3 月 20 日不在其中——即使 room/v1 此时仍有效，也不能拿另一项的同名版本
	//    充当来源通过交接时间校验，必须返回 ErrInvalidReplacement；
	//    来源就在 seat 本项，也不能误报成 ErrReplaceTargetWrongItem。
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v3",
		UnitPrice: 200,
		Start:     v3Start,
		Replaces:  "v1",
	})
	fmt.Printf("⑥ 登记 seat/v3（3-20 开始，仍填 v1 为来源）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v\n",
		errors.Is(err, tariff.ErrInvalidReplacement))
	fmt.Printf("   errors.Is(err, tariff.ErrReplaceTargetWrongItem) = %v（来源就在 seat 本项，不能误报属于另一项）\n",
		errors.Is(err, tariff.ErrReplaceTargetWrongItem))

	// ⑦ 失败后查询：seat 仍只有 v1、v2，边界与替代关系与③之后完全一致；
	//    room 仍是单价 300 分的单个 v1；失败的登记也不占用 v3 标识。
	printViews(book, "seat", "⑦ 登记失败后的版本视图（seat，应与③完全一致）")
	printViews(book, "room", "   （room，应与③完全一致）")

	// ⑧ 另一种填法：本项根本没有该来源、只有其他项存在同名版本。
	//    hall 从未登记过，v1 只存在于 seat、room 中：返回 ErrReplaceTargetWrongItem，
	//    不会借用其他项的 v1 完成登记。
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "hall",
		VersionID: "v5",
		UnitPrice: 50,
		Start:     v3Start,
		Replaces:  "v1",
	})
	fmt.Printf("⑧ hall 登记 v5、来源填 v1（v1 只在 seat、room 中）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrReplaceTargetWrongItem) = %v\n",
		errors.Is(err, tariff.ErrReplaceTargetWrongItem))

	// ⑨ 所有费率项都没有该标识时：返回 ErrReplaceTargetNotFound。
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "hall",
		VersionID: "v5",
		UnitPrice: 50,
		Start:     v3Start,
		Replaces:  "v9",
	})
	fmt.Printf("⑨ hall 登记 v5、来源填 v9（任何项都没有 v9）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrReplaceTargetNotFound) = %v\n",
		errors.Is(err, tariff.ErrReplaceTargetNotFound))

	// ⑧⑨ 两次失败都不留痕：hall 费率项从未被创建，已有两项也不受影响。
	if _, err := book.ItemVersions("hall"); err != nil {
		fmt.Printf("⑩ 两次失败后查询 hall：err=%v（失败登记不创建费率项、不占用版本标识）\n", err)
	}
}

func printViews(book *tariff.Book, itemID, title string) {
	fmt.Printf("%s：\n", title)
	views, err := book.ItemVersions(itemID)
	if err != nil {
		panic(err)
	}
	for _, v := range views {
		fmt.Printf("   %s/%s：单价=%d 开始=%s 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			itemID, v.VersionID, v.UnitPrice, v.Start.Format(time.RFC3339),
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
	fmt.Printf("   来源=费率项 %s / 版本 %s，数量=%d；首次受理时刻=%s\n",
		o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	if o.Confirmed {
		fmt.Printf("   结果=已确认 单价=%d 分 × 数量 %d = 总价=%d 分\n",
			o.UnitPrice, o.Request.Quantity, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 原因=%s（err 为 nil，拒绝不是调用错误）\n", o.Reason)
	}
}
