# 本地费率版本与报价

`tariff` 是一个在本机运行的费率版本与报价账本：登记费率项的各个版本（含生效时间与版本替代关系），按请求标识受理报价，并保存每笔报价的**首次受理结果**供后续核对。账本可并发使用，结果只保存在当前账本实例内。

本文介绍费率变化后如何核对一笔报价：调用方如何通过现有 Go 包查询结果，判断应当沿用原请求标识取回历史结果，还是发起一笔新报价。

## 使用

```bash
go test ./...
```

测试通过表示基线包可以加载。随测试一起编译运行的还有 `tariff/example_test.go` 中的两个可运行示例，下文的代码与输出即来自它们；修改示例后运行上述命令即可校验文字输出是否仍与实际行为一致。

## 核心概念

- **版本与实际有效区间**：`RegisterVersion` 登记版本时填写的 `End` 是“登记的结束时间”；当新版本通过 `Replaces` 替代旧版本时，旧版本的**实际有效结束时间**会被截断到新版本的生效时刻（交接点）。两者可能不同：旧版本登记的 `End` 保持原值，`EffectiveEnd` 才是实际不再接受新报价的时刻。被替代的旧版本不会在新版本到期后恢复生效。
- **报价结果（`Outcome`）不是错误**：`Quote` 正常受理时，即使报价被拒绝，返回的 `err` 也是 `nil`。**不能仅凭错误为空就视为成功**，必须检查 `out.Confirmed`；被拒绝时原因在 `out.Reason`（如 `version_expired`、`version_not_yet_effective`）。只有标识冲突这类调用问题才会以非空 `err` 返回。
- **首次结果不可变**：非空请求标识的首次结果（确认或拒绝）都会保存。数量、版本来源和确认金额必须从保存的首次结果（`Lookup` 或原样重试的返回值）读取，**不能拿当前费率重新计算**。交接时刻起旧版本不能接受新报价，但已经确认的记录仍保留原版本、数量、单价、总价和首次受理时刻。
- **幂等重试**：相同标识且费率项、版本、数量完全相同的重试，原样返回首次结果（包括首次的拒绝）；任一字段不同则返回 `tariff: request id already used with different content`，且原记录不被覆盖。
- **核对入口**：`Lookup(requestID)` 只查询、不产生新受理；`Quote` 用原标识原样提交是重试。两者都能取回首次结果。若需要按当前费率重新报价，必须使用一个从未用过的新标识。

## 完整示例：费率变化后核对一笔报价

场景：费率项 `seat` 的旧版本 `v1` 单价 150 分，登记有效期到 2027-01-01。2026-02-01 旧版本有效期间以标识 `q-seat-0001` 报价 4 个，确认 600 分。随后登记从 2026-03-01 起替代 `v1` 的新版本 `v2`（180 分），并把受理时刻推进到交接点。示例通过 `WithClock` 直接指定受理时刻，**不需要等待真实时间流逝，也不需要另外编写时钟**：

```go
package tariff_test

import (
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

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

func printOutcome(tag string, o tariff.Outcome, err error) {
	if err != nil {
		fmt.Printf("%s: err=%v\n", tag, err)
		return
	}
	fmt.Printf("%s: err=<nil> confirmed=%t item=%s version=%s quantity=%d unitPrice=%d total=%d acceptedAt=%s reason=%q\n",
		tag, o.Confirmed, o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.UnitPrice, o.Total, o.AcceptedAt.Format(time.RFC3339), o.Reason)
}

func ExampleBook_reconcileAfterRateChange() {
	// 受理时刻由变量 now 决定，可直接跳到任意时间点，无需等待真实时间。
	var now time.Time
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	utc := time.UTC
	registeredEnd := time.Date(2027, 1, 1, 0, 0, 0, 0, utc) // 登记时填写的结束时刻
	handoff := time.Date(2026, 3, 1, 0, 0, 0, 0, utc)       // 新版本生效、替代旧版本的交接点

	// 登记旧版本 seat/v1：单价 150 分，登记的有效期到 2027-01-01。
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v1",
		UnitPrice: 150,
		Start:     time.Date(2026, 1, 1, 0, 0, 0, 0, utc),
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
}
```

输出：

