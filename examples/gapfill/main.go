// 命令 gapfill 是“填补费率版本之间的空档”的完整可运行示例：
// 先登记的两版（v3 自 3 月 20 日起持续有效、v1 早在 3 月 10 日结束）
// 中间留下 [03-10, 03-20) 的空档，查询空档得到 ErrNoEffectiveVersion；
// 补登 v2 时 Replaces 必须留空——不填结束时间会与 v3 重叠（ErrOverlap），
// 把在交接点已经失效的 v1 填入 Replaces 会得到 ErrInvalidReplacement；
// 区间恰好填满空档、Replaces 留空时登记才成功，且不移动两侧旧版的任何边界。
//
// 运行：
//
//	go run ./examples/gapfill
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // v1 生效起点
	v1End   = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // v1 登记结束（不含），也是空档起点
	v2Start = v1End                                        // 补登版本 v2 的起点：与 v1 结束相接
	v2End   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // v2 登记结束（不含），也是空档终点
	v3Start = v2End                                        // v3 生效起点：与 v2 结束相接
	gapAt   = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // 空档内部
)

func main() {
	// 本示例只登记版本并按指定时刻查询，不调用 Quote：
	// RegisterVersion 不读账本时钟，EffectiveVersionAt 的查询时刻由调用方显式给出，
	// 因此即使直接 NewBook()（真实时钟），输出也完全确定、与运行当天无关。
	book := tariff.NewBook()

	// ① 先登记 seat-v3（注意它不是最后登记的）：单价 200 分，
	//    2026-03-20 起持续有效（不填结束时间），不替代任何版本（Replaces 留空）。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
	}); err != nil {
		panic(err)
	}
	fmt.Println("① 登记 seat-v3：单价 200 分，2026-03-20T00:00:00Z 起持续有效，Replaces 留空，err=<nil>")

	// ② 再登记较早的 seat-v1：单价 150 分，2026-03-01 至 2026-03-10（不含），
	//    同样不替代任何版本。它与 v3 之间留下 [03-10, 03-20) 的空档。
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
	fmt.Println("② 登记 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-10T00:00:00Z)，Replaces 留空，err=<nil>")
	printViews(book, "①② 两版登记完成（登记顺序是 v3、v1，不是按生效时间）")

	// ③ 查询空档内部 3 月 15 日：没有任何版本的实际有效区间覆盖它，
	//    返回 ErrNoEffectiveVersion——账本不会拿时间邻近的 v1 或 v3 补位。
	printAt(book, gapAt, "③ 查询空档内部")

	// ④ 容易填错之一：v2 不填结束时间（Replaces 留空）。
	//    它的实际有效区间会是 [03-10, ∞)，与持续有效的 v3（[03-20, ∞)）重叠。
	//    Replaces 留空时没有任何旧版会为它截短，账本也不会自动拿 v3 的开始时刻
	//    当作 v2 的结束，因此返回 ErrOverlap。
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start, // 不填 End：持续有效
	})
	fmt.Printf("④ 登记 seat-v2（不填结束时间，Replaces 留空）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrOverlap) = %v；账本不会自动以 v3 的开始作为 v2 的结束\n",
		errors.Is(err, tariff.ErrOverlap))
	printViews(book, "   失败后仍只有 v1、v3")
	printAt(book, gapAt, "   失败后再查 3 月 15 日")

	// ⑤ 容易填错之二：把 seat-v1 填进 Replaces（即使结束时间已经填对）。
	//    “接在旧版后面”不等于替代：替代要求新版本的 Start 严格落在被替代版本
	//    当前的实际有效区间 [EffectiveStart, EffectiveEnd) 内（结束时刻不含）。
	//    v1 的实际结束恰是 3 月 10 日零点，v2 的开始也恰是 3 月 10 日零点，
	//    该点已经不在 v1 的实际有效期内，因此返回 ErrInvalidReplacement。
	//    区间本身恰好不重叠，所以这不是 ErrOverlap——错的是替代关系；
	//    填补空档应把 Replaces 留空。
	v2RegisteredEnd := v2End
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2RegisteredEnd,
		Replaces:  "seat-v1",
	})
	fmt.Printf("⑤ 登记 seat-v2（结束时间填对，但 Replaces 填 seat-v1）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v；errors.Is(err, tariff.ErrOverlap) = %v\n",
		errors.Is(err, tariff.ErrInvalidReplacement), errors.Is(err, tariff.ErrOverlap))
	printViews(book, "   失败后仍只有 v1、v3，也没有建立任何替代关系")

	// ⑥ 正确的补登记：区间恰好填满空档 [03-10, 03-20)，Replaces 留空。
	//    两侧端点与 v1 的结束、v3 的开始落在同一瞬间属于相接，不算重叠；
	//    ④⑤ 两次失败都整次回滚、不占用 seat-v2 标识，沿用同一标识即可登记成功。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2RegisteredEnd,
	}); err != nil {
		panic(err)
	}
	fmt.Println("⑥ 登记 seat-v2：单价 180 分，[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)，Replaces 留空：err=<nil>，登记生效")

	// ⑦ 补登后的版本视图：
	//    列表按生效时间排列为 v1、v2、v3，与登记顺序（v3、v1、v2）无关；
	//    三版登记的起止时间与实际有效区间完全一致，替代与被替代关系全空；
	//    v1 的结束仍是 3 月 10 日、v3 的开始仍是 3 月 20 日且仍持续有效——
	//    没有 Replaces 的补登不移动任何旧版边界，时间相接也不产生替代关系。
	printViews(book, "⑦ 补登成功后的版本视图（按生效时间排列，而非登记顺序）")

	// ⑧ 按指定时刻查询：原来的空档时刻 3 月 15 日现在返回 v2、180 分；
	//    3 月 10 日交接点属于 v2（开始时刻包含），3 月 20 日交接点属于 v3
	//    （v2 的结束时刻不含）——时间相接不等于发生替代，选版只看实际有效区间。
	fmt.Println("⑧ 按指定时刻查询生效版本：")
	for _, at := range []time.Time{
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		v2Start, // 3 月 10 日交接点（含），归 v2
		gapAt,   // 原空档内部，归 v2
		time.Date(2026, 3, 19, 23, 59, 59, 0, time.UTC),
		v3Start, // 3 月 20 日交接点：v2 结束不含，归 v3
		time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	} {
		printAt(book, at, "  ")
	}
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

func printAt(book *tariff.Book, at time.Time, label string) {
	got, err := book.EffectiveVersionAt("seat", at)
	if errors.Is(err, tariff.ErrNoEffectiveVersion) {
		fmt.Printf("%s %s：err=%v（空档内无生效版本，不用邻近版本补位）\n",
			label, at.Format(time.RFC3339), err)
		return
	}
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s %s：%s（单价=%d 分，实际有效区间 [%s, %s)）\n",
		label, at.Format(time.RFC3339), got.VersionID, got.UnitPrice,
		got.EffectiveStart.Format(time.RFC3339), fmtEnd(got.EffectiveEnd))
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}
