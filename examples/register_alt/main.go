// 命令 register_alt 演示“登记替代费率版本”时一次失败、修正后成功的完整过程，
// 以及登记返回错误与报价受理后被拒绝之间的区别。
//
// 运行：
//
//	go run ./examples/register_alt
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

func main() {
	// 下述日期均指 UTC 零点，结束时刻不含。
	// 演示时钟：报价受理时刻从变量 now 读取，因此无论哪天运行，输出都确定、可复现。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now。
	var now time.Time
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	v1Start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	v1End := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	v2Start := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	v2End := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	v3Start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// ① 先登记 seat-v1：单价 150 分，2026-03-01 生效，登记结束为 2026-03-31（不含）。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     v1Start,
		End:       &v1End,
	}); err != nil {
		panic(err)
	}
	fmt.Println("① 登记 seat-v1：150 分，2026-03-01 起，登记结束 2026-03-31（不含）：成功")

	// ② 再登记 seat-v3：单价 200 分，2026-04-01 起持续有效（不填结束时间）。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
	}); err != nil {
		panic(err)
	}
	fmt.Println("② 登记 seat-v3：200 分，2026-04-01 起持续有效（不填结束）：成功")

	// ③ 尝试登记 seat-v2：单价 180 分，2026-03-10 起替代 seat-v1，但不填结束时间。
	//    seat-v1 虽然会被截短到 3 月 10 日，seat-v2 本身却会持续有效，
	//    延伸进 seat-v3 自 4 月 1 日起的有效时间，因此与同费率项的另一个版本重叠。
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "seat-v1",
		// End 故意留空：nil 表示持续有效
	})
	fmt.Printf("③ seat-v2（180 分，3 月 10 日起替代 v1，不填结束）登记结果：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrOverlap)=%v —— 返回错误即说明本次登记没有生效\n",
		errors.Is(err, tariff.ErrOverlap))

	// ④ 失败后立即查询：账本里仍只有 v1、v3 两个版本。
	//    v1 的登记结束仍是 3 月 31 日、实际结束也仍是 3 月 31 日，
	//    不存在被 v2 替代的关系；v3 完全不受影响。
	printViews("④ 登记失败后查询 seat 的版本（应仍只有两个，且无任何替代关系）", book)

	// ⑤ 沿用 seat-v2 这个版本标识，把结束时间改为 2026-04-01（不含）再次登记。
	//    失败的登记不占用版本标识；v2 的结束时刻与 v3 的开始时刻同为 4 月 1 日零点，
	//    两端半开区间在这一点相接，互不重叠。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2End,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	fmt.Printf("⑤ 沿用 seat-v2 标识、补上结束 %s（不含）再次登记：成功（err=<nil>）\n",
		v2End.Format(time.RFC3339))

	// ⑥ 成功后查询：注意 v1 的“登记结束”仍是 3 月 31 日（永不改变），
	//    而“实际有效结束”被截短到 3 月 10 日；v1 被 v2 替代，v2 替代 v1。
	printViews("⑥ 登记成功后查询 seat 的版本（三个版本，v1 已被截短并与 v2 相接）", book)

	// 报价受理时刻设为 2026-03-10 00:00（UTC 零点）：
	// 这正是 v1 实际有效区间的结束点（不含），也是 v2 有效区间的起点（含）。
	now = v2Start

	// ⑦ 用全新请求标识、数量 4 引用 seat-v1。调用 err 为 nil（报价已受理），
	//    但 Confirmed=false、原因 version_expired —— 受理后拒绝，不是调用错误。
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v1-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑦ 3 月 10 日用新标识引用 seat-v1（quote-v1-at-handoff，数量 4）", expired)
	fmt.Printf("   err==nil 但 Confirmed=%v、原因=%s —— 这是“受理后拒绝”，与③的登记返回错误不是一回事\n",
		expired.Confirmed, expired.Reason)

	// ⑧ 另一个全新请求标识、数量 4 引用 seat-v2：按 180 分确认，总价 720 分。
	confirmed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑧ 3 月 10 日用新标识引用 seat-v2（quote-v2-at-handoff，数量 4）", confirmed)
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}

func printViews(title string, b *tariff.Book) {
	fmt.Printf("%s：\n", title)
	views, err := b.ItemVersions("seat")
	if err != nil {
		panic(err)
	}
	fmt.Printf("   版本数量=%d\n", len(views))
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 分 生效=%s 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice, v.Start.Format(time.RFC3339),
			fmtEnd(v.End), fmtEnd(v.EffectiveEnd), v.Replaces, v.SupersededBy)
	}
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
