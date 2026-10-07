// 命令 inrange 是“按时间范围查看生效版本”的完整可运行示例：
// 同一费率项 seat 的旧版 seat-v1（单价 150 分，2026-03-01 开始、登记结束 2026-03-31）
// 被新版 seat-v2（单价 180 分，2026-03-10 起替代旧版、2026-03-20 结束）截短实际有效期；
// 分别查看 3 月 9 日至 11 日、3 月 10 日至 25 日、3 月 20 日至 25 日，
// 并区分合法范围的空结果与 ErrInvalidRange、ErrItemNotFound 两类调用失败。
//
// 运行：
//
//	go run ./examples/inrange
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // 旧版生效起点（含）
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 旧版登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 新版生效起点，也是旧版实际结束（不含）
	v2End   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // 新版结束时刻（不含）
)

func main() {
	// 本示例只登记版本并按调用方给出的时间范围查询，不调用 Quote：
	// RegisterVersion 不读账本时钟，VersionsInRange 的 from/to 也由调用方显式给出，
	// 因此即使直接 NewBook()（真实时钟），无论在哪一天运行输出都确定、可复现，
	// 读者无需注入时钟或补写初始化代码。
	book := tariff.NewBook()

	// ① 登记旧版 seat-v1：单价 150 分，2026-03-01 生效，登记结束 2026-03-31（不含）。
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
	fmt.Println("① 登记旧版 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z)，err=<nil>")

	// ② 登记新版 seat-v2：单价 180 分，2026-03-10 起替代旧版，登记结束 2026-03-20（不含）。
	//    从交接时刻起旧版的实际有效区间被截短为 [03-01, 03-10)，
	//    但它登记时填写的结束 3 月 31 日原样保留——两种结束不是同一个值。
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
	fmt.Println("② 登记新版 seat-v2：单价 180 分，2026-03-10T00:00:00Z 起替代 seat-v1，登记结束 2026-03-20T00:00:00Z（不含），err=<nil>")
	printViews(book)

	// ③ 范围 [03-09, 03-11) 跨过交接点：旧版按截短后的实际结束 3 月 10 日参与，
	//    与范围在 [03-09, 03-10) 相交；新版与范围在 [03-10, 03-11) 相交。
	//    两版都列出，按实际生效起点先旧后新。
	printRange(book, "③",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC))

	// ④ 范围 [03-10, 03-25) 从交接点开始：旧版实际结束恰好等于范围开始，
	//    只是边界相接、不命中；只有新版与范围相交——即使范围一直延伸到新版结束之后，
	//    后面的空档也不会补入任何版本。
	printRange(book, "④", v2Start,
		time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC))

	// ⑤ 范围 [03-20, 03-25) 从新版结束时刻开始：新版只是边界相接、不命中，
	//    旧版也不会在新版结束后恢复——空列表、不报错，账本不拿邻近版本补位。
	printRange(book, "⑤", v2End,
		time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC))

	// ⑥ 结束不晚于开始：返回 ErrInvalidRange，不返回版本列表。
	//    这是调用失败，不是“范围内没有生效版本”（对照⑤的空列表、nil 错误）。
	at := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	got, err := book.VersionsInRange("seat", at, at)
	fmt.Printf("⑥ 结束不晚于开始（费率项 seat，from=to=%s）：\n", at.Format(time.RFC3339))
	fmt.Printf("   err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidRange) = %v；返回列表为 nil：%v（调用失败，不给版本列表）\n\n",
		errors.Is(err, tariff.ErrInvalidRange), got == nil)

	// ⑦ 范围合法，但费率项从未登记过版本：返回 ErrItemNotFound。
	from := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)
	got, err = book.VersionsInRange("never-seen", from, to)
	fmt.Printf("⑦ 范围合法但费率项从未登记（费率项 never-seen，[%s, %s)）：\n",
		from.Format(time.RFC3339), to.Format(time.RFC3339))
	fmt.Printf("   err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrItemNotFound) = %v；返回列表为 nil：%v\n\n",
		errors.Is(err, tariff.ErrItemNotFound), got == nil)

	// ⑧ 不存在的费率项同时使用非法范围：范围错误优先，仍返回 ErrInvalidRange，
	//    不会落到 ErrItemNotFound。
	got, err = book.VersionsInRange("never-seen", at, at)
	fmt.Printf("⑧ 不存在的费率项同时使用非法范围（费率项 never-seen，from=to=%s）：\n",
		at.Format(time.RFC3339))
	fmt.Printf("   err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidRange) = %v；errors.Is(err, tariff.ErrItemNotFound) = %v（范围错误优先）\n",
		errors.Is(err, tariff.ErrInvalidRange), errors.Is(err, tariff.ErrItemNotFound))
	fmt.Printf("   返回列表为 nil：%v\n", got == nil)
}

// printViews 打印登记完成后账本保存的完整版本视图。
func printViews(book *tariff.Book) {
	fmt.Println("登记后的版本视图：")
	views, err := book.ItemVersions("seat")
	if err != nil {
		panic(err)
	}
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 分 登记区间=[%s, %s) 实际有效区间=[%s, %s) 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice,
			v.Start.Format(time.RFC3339), fmtEnd(v.End),
			v.EffectiveStart.Format(time.RFC3339), fmtEnd(v.EffectiveEnd),
			v.Replaces, v.SupersededBy)
	}
	fmt.Println()
}

// printRange 执行一次范围查询并打印命中明细；
// 表头同时给出费率项与半开范围 [from, to)，让读者一眼看出查的是哪个项、哪段时间。
// 命中记录显示版本标识、整数分单价、登记起止时间和实际有效区间，
// 这些都是账本保存的原值，不会被裁成查询范围的边界。
func printRange(book *tariff.Book, label string, from, to time.Time) {
	fmt.Printf("%s 查询费率项 seat 在 [%s, %s) 内生效过的版本：\n",
		label, from.Format(time.RFC3339), to.Format(time.RFC3339))
	views, err := book.VersionsInRange("seat", from, to)
	if err != nil {
		fmt.Printf("   调用失败：err=%v\n\n", err)
		return
	}
	fmt.Printf("   命中 %d 个（err=%v），按实际生效起点从早到晚排列：\n", len(views), err)
	if len(views) == 0 {
		fmt.Println("   空列表：合法范围内没有命中任何版本（新版结束后旧版不恢复，空档不补入邻近版本），这不是调用失败")
		fmt.Println()
		return
	}
	for _, v := range views {
		fmt.Printf("   - %s：单价=%d 分 登记区间=[%s, %s) 实际有效区间=[%s, %s)\n",
			v.VersionID, v.UnitPrice,
			v.Start.Format(time.RFC3339), fmtEnd(v.End),
			v.EffectiveStart.Format(time.RFC3339), fmtEnd(v.EffectiveEnd))
	}
	fmt.Println()
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}
