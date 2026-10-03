// 命令 reconcile 是“费率变化后如何核对一笔报价”的完整可运行示例。
//
// 运行：
//
//	go run ./examples/reconcile
package main

import (
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// 登记旧版本 seat-v1：单价 150 分/单位，
	// 登记的有效期为 2026-03-01 至 2026-03-31（结束时刻不含）。
	oldEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		End:       &oldEnd,
	}))

	// 2026-03-02 10:00，旧版本有效期内，用标识 quote-001 报价，数量 4。
	now = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	first, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	must(err)
	printOutcome("① 有效期内首次报价（quote-001）", first)

	// 登记新版本 seat-v2：单价 180，2026-03-10 00:00 起替代 seat-v1。
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
		Replaces:  "seat-v1",
	}))
	fmt.Println("② 新版本登记后的版本视图：")
	views, err := book.ItemVersions("seat")
	must(err)
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice, fmtEnd(v.End), fmtEnd(v.EffectiveEnd),
			v.Replaces, v.SupersededBy)
	}

	// 将受理时刻推进到交接点本身（有效区间结束时刻不含该点）。
	now = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	got, err := book.Lookup("quote-001")
	must(err)
	printOutcome("③ 交接点按原标识 Lookup（quote-001）", got)
	fmt.Printf("   与首次结果完全一致：%v\n", got == first)

	replay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	must(err)
	printOutcome("④ 交接点原样重试（quote-001）", replay)
	fmt.Printf("   与首次结果完全一致：%v\n", replay == first)

	fresh, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-002",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑤ 交接点用新标识引用旧版本（quote-002）", fresh)
	fmt.Printf("   本次返回 err==nil 但 Confirmed=%v，拒绝原因=%s，不能当作成功\n",
		fresh.Confirmed, fresh.Reason)

	// 再登记 seat-v3：2026-04-01 起替代 seat-v2，用于演示“版本尚未生效”的首次拒绝。
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Replaces:  "seat-v2",
	}))

	// 3 月 20 日：v3 尚未生效。非空标识的首次拒绝也会被保存。
	now = time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)
	early, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-003",
		ItemID:    "seat",
		VersionID: "seat-v3",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑥ v3 生效前首次报价（quote-003）", early)

	// 推进到 4 月 2 日：v3 已生效，但原样重试仍返回 3 月 20 日的首次拒绝。
	now = time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC)
	earlyReplay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-003",
		ItemID:    "seat",
		VersionID: "seat-v3",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑦ v3 生效后原样重试（quote-003）", earlyReplay)
	fmt.Printf("   与首次拒绝完全一致：%v（受理时刻仍是 %s）\n",
		earlyReplay == early, earlyReplay.AcceptedAt.Format(time.RFC3339))

	again, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-004",
		ItemID:    "seat",
		VersionID: "seat-v3",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑧ 改用新标识按 v3 重新报价（quote-004）", again)

	// 相同标识改动数量（改动费率项或版本同理）→ 请求内容冲突，原记录不被覆盖。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  5,
	})
	fmt.Printf("⑨ 相同标识改数量：err=%v\n", err)
	unchanged, err := book.Lookup("quote-001")
	must(err)
	fmt.Printf("   原记录未被覆盖：Confirmed=%v 数量=%d 单价=%d 总价=%d\n",
		unchanged.Confirmed, unchanged.Request.Quantity, unchanged.UnitPrice, unchanged.Total)

	// 从未受理过的标识 → 未找到，这是 err 非空的情况。
	_, err = book.Lookup("quote-999")
	fmt.Printf("⑩ 查询从未受理的标识：err=%v\n", err)

	// 空标识：返回拒绝结果且 err 为 nil，但该拒绝不会保存。
	empty, err := book.Quote(tariff.QuoteRequest{ItemID: "seat", VersionID: "seat-v2", Quantity: 1})
	must(err)
	fmt.Printf("⑪ 空标识报价：Confirmed=%v 原因=%s err=%v\n", empty.Confirmed, empty.Reason, err)
	_, err = book.Lookup("")
	fmt.Printf("   空标识查询：err=%v（空标识的拒绝未保存）\n", err)

	// 报价结果只保存在当前账本实例内。
	freshBook := tariff.NewBook()
	_, err = freshBook.Lookup("quote-001")
	fmt.Printf("⑫ 新建账本查询 quote-001：err=%v\n", err)
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
