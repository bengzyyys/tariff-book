// 命令 byitem 是“按费率项查看已受理报价”的完整可运行示例：
// 核对报价时不必先知道请求标识，直接给出费率项标识，即可取得当前账本中
// 请求来源属于该项的全部首次受理结果——确认与拒绝都列出，旧确认价不被
// 当前费率重算；费率项从未登记过版本时，version_not_found 的拒绝同样查得到。
//
// 运行：
//
//	go run ./examples/byitem
package main

import (
	"errors"
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

	// ① 登记旧版本 seat-v1：单价 150 分，登记有效期至 2026-03-31（不含）。
	oldEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		End:       &oldEnd,
	}); err != nil {
		panic(err)
	}

	// ② 2026-03-02 10:00，旧版有效期内，用 quote-001 按数量 4 报价，确认 600 分。
	now = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	first, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("② 旧版有效期内首次报价（quote-001）", first)

	// ③ 登记新版 seat-v2：单价 180，2026-03-10 起替代 seat-v1。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}

	// ④ 交接后，新请求 quote-002 仍引用旧版，得到 version_expired 拒绝。
	now = time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-002",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("④ 交接后新请求引用旧版（quote-002）", expired)

	// ⑤ 另一费率项 addon 从未登记过任何版本；合法数量的请求因
	//    version_not_found 被拒绝，这份拒绝同样按费率项保存、可查。
	notFound, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-addon-1",
		ItemID:    "addon",
		VersionID: "addon-v1",
		Quantity:  1,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑤ 未登记版本的费率项首次报价（quote-addon-1）", notFound)

	// ⑥ 空请求标识的拒绝不保存；稍后的列表不应出现它。
	empty, err := book.Quote(tariff.QuoteRequest{
		ItemID: "seat", VersionID: "seat-v1", Quantity: 1,
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("⑥ 空请求标识报价：Confirmed=%v 原因=%s err=%v（该拒绝不保存）\n\n",
		empty.Confirmed, empty.Reason, err)

	// ⑦ 不必知道请求标识，直接按费率项 seat 取出全部首次受理结果。
	//    旧确认仍是旧版、150 分、600 分；失效拒绝的零金额不会被当成免费确认价。
	list, err := book.ItemOutcomes("seat")
	if err != nil {
		panic(err)
	}
	fmt.Printf("⑦ 按费率项 seat 查看已受理报价（共 %d 条，按首次受理时刻排列）：\n", len(list))
	for _, o := range list {
		printOutcome("  ·", o)
	}

	// ⑧ 从未登记过版本的 addon 也能查到 version_not_found 拒绝，
	//    不会因为费率项没有版本而让查询失败。
	addonList, err := book.ItemOutcomes("addon")
	if err != nil {
		panic(err)
	}
	fmt.Printf("⑧ 按费率项 addon 查看（共 %d 条，未登记版本也能查到拒绝）：\n", len(addonList))
	for _, o := range addonList {
		printOutcome("  ·", o)
	}

	// ⑨ 非空但没有任何匹配记录的费率项：返回空列表、不报错。
	none, err := book.ItemOutcomes("never-used")
	fmt.Printf("⑨ 查无记录的非空费率项 never-used：条数=%d err=%v\n\n", len(none), err)

	// ⑩ 空费率项标识：返回调用方可明确识别的输入错误。
	_, err = book.ItemOutcomes("")
	fmt.Printf("⑩ 空费率项标识：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrEmptyItemIDQuery) = %v\n\n",
		errors.Is(err, tariff.ErrEmptyItemIDQuery))

	// ⑪ 列表是独立副本：改写返回记录不影响后续单笔查询、原样重试与再次列表。
	list[0].Total = 1
	list[0].Confirmed = false
	list[0].Request.Quantity = 99
	got, err := book.Lookup("quote-001")
	if err != nil {
		panic(err)
	}
	fmt.Printf("⑪ 改写列表元素后再 Lookup（quote-001）：Confirmed=%v 数量=%d 单价=%d 总价=%d（仍是首次结果）\n",
		got.Confirmed, got.Request.Quantity, got.UnitPrice, got.Total)
	again, err := book.ItemOutcomes("seat")
	if err != nil {
		panic(err)
	}
	fmt.Printf("   再次按费率项查看仍为 %d 条，首条总价=%d 数量=%d，未被外部改写污染\n",
		len(again), again[0].Total, again[0].Request.Quantity)
}

func printOutcome(title string, o tariff.Outcome) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求标识=%s item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.RequestID, o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	if o.Confirmed {
		fmt.Printf("   结果=已确认 单价=%d 分 总价=%d 分\n\n", o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 原因=%s 单价=%d 总价=%d（零金额不是免费确认价）\n\n",
			o.Reason, o.UnitPrice, o.Total)
	}
}
