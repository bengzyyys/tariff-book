// 命令 retruncation 是“同一旧版先被较晚生效的版本截短，随后在剩余有效期内
// 补登记较早生效的替代版本”的完整可运行示例：
// 先登记 v1（3 月 1 日至 3 月 31 日），再登记 3 月 20 日起替代 v1 的 v3，
// 最后补登记 3 月 10 日至 3 月 20 日、同样替代 v1 的 v2。
// 首次登记 v2 不填结束时间，因持续有效区间与 v3 重叠返回 ErrOverlap；
// 补上 3 月 20 日的结束时间后，沿用同一个 v2 标识登记成功。
//
// 运行：
//
//	go run ./examples/retruncation
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // v1 生效
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // v1 登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // v2 替代 v1 的交接点
	v3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // v3 替代 v1 的交接点，也是 v2 的结束（不含）
)

func main() {
	// 本示例只登记版本并按指定时刻查询，不受理报价：
	// RegisterVersion 与 EffectiveVersionAt 都不读取账本时钟，
	// 因此直接使用默认真实时钟即可，输出同样确定、可复现，
	// 无需注入演示时钟，也不必等待真实日期到来。
	book := tariff.NewBook()

	// ① 登记 seat-v1：单价 150 分，2026-03-01 生效，登记结束 2026-03-31（不含）。
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

	// ② 登记 seat-v3：单价 200 分，2026-03-20 起替代 v1、持续有效（不填结束时间）。
	//    从这一刻起 v1 的实际有效结束被截短到 3 月 20 日，
	//    但它登记时填写的结束 3 月 31 日仍然保留。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	printViews(book, "①② v1、v3 登记完成后的版本视图（v1 已被较晚生效的 v3 截短）")

	// ③ 首次尝试补登记 seat-v2：单价 180 分，2026-03-10 起替代 v1，但不填结束时间。
	//    v1 已显示被 v3 替代，并不代表它在 3 月 10 日不能再次被替代：
	//    3 月 10 日晚于 v1 的生效起点，且仍落在 v1 当前的实际有效区间
	//    [03-01, 03-20) 内，交接时刻本身合法。真正让这次登记失败的是
	//    不填结束时间时 v2 的持续有效区间 [03-10, ∞) 会延伸进 v3 的
	//    有效时间 [03-20, ∞)——Replaces 只能截短 v1，不能让 v2 占用 v3 的时间，
	//    因此返回 ErrOverlap。
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "seat-v1",
	})
	fmt.Printf("③ 登记 seat-v2（替代 v1，不填结束时间）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrOverlap) = %v；只有 err==nil 才表示登记生效\n",
		errors.Is(err, tariff.ErrOverlap))

	// ④ 失败后查询：仍只有 v1、v3 两个版本，v2 不存在；
	//    v1 的实际结束仍是 3 月 20 日、仍显示被 v3 替代，替代关系不变；
	//    v3 不受影响，失败的登记也不占用 seat-v2 这个版本标识。
	printViews(book, "④ 登记失败后的版本视图（应与①②完全一致）")

	// ⑤ 沿用同一个版本标识 seat-v2，把结束时间补为 2026-03-20 00:00（不含）后再次登记。
	//    v2 的区间 [03-10, 03-20) 与 v3 的 [03-20, ∞) 只是端点相接，不算重叠。
	v2RegisteredEnd := v3Start
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2RegisteredEnd,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	fmt.Println("⑤ 补填结束时间 2026-03-20 后沿用标识 seat-v2 再次登记：err=<nil>，登记生效")

	// ⑥ 成功后的查询：版本列表按生效时刻排列为 v1、v2、v3，
	//    而不是登记顺序 v1、v3、v2。v1 的登记结束仍是 3 月 31 日，
	//    实际结束进一步提前到 3 月 10 日，SupersededBy 改为 v2；
	//    v2 与 v3 的 Replaces 都保留 v1，v3 的起止时间和单价不变——
	//    这不是 v1→v2→v3 依次替代的链，而是 v2、v3 先后各自替代了 v1。
	printViews(book, "⑥ 登记成功后的版本视图（按生效时刻排列，而非登记顺序）")

	// ⑦⑧⑨⑩ 按指定时刻查询生效版本：选择依据是各版本的实际有效区间，
	//      与登记顺序、与 v1 尚未到的登记结束日期都无关。
	printEffective(book, "⑦ 查询 2026-03-05 00:00（v2 交接点之前）",
		time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC))
	printEffective(book, "⑧ 查询 2026-03-10 00:00（v2 交接点，含）", v2Start)
	printEffective(book, "⑨ 查询 2026-03-15 12:00（v2 区间内）",
		time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	printEffective(book, "⑩ 查询 2026-03-20 00:00（v3 交接点，含）", v3Start)
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

func printEffective(book *tariff.Book, title string, at time.Time) {
	v, err := book.EffectiveVersionAt("seat", at)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s：%s（单价=%d 分，实际有效结束=%s）\n",
		title, v.VersionID, v.UnitPrice, fmtEnd(v.EffectiveEnd))
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}
