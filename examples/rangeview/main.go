// 命令 rangeview 是“按时间范围查看费率版本”的完整可运行示例：
// 核对一段时间的费率安排时，不必事先知道版本标识，给出费率项、范围开始和
// 结束时刻，即可取得这段时间内实际生效过的版本列表——范围包含开始时刻、
// 不包含结束时刻，仅边界相接不算命中；被替代的旧版按截短后的实际结束判断，
// 空档不补入相邻版本。
//
// 运行：
//
//	go run ./examples/rangeview
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

func day(dayOfMonth int) time.Time {
	return time.Date(2026, 3, dayOfMonth, 0, 0, 0, 0, time.UTC)
}

func main() {
	book := tariff.NewBook()

	// ① 登记旧版本 seat-v1：单价 150 分，3 月 1 日起生效，登记结束 3 月 31 日（不含）。
	v1End := day(31)
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: day(1), End: &v1End,
	}); err != nil {
		panic(err)
	}

	// ② 登记新版 seat-v2：单价 180 分，3 月 10 日起替代旧版，3 月 20 日结束（不含）。
	//    旧版的实际有效区间随即被截断到 3 月 10 日，登记结束仍是 3 月 31 日。
	v2End := day(20)
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: day(10), End: &v2End, Replaces: "seat-v1",
	}); err != nil {
		panic(err)
	}

	// ③ 查看 3 月 9 日至 3 月 11 日：跨交接点，旧版和新版都命中。
	views, err := book.ItemVersionsInRange("seat", day(9), day(11))
	if err != nil {
		panic(err)
	}
	printViews("③ 查看 3 月 9 日至 3 月 11 日（跨交接点）", views)

	// ④ 查看 3 月 10 日至 3 月 25 日：旧版实际区间止于 3 月 10 日，
	//    与范围仅在边界相接，不算命中；只有新版。
	views, err = book.ItemVersionsInRange("seat", day(10), day(25))
	if err != nil {
		panic(err)
	}
	printViews("④ 查看 3 月 10 日至 3 月 25 日（旧版仅边界相接）", views)

	// ⑤ 查看 3 月 20 日至 3 月 25 日：新版已结束、旧版不恢复，
	//    费率项存在但没有命中版本，返回空列表且不报错。
	views, err = book.ItemVersionsInRange("seat", day(20), day(25))
	fmt.Printf("⑤ 查看 3 月 20 日至 3 月 25 日（两版都已结束）：命中 %d 个版本 err=%v\n\n", len(views), err)

	// ⑥ 结束不晚于开始：一律返回可明确识别的范围错误，不返回版本列表。
	_, err = book.ItemVersionsInRange("seat", day(10), day(10))
	fmt.Printf("⑥ 结束等于开始：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidRange) = %v\n\n",
		errors.Is(err, tariff.ErrInvalidRange))

	// ⑦ 范围合法但费率项从未登记过版本：沿用现有的费率项未找到错误。
	_, err = book.ItemVersionsInRange("never-seen", day(9), day(11))
	fmt.Printf("⑦ 从未登记的费率项：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrItemNotFound) = %v\n\n",
		errors.Is(err, tariff.ErrItemNotFound))

	// ⑧ 返回的视图是独立副本：改写其中的结束时间不影响账本与后续查询。
	views, err = book.ItemVersionsInRange("seat", day(9), day(11))
	if err != nil {
		panic(err)
	}
	*views[0].End = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	*views[0].EffectiveEnd = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	again, err := book.ItemVersionsInRange("seat", day(9), day(11))
	if err != nil {
		panic(err)
	}
	fmt.Printf("⑧ 改写上次结果的结束时间后，再次查看 3 月 9 日至 3 月 11 日：\n")
	printViews("   再次查询（账本不受影响）", again)
}

func printViews(title string, views []tariff.VersionView) {
	fmt.Printf("%s：命中 %d 个版本\n", title, len(views))
	for _, v := range views {
		fmt.Printf("   %s/%s：单价=%d 分 开始=%s 登记结束=%s 实际有效区间=[%s, %s) 替代=%q 被替代=%q\n",
			v.ItemID, v.VersionID, v.UnitPrice,
			v.Start.Format("2006-01-02"), formatEnd(v.End),
			v.EffectiveStart.Format("2006-01-02"), formatEnd(v.EffectiveEnd),
			v.Replaces, v.SupersededBy)
	}
	fmt.Println()
}

func formatEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format("2006-01-02")
}
