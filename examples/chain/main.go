// 命令 chain 是“连续调整费率时该替代哪一版”的完整可运行示例：
// 同一费率项上甲→乙→丙连续替代，其中丙第一次误把已被乙替代的甲版
// 填为被替代版本，登记返回 ErrInvalidReplacement；查询确认账本无变化后，
// 沿用同一丙版标识改填乙版登记成功。程序末尾还用一次独立登记演示：
// 普通登记允许区间相接（接续），但这不代表可以在旧版实际结束点替代它。
//
// 运行：
//
//	go run ./examples/chain
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC 零点，结束时刻不含在有效期内。
var (
	aStart = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // 甲版开始
	aEnd   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 甲版登记结束（不含）
	bStart = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 乙版开始、甲→乙交接点
	bEnd   = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC) // 乙版登记结束（不含）
	cStart = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // 丙版开始、乙→丙交接点
	cEnd   = time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC) // 丙版登记结束（不含）
)

func main() {
	// 演示时钟：本示例只有登记（RegisterVersion 不读时钟），
	// 注入时钟只为完整呈现初始化方式，并保证在任何一天运行输出都确定。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now。
	now := aStart
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记甲版：单价 150 分，2026-03-01 生效，登记结束 2026-03-31（不含）。
	aRegisteredEnd := aEnd
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-a",
		UnitPrice: 150,
		Start:     aStart,
		End:       &aRegisteredEnd,
	}))

	// ② 登记乙版：单价 180 分，2026-03-10 起替代甲版，登记结束 2026-03-25（不含）。
	//    登记成功后甲版的实际有效结束被截短到 3 月 10 日，但甲版登记的结束 3 月 31 日
	//    仍原样保留——这两个“结束”不是同一个字段，连续调费极易在这里误判该替代谁。
	bRegisteredEnd := bEnd
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-b",
		UnitPrice: 180,
		Start:     bStart,
		End:       &bRegisteredEnd,
		Replaces:  "seat-a",
	}))
	printViews(book, "①② 甲、乙两版登记完成后的版本视图")

	// ③ 准备登记丙版：单价 200 分，2026-03-20 开始、2026-03-24 结束（均不含端点）。
	//    误填 Replaces 为甲版：甲版登记的结束虽仍是 3 月 31 日，
	//    但它的实际有效期已在 3 月 10 日乙版接手时结束，3 月 20 日不在甲版当前的
	//    实际有效区间内，因此这次登记返回 ErrInvalidReplacement。
	cRegisteredEnd := cEnd
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-c",
		UnitPrice: 200,
		Start:     cStart,
		End:       &cRegisteredEnd,
		Replaces:  "seat-a",
	})
	fmt.Printf("③ 登记丙版（200 分，03-20 起，误填被替代版本=甲版）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v\n",
		errors.Is(err, tariff.ErrInvalidReplacement))
	fmt.Println("   登记失败是调用本身返回错误：丙版不入库、标识不被占用，与报价受理后的拒绝不同")

	// ④ 失败后查询：仍然只有甲、乙两版。
	//    甲版实际结束仍为 3 月 10 日、仍显示被乙版替代；乙版没有被截短（实际结束仍是
	//    登记时填的 3 月 25 日），也没有出现丙版——整次登记已完全回滚。
	printViews(book, "④ 失败后的版本视图（仍只有甲、乙两版，关系与边界均不变）")

	// ⑤ 沿用同一个版本标识 seat-c，只把被替代版本改为当前实际生效的乙版。
	//    乙版当前实际有效区间是 [03-10, 03-25)，3 月 20 日落在其中，因此登记成功；
	//    失败的登记不占用版本标识。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-c",
		UnitPrice: 200,
		Start:     cStart,
		End:       &cRegisteredEnd,
		Replaces:  "seat-b",
	}); err != nil {
		panic(err)
	}
	fmt.Println("⑤ 沿用标识 seat-c、被替代版本改为乙版再次登记：err=<nil>，登记生效")

	// ⑥ 成功后的视图：
	//    乙版“替代”甲版、又“被替代”丙版；实际有效结束被截短到 3 月 20 日，
	//    但它登记时填写的结束 3 月 25 日仍原样保留；
	//    甲版被乙版替代的第一次交接关系继续保留，实际结束仍为 3 月 10 日、登记结束仍为 3 月 31 日。
	printViews(book, "⑥ 丙版登记成功后的版本视图（乙版一肩挑两头，登记结束仍为 03-25）")

	// ⑦ 边界：普通登记中相邻区间允许接续，不代表可以在旧版结束点替代它。
	//    另起一本独立账本，登记一条 [03-10, 03-20) 的版本（不填 Replaces）：
	//    它与乙版登记的区间端点相接、互不重叠，这是普通登记允许的“接续”。
	//    但同样是 3 月 20 日这个时刻，若用它在 Replaces 中替代实际有效结束为
	//    3 月 20 日的版本，校验要求 Start 严格早于被替代版本的实际结束，
	//    恰好等于实际结束时刻会以 ErrInvalidReplacement 拒绝。
	fmt.Println("⑦ 边界对照：普通登记允许相接接续，但替代时刻恰好等于实际结束会被拒绝")
	book2 := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))
	dEnd1 := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	must(book2.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-d",
		UnitPrice: 180,
		Start:     bStart,
		End:       &dEnd1,
	}))
	eEnd := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	if err := book2.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-e",
		UnitPrice: 200,
		Start:     cStart, // 2026-03-20 00:00 = seat-d 的实际结束时刻
		End:       &eEnd,
		Replaces:  "seat-d",
	}); err != nil {
		fmt.Printf("   在实际结束点 03-20 替代 d 版：err=%v\n", err)
		fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v：端点相接可普通接续，但不能在结束点替代\n",
			errors.Is(err, tariff.ErrInvalidReplacement))
	} else {
		panic("expected ErrInvalidReplacement when replacing exactly at effective end")
	}
	// 同一时刻不填 Replaces 做普通登记则成功：一个版本的结束与另一个版本的开始
	// 落在同一瞬间属于相接，半开区间下不算重叠。
	if err := book2.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-f",
		UnitPrice: 200,
		Start:     cStart,
		End:       &eEnd,
	}); err != nil {
		panic(err)
	}
	fmt.Println("   同一时刻不填 Replaces 普通登记 seat-f：err=<nil>，端点相接允许接续")
	printViews(book2, "   边界对照账本视图")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func printViews(book *tariff.Book, title string) {
	fmt.Printf("%s：\n", title)
	views, err := book.ItemVersions("seat")
	must(err)
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 开始=%s 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice, v.Start.Format(time.RFC3339),
			fmtEnd(v.End), fmtEnd(v.EffectiveEnd), v.Replaces, v.SupersededBy)
	}
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}
