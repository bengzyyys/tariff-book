package tariff_test

import (
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// must 用于示例中的登记步骤，使示例不依赖 testing 包即可被读者整段复用。
func must(err error) {
	if err != nil {
		panic(err)
	}
}

func endText(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}

// printOutcome 统一打印一次报价/查询调用的错误与受理结果。
// 注意 err 与 Confirmed 是两个独立的判断维度。
func printOutcome(tag string, o tariff.Outcome, err error) {
	if err != nil {
		fmt.Printf("%s: err=%v\n", tag, err)
		return
	}
	fmt.Printf("%s: err=<nil> confirmed=%t item=%s version=%s quantity=%d unitPrice=%d total=%d acceptedAt=%s reason=%q\n",
		tag, o.Confirmed, o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.UnitPrice, o.Total, o.AcceptedAt.Format(time.RFC3339), o.Reason)
}

// ExampleBook_reconcileAfterRateChange 演示费率变化后如何核对一笔报价：
// 旧版本有效期内确认的报价，在交接点之后仍可按原标识查询或原样重试取回；
// 而用新标识再次引用已失效的旧版本，得到的是“调用成功但报价被拒绝”。
func ExampleBook_reconcileAfterRateChange() {
	// 账本的受理时刻由变量 now 决定，示例可以直接跳到任意时间点，无需等待真实时间。
	var now time.Time
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	utc := time.UTC
	oldStart := time.Date(2026, 1, 1, 0, 0, 0, 0, utc)
	registeredEnd := time.Date(2027, 1, 1, 0, 0, 0, 0, utc) // 登记时填写的结束时刻
	handoff := time.Date(2026, 3, 1, 0, 0, 0, 0, utc)       // 新版本生效、替代旧版本的交接点

	// 登记旧版本 seat/v1：单价 150 分，登记的有效期到 2027-01-01。
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v1",
		UnitPrice: 150,
		Start:     oldStart,
		End:       &registeredEnd,
	}))

	// 2026-02-01 09:00，旧版本有效期间，用标识 q-seat-0001 报价 4 个，确认 600 分。
	now = time.Date(2026, 2, 1, 9, 0, 0, 0, utc)
	first, err := book.Quote(tariff.QuoteRequest{
		RequestID: "q-seat-0001",
		ItemID:    "seat",
		VersionID: "v1",
		Quantity:  4,
	})
	printOutcome("首次报价", first, err)

	// 登记新版本 seat/v2：单价 180 分，2026-03-01 00:00 起替代 v1。
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v2",
		UnitPrice: 180,
		Start:     handoff,
		Replaces:  "v1",
	}))

	// 版本视图显示：v1 的“登记结束”仍是 2027-01-01，
	// 但“实际有效结束”已因被替代而截断到交接点 2026-03-01。
	views, err := book.ItemVersions("seat")
	must(err)
	for _, v := range views {
		fmt.Printf("版本 %s：unitPrice=%d 登记结束=%s 实际有效结束=%s supersededBy=%q\n",
			v.VersionID, v.UnitPrice, endText(v.End), endText(v.EffectiveEnd), v.SupersededBy)
	}

	// 把受理时刻推进到交接点（结束边界不含，此刻起 v1 不再接受新报价）。
	now = handoff

	// 核对方式一：按原标识查询，取回保存的首次确认结果。
	looked, err := book.Lookup("q-seat-0001")
	printOutcome("交接点按原标识查询", looked, err)

	// 核对方式二：用完全相同的内容原样重试，返回的仍是首次结果，
	// 版本、数量、单价、总价和首次受理时刻都不按当前费率重算。
	replay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "q-seat-0001",
		ItemID:    "seat",
		VersionID: "v1",
		Quantity:  4,
	})
	printOutcome("交接点原样重试", replay, err)

	// 换一个从未使用过的新标识再次引用旧版本：
	// 调用没有返回错误（err 为 <nil>），但报价未被确认，原因是版本已失效。
	oldAgain, err := book.Quote(tariff.QuoteRequest{
		RequestID: "q-seat-0002",
		ItemID:    "seat",
		VersionID: "v1",
		Quantity:  4,
	})
	printOutcome("新标识引用旧版本", oldAgain, err)

	// Output:
	// 首次报价: err=<nil> confirmed=true item=seat version=v1 quantity=4 unitPrice=150 total=600 acceptedAt=2026-02-01T09:00:00Z reason=""
	// 版本 v1：unitPrice=150 登记结束=2027-01-01T00:00:00Z 实际有效结束=2026-03-01T00:00:00Z supersededBy="v2"
	// 版本 v2：unitPrice=180 登记结束=无（持续有效） 实际有效结束=无（持续有效） supersededBy=""
	// 交接点按原标识查询: err=<nil> confirmed=true item=seat version=v1 quantity=4 unitPrice=150 total=600 acceptedAt=2026-02-01T09:00:00Z reason=""
	// 交接点原样重试: err=<nil> confirmed=true item=seat version=v1 quantity=4 unitPrice=150 total=600 acceptedAt=2026-02-01T09:00:00Z reason=""
	// 新标识引用旧版本: err=<nil> confirmed=false item=seat version=v1 quantity=4 unitPrice=0 total=0 acceptedAt=2026-03-01T00:00:00Z reason="version_expired"
}

