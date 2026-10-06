// 命令 quantity 是“数量填错后怎样重新报价”的完整可运行示例：
// 同一本账本、同一费率项的同一有效版本下，先用一个从未用过的非空标识提交
// 数量 0 得到 invalid_quantity 的首次拒绝；随后按该标识查询、原样重试都
// 取回同一份拒绝；沿用该标识只把数量改为 4 得到 ErrRequestIDConflict，
// 原拒绝记录不被覆盖；最后换用另一个从未用过的标识、以数量 4 引用同一
// 版本，才得到确认报价。
//
// 运行：
//
//	go run ./examples/quantity
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

	// ① 登记 seat-v1：单价 150 分/单位，
	//    登记的有效期为 2026-03-01 至 2026-03-31（结束时刻不含）。
	v1End := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		End:       &v1End,
	}); err != nil {
		panic(err)
	}
	fmt.Println("① 登记 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z)，err=<nil>")

	// ② 2026-03-02 10:00，seat-v1 在有效期内，但用从未用过的非空标识
	//    quote-001 首次提交时把数量错填为 0。数量必须是正整数，
	//    首次受理即被拒绝：原因 invalid_quantity，调用的 err 为 nil，
	//    单价和总价均为零，结果仍保留原始数量 0、费率项、版本和首次受理时刻。
	now = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	first, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  0,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("② 有效期内首次提交、数量错填为 0（quote-001）", first)
	fmt.Printf("   调用 err==nil，但 Confirmed=%v、原因=%s：拒绝已被受理并保存，不是调用错误\n",
		first.Confirmed, first.Reason)

	// ③ 按该标识查询：取回的是同一份首次拒绝，原数量 0 和首次受理时刻不变。
	got, err := book.Lookup("quote-001")
	if err != nil {
		panic(err)
	}
	printOutcome("③ 按原标识 Lookup（quote-001）", got)
	fmt.Printf("   与首次拒绝完全一致：%v\n", got == first)

	// ④ 原样重试（相同标识、相同内容）同样返回首次拒绝，而不是重新判断。
	replay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  0,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("④ 原样重试（quote-001）", replay)
	fmt.Printf("   与首次拒绝完全一致：%v（受理时刻仍是 %s）\n",
		replay == first, replay.AcceptedAt.Format(time.RFC3339))

	// ⑤ 沿用原标识、只把数量改正为 4：请求内容与已保存的首次结果不同，
	//    调用本身返回 ErrRequestIDConflict，没有可使用的报价结果，
	//    已受理的首次拒绝不会被这次修正覆盖。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	fmt.Printf("⑤ 沿用 quote-001 只把数量改为 4：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrRequestIDConflict) = %v；这是调用错误，没有报价结果\n",
		errors.Is(err, tariff.ErrRequestIDConflict))
	unchanged, err := book.Lookup("quote-001")
	if err != nil {
		panic(err)
	}
	fmt.Printf("   原拒绝记录未被覆盖：Confirmed=%v 原因=%s 数量=%d 首次受理时刻=%s\n",
		unchanged.Confirmed, unchanged.Reason, unchanged.Request.Quantity,
		unchanged.AcceptedAt.Format(time.RFC3339))

	// ⑥ 改正数量的正确做法：换用一个从未用过的新标识 quote-002，
	//    以数量 4 引用同一有效版本。这是一次新的首次报价，
	//    仍按受理时刻的版本有效期与金额规则判断——此处版本有效，
	//    确认单价 150 分、总价 600 分，来源和数量与这次新请求一致。
	fixed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-002",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑥ 换新标识、数量改正为 4 后重新报价（quote-002）", fixed)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 %d = 总价 %d 分\n",
		fixed.Confirmed, fixed.UnitPrice, fixed.Request.Quantity, fixed.Total)
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
