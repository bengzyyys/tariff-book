// 命令 requote 是“数量填错后怎样重新报价”的完整可运行示例：
// 用新标识首次提交数量 0 得到 invalid_quantity 拒绝后，演示查询与原样重试
// 取回的都是同一份首次拒绝；沿用原标识改数量会得到 ErrRequestIDConflict，
// 原拒绝不被覆盖；只有换用从未用过的新标识，才会发起一笔新的首次报价。
//
// 运行：
//
//	go run ./examples/requote
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

	// ① 登记有效版本 seat-v1：单价 150 分/单位，
	//    有效期为 2026-03-01 至 2026-03-31（UTC，结束时刻不含）。
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
	fmt.Println("① 登记有效版本 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z)，err=<nil>")

	// ② 2026-03-02 10:00（版本有效期内），用一个从未用过的非空标识提交数量 0。
	//    数量必须是正整数；这次调用的 err 为 nil，但结果是原因 invalid_quantity 的拒绝，
	//    确认状态为否、单价和总价均为零，请求里的原始数量、费率项、版本原样保留。
	now = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	wrongReq := tariff.QuoteRequest{
		RequestID: "quote-wrong-qty",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  0,
	}
	rejected, err := book.Quote(wrongReq)
	if err != nil {
		panic(err)
	}
	printOutcome("② 首次提交数量 0（quote-wrong-qty）", rejected)
	fmt.Printf("   err=%v：拒绝已被保存，但它是正常受理结果，不是调用错误；零金额也不是免费报价\n", err)

	// 把时钟拨到另一个时刻，证明查询与重试取回的受理时刻不会被重算。
	now = time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)

	// ③ 按原标识查询：非空标识的首次拒绝也已保存，取回的就是②那份记录。
	got, err := book.Lookup("quote-wrong-qty")
	if err != nil {
		panic(err)
	}
	printOutcome("③ 按原标识 Lookup（quote-wrong-qty）", got)
	fmt.Printf("   与首次拒绝完全一致：%v\n", got == rejected)

	// ④ 原样再次提交（相同标识、相同内容）：仍返回首次拒绝，
	//    原数量 0 与首次受理时刻 2026-03-02 10:00 保持不变。
	replay, err := book.Quote(wrongReq)
	if err != nil {
		panic(err)
	}
	printOutcome("④ 原样再次提交（quote-wrong-qty）", replay)
	fmt.Printf("   与首次拒绝完全一致：%v（数量仍为 %d，受理时刻仍是 %s，没有按 3 月 3 日重新受理）\n",
		replay == rejected, replay.Request.Quantity, replay.AcceptedAt.Format(time.RFC3339))

	// ⑤ 只把数量改成 4、沿用原标识提交：请求内容与首次不同，
	//    调用本身返回 ErrRequestIDConflict，没有可使用的新报价结果。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "quote-wrong-qty",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	fmt.Printf("⑤ 沿用原标识、只把数量改为 4：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrRequestIDConflict) = %v；这次调用没有可使用的报价结果\n",
		errors.Is(err, tariff.ErrRequestIDConflict))

	// ⑥ 冲突不会覆盖任何记录：原拒绝仍能查到，原因、原始数量、金额与受理时刻都不变。
	still, err := book.Lookup("quote-wrong-qty")
	if err != nil {
		panic(err)
	}
	printOutcome("⑥ 冲突后再查原标识（quote-wrong-qty）", still)
	fmt.Printf("   原拒绝未被覆盖：原因=%s 数量=%d 单价=%d 总价=%d 受理时刻=%s\n",
		still.Reason, still.Request.Quantity, still.UnitPrice, still.Total,
		still.AcceptedAt.Format(time.RFC3339))

	// ⑦ 改用另一个从未用过的标识、数量 4 引用同一有效版本：
	//    这是一笔新的首次报价，按 150 分确认总价 600 分，
	//    来源（费率项、版本）和数量都来自这次新请求，受理时刻是本次时刻。
	fixed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-right-qty",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑦ 换新标识、数量 4 重新报价（quote-right-qty）", fixed)
	fmt.Printf("   Confirmed=%v；来源 item=%s version=%s 与数量=%d 均属本次新请求，单价 %d 分 × 4 = 总价 %d 分\n",
		fixed.Confirmed, fixed.Request.ItemID, fixed.Request.VersionID, fixed.Request.Quantity,
		fixed.UnitPrice, fixed.Total)
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
