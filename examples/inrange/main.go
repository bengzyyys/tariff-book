// 命令 inrange 是“按时间范围查看生效版本”的完整可运行示例：
// 同一费率项 seat 上，旧版 seat-v1（单价 150 分，2026-03-01 起、登记结束
// 2026-03-31）被新版 seat-v2（单价 180 分，2026-03-10 起替代、2026-03-20
// 结束）截短。分别查看 [03-09, 03-11)、[03-10, 03-25)、[03-20, 03-25)
// 三段范围：第一段按生效先后列出旧版和新版，第二段只列新版，第三段是
// 空列表——旧版按实际结束的 3 月 10 日参与判断，新版结束后旧版不恢复，
// 范围里的空档也不补入邻近版本。随后演示结束不晚于开始返回
// ErrInvalidRange、费率项从未登记返回 ErrItemNotFound，以及两者同时
// 出现时范围错误优先。
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
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 新版替代旧版的交接点
	v2End   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // 新版结束（不含）
)

func main() {
	// 本示例只登记版本并按时间范围查询，不调用 Quote：
	// RegisterVersion 不读账本时钟，VersionsInRange 的查询范围由调用方显式给出，
	// 因此即使直接 NewBook()（真实时钟），输出也完全确定、与运行当天无关。
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
	fmt.Println("① 登记 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z)，err=<nil>")

	// ② 登记新版 seat-v2：单价 180 分，2026-03-10 起替代 seat-v1，2026-03-20 结束（不含）。
	//    旧版的登记结束仍是 3 月 31 日，实际有效结束从这一刻起被截短到 3 月 10 日。
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
	fmt.Println("② 登记 seat-v2：单价 180 分，[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)，替代 seat-v1，err=<nil>")
	printViews(book, "   两版登记完成后的版本视图")

	// ③ 查看 [03-09, 03-11)：跨过 3 月 10 日交接点，旧版和新版都命中，
	//    按实际生效起点从早到晚排列。返回的是账本保存的版本信息：
	//    旧版仍从 3 月 1 日开始、登记结束仍是 3 月 31 日，只是实际结束为
	//    3 月 10 日——起止时间不会被裁成查询范围的边界。
	printRange(book, "seat",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC),
		"③ 查看 seat 的 [2026-03-09, 2026-03-11)：跨过交接点，旧版、新版都命中")

	// ④ 查看 [03-10, 03-25)：旧版的实际结束（3 月 10 日）恰好等于范围开始，
	//    仅在边界处相接不算命中，因此只列新版；范围后半段（3 月 20 日之后）
	//    是空档，不会补入邻近版本。
	printRange(book, "seat",
		v2Start,
		time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
		"④ 查看 seat 的 [2026-03-10, 2026-03-25)：旧版仅与范围开始相接，只列新版")

	// ⑤ 查看 [03-20, 03-25)：新版的结束（3 月 20 日）恰好等于范围开始，不算命中；
	//    新版结束后旧版不会恢复。费率项存在但范围内没有命中版本：
	//    返回空列表且 err 为 nil——这是正常空结果，不是调用失败。
	printRange(book, "seat",
		v2End,
		time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
		"⑤ 查看 seat 的 [2026-03-20, 2026-03-25)：新版仅与范围开始相接，没有命中版本")

	// ⑥ 结束时刻不晚于开始时刻：一律返回 ErrInvalidRange，不给出版本列表。
	_, err := book.VersionsInRange("seat",
		time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	fmt.Printf("⑥ 查看 seat 的 [2026-03-11, 2026-03-09)（结束不晚于开始）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidRange) = %v；范围错误是调用失败，没有版本列表\n",
		errors.Is(err, tariff.ErrInvalidRange))

	// ⑦ 范围合法但费率项从未登记过版本：返回 ErrItemNotFound。
	_, err = book.VersionsInRange("addon",
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC))
	fmt.Printf("⑦ 查看从未登记的 addon 的 [2026-03-09, 2026-03-11)：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrItemNotFound) = %v\n",
		errors.Is(err, tariff.ErrItemNotFound))

	// ⑧ 不存在的费率项同时使用非法范围：范围错误优先，仍返回 ErrInvalidRange。
	_, err = book.VersionsInRange("addon",
		time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	fmt.Printf("⑧ 查看 addon 的 [2026-03-11, 2026-03-09)（未登记 + 非法范围）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidRange) = %v；范围错误优先于费率项未登记\n",
		errors.Is(err, tariff.ErrInvalidRange))
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

func printRange(book *tariff.Book, itemID string, from, to time.Time, title string) {
	views, err := book.VersionsInRange(itemID, from, to)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s：\n", title)
	fmt.Printf("   查询 item=%s 范围 [%s, %s)，命中 %d 个版本：\n",
		itemID, from.Format(time.RFC3339), to.Format(time.RFC3339), len(views))
	for _, v := range views {
		fmt.Printf("   · %s：单价=%d 分 登记起止=[%s, %s) 实际有效区间=[%s, %s)\n",
			v.VersionID, v.UnitPrice,
			v.Start.Format(time.RFC3339), fmtEnd(v.End),
			v.EffectiveStart.Format(time.RFC3339), fmtEnd(v.EffectiveEnd))
	}
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}