// ExampleBook_rejectionConflictAndMissingID 演示另外三种直接影响核对结果的情况：
// 首次拒绝也会被保存并在重试中原样返回；相同标识改动内容产生冲突且不覆盖原记录；
// 未受理过的标识查不到，且记录只存在于当前账本实例内。
func ExampleBook_rejectionConflictAndMissingID() {
	var now time.Time
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))
	utc := time.UTC

	v3Start := time.Date(2026, 6, 1, 0, 0, 0, 0, utc)
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v3",
		UnitPrice: 200,
		Start:     v3Start,
	}))

	// 版本生效之前首次报价：调用成功，但因尚未生效被拒绝；拒绝结果同样按标识保存。
	now = time.Date(2026, 5, 1, 0, 0, 0, 0, utc)
	early, err := book.Quote(tariff.QuoteRequest{
		RequestID: "q-early",
		ItemID:    "seat",
		VersionID: "v3",
		Quantity:  1,
	})
	printOutcome("生效前首次报价", early, err)

	// 把时刻推进到生效之后，原样重试：返回的仍是首次拒绝，不会改判。
	now = v3Start
	replay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "q-early",
		ItemID:    "seat",
		VersionID: "v3",
		Quantity:  1,
	})
	printOutcome("生效后原样重试", replay, err)

	// 要按当前费率重新报价，必须换一个新标识；这次得到确认。
	fresh, err := book.Quote(tariff.QuoteRequest{
		RequestID: "q-fresh",
		ItemID:    "seat",
		VersionID: "v3",
		Quantity:  1,
	})
	printOutcome("新标识重新报价", fresh, err)

	// 相同标识但改动数量（改动费率项或版本同理）返回冲突错误，原记录不被覆盖。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "q-fresh",
		ItemID:    "seat",
		VersionID: "v3",
		Quantity:  2,
	})
	fmt.Printf("相同标识改动数量: err=%v\n", err)
	kept, err := book.Lookup("q-fresh")
	printOutcome("冲突后查询原标识", kept, err)

	// 查询从未受理过的标识返回 ErrRequestNotFound。
	_, err = book.Lookup("q-never-seen")
	fmt.Printf("未知标识查询: err=%v\n", err)

	// 结果只保存在当前账本实例内：重新创建账本后，历史记录无法继续查询。
	_, err = tariff.NewBook().Lookup("q-fresh")
	fmt.Printf("新建账本查询旧标识: err=%v\n", err)

	// Output:
	// 生效前首次报价: err=<nil> confirmed=false item=seat version=v3 quantity=1 unitPrice=0 total=0 acceptedAt=2026-05-01T00:00:00Z reason="version_not_yet_effective"
	// 生效后原样重试: err=<nil> confirmed=false item=seat version=v3 quantity=1 unitPrice=0 total=0 acceptedAt=2026-05-01T00:00:00Z reason="version_not_yet_effective"
	// 新标识重新报价: err=<nil> confirmed=true item=seat version=v3 quantity=1 unitPrice=200 total=200 acceptedAt=2026-06-01T00:00:00Z reason=""
	// 相同标识改动数量: err=tariff: request id already used with different content
	// 冲突后查询原标识: err=<nil> confirmed=true item=seat version=v3 quantity=1 unitPrice=200 total=200 acceptedAt=2026-06-01T00:00:00Z reason=""
	// 未知标识查询: err=tariff: request id not found
	// 新建账本查询旧标识: err=tariff: request id not found
}