```text
首次报价: err=<nil> confirmed=true item=seat version=v1 quantity=4 unitPrice=150 total=600 acceptedAt=2026-02-01T09:00:00Z reason=""
版本 v1：unitPrice=150 登记结束=2027-01-01T00:00:00Z 实际有效结束=2026-03-01T00:00:00Z supersededBy="v2"
版本 v2：unitPrice=180 登记结束=无（持续有效） 实际有效结束=无（持续有效） supersededBy=""
交接点按原标识查询: err=<nil> confirmed=true item=seat version=v1 quantity=4 unitPrice=150 total=600 acceptedAt=2026-02-01T09:00:00Z reason=""
交接点原样重试: err=<nil> confirmed=true item=seat version=v1 quantity=4 unitPrice=150 total=600 acceptedAt=2026-02-01T09:00:00Z reason=""
新标识引用旧版本: err=<nil> confirmed=false item=seat version=v1 quantity=4 unitPrice=0 total=0 acceptedAt=2026-03-01T00:00:00Z reason="version_expired"
```

如何据此核对：

- **沿用原标识**：原请求已确认（`Confirmed == true`）时，费率变化不影响该笔交易。用 `Lookup` 查询或用相同内容原样 `Quote` 重试，取回的仍是首次结果——`v1`、数量 4、单价 150、总价 600、受理时刻 2026-02-01T09:00:00Z。对账金额以这份记录为准，不要用 v2 的 180 分重新乘算。
- **发起新报价**：原请求被拒绝（`Confirmed == false`），或业务确实需要按当前有效版本重新计费时，使用一个**新的请求标识**调用 `Quote`。如上所示，在交接点用新标识引用 `v1` 会得到 `err == nil` 但 `Confirmed == false`、原因为 `version_expired` 的未确认结果；需要报价时应引用当前有效的 `v2`。

## 首次拒绝的保存、内容冲突与未找到

下面几种情况直接影响核对结论，均由同一账本实例演示：

```go
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
		RequestID: "q-early", ItemID: "seat", VersionID: "v3", Quantity: 1,
	})
	printOutcome("生效前首次报价", early, err)

	// 把时刻推进到生效之后，原样重试：返回的仍是首次拒绝，不会改判。
	now = v3Start
	replay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "q-early", ItemID: "seat", VersionID: "v3", Quantity: 1,
	})
	printOutcome("生效后原样重试", replay, err)

	// 要按当前费率重新报价，必须换一个新标识；这次得到确认。
	fresh, err := book.Quote(tariff.QuoteRequest{
		RequestID: "q-fresh", ItemID: "seat", VersionID: "v3", Quantity: 1,
	})
	printOutcome("新标识重新报价", fresh, err)

	// 相同标识但改动数量（改动费率项或版本同理）返回冲突错误，原记录不被覆盖。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "q-fresh", ItemID: "seat", VersionID: "v3", Quantity: 2,
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
}
```

输出：

```text
生效前首次报价: err=<nil> confirmed=false item=seat version=v3 quantity=1 unitPrice=0 total=0 acceptedAt=2026-05-01T00:00:00Z reason="version_not_yet_effective"
生效后原样重试: err=<nil> confirmed=false item=seat version=v3 quantity=1 unitPrice=0 total=0 acceptedAt=2026-05-01T00:00:00Z reason="version_not_yet_effective"
新标识重新报价: err=<nil> confirmed=true item=seat version=v3 quantity=1 unitPrice=200 total=200 acceptedAt=2026-06-01T00:00:00Z reason=""
相同标识改动数量: err=tariff: request id already used with different content
冲突后查询原标识: err=<nil> confirmed=true item=seat version=v3 quantity=1 unitPrice=200 total=200 acceptedAt=2026-06-01T00:00:00Z reason=""
未知标识查询: err=tariff: request id not found
新建账本查询旧标识: err=tariff: request id not found
```

要点：

- **首次拒绝也会保存**（要求请求标识非空）。若首次因“版本尚未生效”被拒绝，即使后来版本已生效，用原标识原样重试仍返回首次拒绝（受理时刻也是首次的时刻）；要重新报价必须使用新标识。
- **相同标识改动内容即冲突**：费率项、版本或数量任一不同，`Quote` 返回 `tariff: request id already used with different content`，原记录不会被覆盖，随后仍可按原标识查到首次结果。
- **查询从未受理的标识**返回 `tariff: request id not found`——这同样是非空错误，与“受理过但被拒绝”（`err` 为 `nil`、`Confirmed` 为 `false`）要区分开。
- **结果只存在于当前账本实例内**：重新 `NewBook()` 后无法查询旧实例的记录，不要误以为账本重建后历史报价仍在。需要跨进程保留时，应由调用方自行持久化首次 `Outcome`。

## 拒绝原因一览

`Outcome.Reason` 的可能取值：`empty_request_id`、`invalid_quantity`、`version_not_found`、`version_not_yet_effective`、`version_expired`、`total_overflow`；确认时为空字符串。注意空请求标识的拒绝不会保存，也无法通过 `Lookup` 查到。
