// 命令 interpose 是“同一旧版本先被较晚生效的版本截短，随后又在剩余有效期内
// 被补登记的较早版本替代”的完整可运行示例：
// 先登记 v1（3/1–3/31）与 v3（3/20 起持续有效、替代 v1），再补登 v2
// （3/10–3/20、同样替代 v1）；v2 第一次因不填结束时间与 v3 重叠被
// ErrOverlap 拒绝，补上 3 月 20 日的结束时间后沿用同一标识登记成功。
//
// 运行：
//
//	go run ./examples/interpose
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
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // v1 登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // v2（补登）的交接点
	v2End   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // v2 登记结束（不含），与 v3 开始相接
	v3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // v3 先登记的、较晚的交接点
)

func main() {
	// 本示例只登记版本并按指定时刻查询，不调用 Quote：
	// RegisterVersion 不读账本时钟，EffectiveVersionAt 的查询时刻由调用方显式给出，
	// 因此即使直接 NewBook()（真实时钟），输出也完全确定、与运行当天无关。
	book := tariff.NewBook()

	// ① 先登记 seat-v1：单价 150 分，2026-03-01 生效，登记结束 2026-03-31（不含）。
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
	fmt.Println("① 登记 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z)，err=<nil>")

	// ② 再登记 seat-v3：单价 200 分，2026-03-20 起替代 v1、持续有效（不填结束时间）。
	//    v1 先被这个“较晚生效”的版本截短：实际有效结束提前到 3 月 20 日，
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
	fmt.Println("② 登记 seat-v3：单价 200 分，2026-03-20T00:00:00Z 起替代 seat-v1、持续有效，err=<nil>")
	printViews(book, "①② 登记顺序为 v1、v3 时的版本视图")

	// ③ 调用方如何判断“已经显示被 v3 替代的 v1，还能不能再次作为替代来源”：
	//    用 ItemVersions 读 v1 当前的实际有效区间 [EffectiveStart, EffectiveEnd)，
	//    检查拟用交接点是否晚于它的生效起点、且仍早于它当前的实际有效结束。
	//    “被替代”只说明它在当前实际结束之后失效，并不表示它在剩余实际有效期内
	//    不能被另一个更早生效的版本再次替代。
	checkSource(book, "seat-v1", v2Start)

	// ④ 第一次登记 seat-v2：单价 180 分，2026-03-10 起替代 v1，但不填结束时间。
	//    v2 的实际有效区间会是 [03-10, ∞)：v1 作为被替代版本会被截短、不参与
	//    本次重叠判断，但同项其他版本 v3（[03-20, ∞)）照常参与，两段持续有效
	//    区间相互重叠，因此返回 ErrOverlap。
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "seat-v1",
	})
	fmt.Printf("④ 首次登记 seat-v2（替代 v1，不填结束时间）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrOverlap) = %v；持续有效区间与 v3 重叠，整次登记未生效\n",
		errors.Is(err, tariff.ErrOverlap))

	// ⑤ 失败后整次回滚：仍只有 v1、v3 两个版本，v2 未入库、标识未被占用；
	//    v1 的实际结束仍是被 v3 截短后的 3 月 20 日，替代关系没有被改写。
	printViews(book, "⑤ 登记失败后的版本视图（仍只有 v1、v3，v2 未入库、标识未被占用）")

	// ⑥ 沿用同一个版本标识 seat-v2，把结束时间补为 2026-03-20 00:00（不含）后再次登记。
	//    失败的登记不占用版本标识；v2 的区间 [03-10, 03-20) 与 v3 的开始端点相接，
	//    结束时刻不含，相接不算重叠；交接点 03-10 仍在 v1 当前实际有效期
	//    [03-01, 03-20) 内，登记成功。
	v2RegisteredEnd := v2End
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
	fmt.Println("⑥ 补填结束时间 2026-03-20T00:00:00Z 后沿用标识 seat-v2 再次登记：err=<nil>，登记生效")

	// ⑦ 成功后的版本关系：
	//    列表按生效时间排列为 v1、v2、v3，而不是登记顺序 v1、v3、v2；
	//    v1 的登记结束仍是 3 月 31 日，实际有效结束被进一步提前到 3 月 10 日，
	//    改显示由 v2 替代；v2 和 v3 的 Replaces 都保留 v1——v3 登记时的替代来源
	//    不会为了排成 v1→v2→v3 的链而改写成 v2，v3 的起止时间和单价也不变。
	printViews(book, "⑦ 补登成功后的版本视图（按生效时间排列，而非登记顺序 v1→v3→v2）")

	// ⑧ 按指定时刻查询：选择依据是各版本当前的实际有效区间，与登记顺序无关。
	//    [03-01, 03-10) 选 v1；3 月 10 日交接点（含）至 3 月 20 日前选 v2；
	//    3 月 20 日交接点（含）起选 v3，即使 v1 登记的结束时刻 3 月 31 日尚未到/已到也不回退。
	fmt.Println("⑧ 按指定时刻查询生效版本（选择依据是账本已登记的实际有效区间）：")
	for _, at := range []time.Time{
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		v2Start, // 3 月 10 日交接点（含），选中 v2
		time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 19, 23, 59, 59, 0, time.UTC), // 3 月 20 日前一刻
		v3Start, // 3 月 20 日交接点（含），选中 v3
		v1End,   // v1 登记结束当天，仍选 v3
	} {
		printAt(book, at)
	}
}

// checkSource 用公开入口演示调用方判断旧版本能否在 handoff 处再次作为替代来源：
// handoff 必须晚于该版本的生效起点，且落在它当前的实际有效区间内
// （恰好等于实际结束时刻也不行，因为结束时刻不含）。
func checkSource(book *tariff.Book, versionID string, handoff time.Time) {
	views, err := book.ItemVersions("seat")
	if err != nil {
		panic(err)
	}
	var src tariff.VersionView
	for _, v := range views {
		if v.VersionID == versionID {
			src = v
		}
	}
	fmt.Printf("③ 拟补登的交接点 %s 是否仍可用 %s 作为替代来源：\n",
		handoff.Format(time.RFC3339), versionID)
	fmt.Printf("   %s：生效起点=%s 登记结束=%s 当前实际有效结束=%s 被替代=%q\n",
		versionID, src.Start.Format(time.RFC3339),
		fmtEnd(src.End), fmtEnd(src.EffectiveEnd), src.SupersededBy)
	afterStart := handoff.After(src.Start)
	beforeEffEnd := false
	if src.EffectiveEnd == nil {
		beforeEffEnd = true
	} else {
		beforeEffEnd = handoff.Before(*src.EffectiveEnd)
	}
	fmt.Printf("   交接点晚于生效起点=%v，且早于当前实际有效结束=%v：已被替代不等于不能再次被替代，%s 仍可作为替代来源\n",
		afterStart, beforeEffEnd, versionID)
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
