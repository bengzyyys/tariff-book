// 命令 gapfill 是“填补费率版本之间的空档”的完整可运行示例：
// 先登记的两版 seat-v3（2026-03-20 起持续有效）与 seat-v1
// （2026-03-01 至 2026-03-10）在 [03-10, 03-20) 之间留下空档，
// 查询空档得到 ErrNoEffectiveVersion，账本不会拿邻近版本补位。
// 补登 seat-v2 时，第一次不填结束时间会延伸进 v3 的有效期，返回 ErrOverlap，
// 账本不会自动以 v3 的开始作为结束；第二次把 v1 填进 Replaces，
// 但 3 月 10 日已经不在 v1 的实际有效期内，返回 ErrInvalidReplacement，
// “接在旧版后面”不是替代。两次失败后账本都仍是原来的两版。
// 最后把 v2 登记为 [2026-03-10, 2026-03-20)、Replaces 留空，恰好补齐空档：
// 不截断、不替代任何旧版，三版按生效时间排列为 v1、v2、v3。
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
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // v1 生效起点（含）
	v1End   = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // v1 结束时刻（不含），也是空档起点
	v2Start = v1End
	v2End   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // v2 结束时刻（不含），也是 v3 起点
	v3Start = v2End
	gapAt   = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // 空档内部
)

func main() {
	// 本示例只登记版本并按指定时刻查询，不调用 Quote：
	// RegisterVersion 不读账本时钟，EffectiveVersionAt 的查询时刻由调用方显式给出，
	// 因此即使直接 NewBook()（真实时钟），输出也完全确定、与运行当天无关。
	book := tariff.NewBook()

	// ① 先登记 seat-v3：单价 200 分，2026-03-20 起持续有效（不填结束时间），
	//    不替代任何版本（Replaces 留空）。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
	}); err != nil {
		panic(err)
	}
	fmt.Println("① 登记 seat-v3：单价 200 分，2026-03-20T00:00:00Z 起持续有效，Replaces 留空，err=<nil>")

	// ② 再登记 seat-v1：单价 150 分，2026-03-01 生效，2026-03-10 结束（不含），
	//    同样不替代任何版本。登记顺序是先 v3 后 v1。
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

	// ③ 版本列表按生效时间排列为 v1、v3，而不是登记顺序 v3、v1；
	//    两版的登记起止与实际有效区间一致，替代与被替代关系均为空。
	printViews(book, "③ 两版登记完成后的版本视图（按生效时间排列，而非登记顺序）")

	// ④ 补登前按时刻查询：
	//    3 月 9 日落在 v1 内；3 月 15 日落在两版之间的空档 [03-10, 03-20)，
	//    返回 ErrNoEffectiveVersion——账本不会拿时间上邻近的 v1 或 v3 补位；
	//    3 月 20 日（开始时刻含）已是 v3。
	fmt.Println("④ 补登 v2 前按时刻查询：")
	printAt(book, time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	printAt(book, gapAt)
	printAt(book, v3Start)

	// ⑤ 第一次尝试补登 seat-v2：单价 180 分，2026-03-10 起，Replaces 留空，
	//    但不填结束时间。它的实际有效区间会是 [03-10, ∞)，延伸进 v3 的
	//    [03-20, ∞)；填补空档不需要替代任何版本，账本也不会自动以 v3 的开始
	//    作为 v2 的结束，因此返回 ErrOverlap。
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
	})
	fmt.Printf("⑤ 登记 seat-v2（不填结束时间，Replaces 留空）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrOverlap) = %v；登记失败是调用本身返回错误，整次登记未生效\n",
		errors.Is(err, tariff.ErrOverlap))

	// ⑥ 失败后整次回滚：仍只有 v1、v3 两个版本，v2 未入库、标识未被占用，
	//    v1 的结束（3 月 10 日）和 v3 的开始（3 月 20 日）都保持原值、关系全空；
	//    空档查询的结论不变。
	printViews(book, "⑥ 第一次失败后的版本视图（仍只有 v1、v3，边界与关系不变）")
	printAt(book, gapAt)

	// ⑦ 第二次尝试补登 seat-v2：这次把结束时间填为 3 月 20 日，却把 v1 填进
	//    Replaces。v1 的实际有效区间是 [03-01, 03-10)，3 月 10 日恰好等于它的
	//    实际结束时刻——结束时刻不含，该点已经不在 v1 的实际有效期内。
	//    v2 只是时间上接在 v1 后面，并不替代 v1，因此返回 ErrInvalidReplacement。
	v2RegisteredEnd := v2End
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2RegisteredEnd,
		Replaces:  "seat-v1",
	})
	fmt.Printf("⑦ 登记 seat-v2（结束=2026-03-20，但 Replaces 填 seat-v1）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v；"+
		"3 月 10 日不在 v1 的实际有效期 [03-01, 03-10) 内，“接在旧版后面”不是替代\n",
		errors.Is(err, tariff.ErrInvalidReplacement))

	// ⑧ 第二次失败同样整次回滚：账本仍只有 v1、v3，边界与替代关系不变。
	printViews(book, "⑧ 第二次失败后的版本视图（仍只有 v1、v3，边界与关系不变）")

	// ⑨ 第三次用同一个标识 seat-v2 正确补登：单价 180 分，
	//    [2026-03-10, 2026-03-20) 恰好填满空档，Replaces 留空。
	//    两侧端点与既有版本相接：半开区间端点相接不算重叠，登记成功；
	//    失败的登记不占用版本标识，因此 seat-v2 仍可使用。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2RegisteredEnd,
	}); err != nil {
		panic(err)
	}
	fmt.Println("⑨ 补登 seat-v2：单价 180 分，[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)，Replaces 留空：err=<nil>，登记生效")

	// ⑩ 补登成功后的版本视图：按生效时间排列为 v1、v2、v3（登记顺序是 v3、v1、v2）；
	//    三版登记的起止时间与实际有效区间完全一致，替代与被替代关系全部为空；
	//    v1 的结束仍是 3 月 10 日、v3 的开始仍是 3 月 20 日，二者都没有被移动。
	printViews(book, "⑩ 补登成功后的版本视图（v1、v2、v3，替代关系全空）")

	// ⑪ 再次按时刻查询：原来的空档时刻现在返回 v2、单价 180 分；
	//    3 月 10 日交接点（开始时刻含）属于 v2，3 月 20 日交接点属于 v3，
	//    结束时刻不含、开始时刻包含；时间相接没有产生任何替代关系。
	fmt.Println("⑪ 补登后按时刻查询（开始时刻包含、结束时刻不包含）：")
	printAt(book, time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	printAt(book, v2Start)
	printAt(book, gapAt)
	printAt(book, v3Start)
	printAt(book, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
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

func printAt(book *tariff.Book, at time.Time) {
	v, err := book.EffectiveVersionAt("seat", at)
	if errors.Is(err, tariff.ErrNoEffectiveVersion) {
		fmt.Printf("   查询 %s：无生效版本（%v），账本不拿邻近版本补位\n", at.Format(time.RFC3339), err)
		return
	}
	if err != nil {
		panic(err)
	}
	fmt.Printf("   查询 %s：%s（单价=%d，实际有效区间 [%s, %s)）\n",
		at.Format(time.RFC3339), v.VersionID, v.UnitPrice,
		v.EffectiveStart.Format(time.RFC3339), fmtEnd(v.EffectiveEnd))
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}
