// 命令 crossitem 是“不同费率项使用同名版本时，怎样登记替代版本”的完整可运行示例：
// 同一本账本中 seat 与 room 各自登记名为 v1 的版本后，给 seat 登记替代 v1 的 v2——
// 替代来源只在本次登记指定的费率项内查找；随后再演示一次仍把本项已失效的 v1
// 填作替代来源的失败登记，以及两种“找不到来源”的错误。
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

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // seat/v1 与 room/v1 共同的生效起点
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 两个 v1 共同的登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // seat/v2 替代 seat/v1 的交接点
	v3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // seat/v3 计划的交接点（seat/v1 的实际有效期已止于 3 月 10 日）
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ①② 同一本账本中登记两个费率项的同名版本 v1：
	//    seat/v1 单价 150 分、room/v1 单价 300 分，
	//    有效期都是 2026-03-01 至 2026-03-31（UTC 零点、结束时刻不含）。
	//    版本标识只在同一费率项内唯一，不同费率项可以复用同名版本。
	seatV1End := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v1",
		UnitPrice: 150,
		Start:     v1Start,
		End:       &seatV1End,
	}); err != nil {
		panic(err)
	}
	roomV1End := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "room",
		VersionID: "v1",
		UnitPrice: 300,
		Start:     v1Start,
		End:       &roomV1End,
	}); err != nil {
		panic(err)
	}
	printViews(book, "①② seat 与 room 各自登记 v1 后的版本视图")

	// ③ 给 seat 登记 v2：单价 180 分，2026-03-10 起替代 v1。
	//    Replaces 填的只是版本标识，替代来源按本次登记指定的费率项 seat 查找，
	//    被截短的只是 seat/v1，与 room 的同名 v1 无关。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "v1",
	}); err != nil {
		panic(err)
	}
	fmt.Println("③ 登记 seat/v2（2026-03-10 起替代 v1）：err=<nil>，登记生效")

	// ④ seat/v1 的登记结束仍是 3 月 31 日（登记时填写的值，永不改变），
	//    实际有效结束变为 3 月 10 日，并保留 seat 两版之间的替代关系；
	//    room/v1 的单价、两种结束时间和替代关系都不受另一项登记的影响。
	printViews(book, "④ seat/v2 登记后的版本视图")

	// ⑤⑥ 把受理时刻推进到交接点 2026-03-10 00:00（结束时刻不含该点），
	//     各用一个从未使用过的新请求标识、数量 4 引用 seat/v2 与 room/v1。
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
	printOutcome("⑤ 交接点用新标识引用 seat/v2（quote-seat-v2-at-handoff）", seatQuote)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 4 = 总价 %d 分\n",
		seatQuote.Confirmed, seatQuote.UnitPrice, seatQuote.Total)

	roomQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-room-v1-at-handoff",
		ItemID:    "room",
		VersionID: "v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑥ 交接点用新标识引用 room/v1（quote-room-v1-at-handoff）", roomQuote)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 4 = 总价 %d 分；room/v1 未被另一项的交接截短，仍按自己的单价确认\n",
		roomQuote.Confirmed, roomQuote.UnitPrice, roomQuote.Total)

	// ⑦ 错误的替代登记：给 seat 登记 2026-03-20 开始的 v3，单价 200 分，
	//    却仍把 v1 填作替代来源。seat/v1 的实际有效期已在 3 月 10 日被 v2 截短，
	//    3 月 20 日不在其当前的实际有效区间内；即使 room/v1 在 3 月 20 日仍然有效，
	//    也不能借另一项的同名版本通过交接时间校验。
	//    来源就在本项、只是已失效，因此返回 ErrInvalidReplacement，
	//    不能误报成“来源属于另一项”（ErrReplaceTargetWrongItem）。
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v3",
		UnitPrice: 200,
		Start:     v3Start,
		Replaces:  "v1",
	})
	fmt.Printf("⑦ 登记 seat/v3（2026-03-20 开始，仍填 v1 作替代来源）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v；",
		errors.Is(err, tariff.ErrInvalidReplacement))
	fmt.Printf("errors.Is(err, tariff.ErrReplaceTargetWrongItem) = %v\n",
		errors.Is(err, tariff.ErrReplaceTargetWrongItem))

	// ⑧ 失败后两项的版本信息与④完全一致：seat 仍只有 v1、v2，room 仍只有 v1；
	//    各项的单价、两种结束时间和替代关系都没有变化，v3 未入库、标识未被占用。
	printViews(book, "⑧ 登记失败后的版本视图（应与④完全一致）")

	// ⑨ 本项找不到来源、其他项却有同名版本：room 下没有 v2，v2 只登记在 seat 下，
	//    返回 ErrReplaceTargetWrongItem——不会借用 seat 的 v2 完成 room 的登记。
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "room",
		VersionID: "v9",
		UnitPrice: 350,
		Start:     v3Start,
		Replaces:  "v2",
	})
	fmt.Printf("⑨ 给 room 登记 v9、替代来源填 v2（v2 只属于 seat）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrReplaceTargetWrongItem) = %v\n",
		errors.Is(err, tariff.ErrReplaceTargetWrongItem))

	// ⑩ 所有费率项都没有所填的标识：返回 ErrReplaceTargetNotFound。
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "room",
		VersionID: "v10",
		UnitPrice: 350,
		Start:     v3Start,
		Replaces:  "v99",
	})
	fmt.Printf("⑩ 给 room 登记 v10、替代来源填 v99（任何项都没有该标识）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrReplaceTargetNotFound) = %v\n",
		errors.Is(err, tariff.ErrReplaceTargetNotFound))

	// ⑪ ⑨⑩ 两次失败同样整次回滚，两项的版本信息仍与④完全一致。
	printViews(book, "⑪ ⑨⑩ 两次失败登记后的版本视图（仍与④完全一致）")
}

func printViews(book *tariff.Book, title string) {
	fmt.Printf("%s：\n", title)
	for _, itemID := range []string{"seat", "room"} {
		views, err := book.ItemVersions(itemID)
		if err != nil {
			panic(err)
		}
		for _, v := range views {
			fmt.Printf("   %s/%s：单价=%d 开始=%s 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
				v.ItemID, v.VersionID, v.UnitPrice, v.Start.Format(time.RFC3339),
				fmtEnd(v.End), fmtEnd(v.EffectiveEnd), v.Replaces, v.SupersededBy)
		}
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
