# 本地费率版本与报价

`tariff` 是一个在本机运行的费率版本与报价账本（`tariff.Book`），支持：

- 按费率项登记带整数分单价、生效起点和结束时刻（不含）的费率版本；
- 登记一个**替代旧版本**的新版本，账本自动把旧版本的实际有效期截断到交接时刻；
- 登记替代版本时，新版本**自身的有效期**不能侵入同费率项其他版本的实际有效时间，否则返回 `tariff.ErrOverlap` 且整次登记不生效（见下文“登记替代版本”一节）；
- 在指定时刻对指定版本报价，得到**首次受理结果**（确认并给出单价、总价，或带原因拒绝）；
- 按请求标识进行幂等重试和事后查询——费率变化后可据此核对一笔报价应沿用原请求标识还是发起新报价。

## 测试

```bash
go test ./...
```

测试通过表示包可以加载且现有行为符合预期。

## 费率变化后如何核对一笔报价

### “报价被拒绝”和“调用返回错误”是两回事

`Quote` 的返回值是 `(tariff.Outcome, error)`：

- **`error` 非空**：调用本身不成立，例如相同请求标识此前提交过不同的请求内容（`tariff.ErrRequestIDConflict`）。此时没有可使用的报价结果。
- **`error` 为空但 `Outcome.Confirmed == false`**：报价已被受理、但被**拒绝**，拒绝原因在 `Outcome.Reason`（如 `version_expired`、`version_not_yet_effective`、`version_not_found`、`invalid_quantity`、`total_overflow`、`empty_request_id`）。

因此**不能仅凭 `err == nil` 判断成功**，必须检查 `Confirmed`。引用已失效版本是一次正常受理：账本返回一条未确认的报价结果并给出原因，而不是返回错误。

### 以保存的首次结果为准，不要按当前费率重算

- 非空请求标识的**首次**结果（无论确认还是拒绝）会被保存；之后相同标识、相同内容（费率项、版本、数量）的重试或 `Lookup` 查询都原样返回首次结果。
- 数量、版本来源（`Outcome.Request`）、单价（`UnitPrice`）和总价（`Total`）一律从首次结果读取。即使该版本后来被替代、单价变化，也**不能拿当前费率重新计算**——已确认记录永远保留确认时的值和首次受理时刻。
- 核对结论：`Lookup` 或原样重试取回的首次结果若 `Confirmed == true`，沿用原请求标识即可；若需要按新版本报价，必须使用一个**从未使用过的新标识**。

### 登记结束时间与实际有效结束时间可能不同

`ItemVersions` 同时返回两种边界：

- `End`：登记时填写的结束时刻（不含），永不因后续登记而改变；
- `EffectiveEnd`：**实际有效**结束时刻（不含）。当新版本通过 `Replaces` 替代旧版本时，旧版本的实际有效期被截断到新版本的生效时刻，因此 `EffectiveEnd` 可能早于登记的 `End`；旧版本若登记时未给结束时间，`End` 仍为 `nil`，而 `EffectiveEnd` 会显示交接点。

从交接时刻起，旧版本不能再接受新报价（新请求得到 `version_expired` 拒绝），但交接前已经确认的记录不受影响，仍保留原版本、数量、单价、总价和首次受理时刻。旧版本也不会在新版本到期后恢复生效。

### 首次拒绝同样幂等

对于非空标识，首次拒绝也会保存，可被 `Lookup` 查到：

- 若首次因**版本尚未生效**（`version_not_yet_effective`）被拒绝，即使之后版本已生效，用原标识原样重试仍返回那次首次拒绝（含首次受理时刻）。要按已生效的版本重新报价，必须换用新标识。
- 空标识的请求不保存（原因 `empty_request_id`），`Lookup("")` 返回未找到。

### 冲突与未找到

- 相同请求标识但改动了**费率项、版本或数量**：`Quote` 返回 `tariff.ErrRequestIDConflict`，原记录不被覆盖。
- 查询从未受理过的标识：`Lookup` 返回 `tariff.ErrRequestNotFound`。

### 结果只在当前账本实例内

报价记录保存在当前 `Book` 实例的内存中。重新 `NewBook()` 得到的是一本空账本，之前的标识全部查不到——不要假设跨实例仍能查询历史结果。

## 登记替代版本：一次失败、修正后成功

准备登记一个替代旧版本的新版本时，要特别注意：**旧版本可以被截短，并不代表新版本可以占用同一费率项其他版本的有效时间。**

重叠校验针对的是账本里**除被替代者之外**的所有版本，比较依据是各方的实际有效区间。新版本登记时若不填结束时间，就表示它持续有效——这会一直延伸到同费率项里排在后面的版本区间内。登记失败时账本不做任何部分提交，调用方应当：

1. 检查 `RegisterVersion` 返回的错误（用 `errors.Is(err, tariff.ErrOverlap)` 判断），**返回错误即说明登记没有生效**；
2. 通过 `ItemVersions` 查询确认账本状态没有变化（没有新版本、没有新的替代关系、旧版本的两种结束时间都未改变）；
3. 修正请求后**沿用同一个版本标识**重新登记——失败的登记不会占用版本标识。
4. 半开区间在同一时刻相接（前者结束时刻 = 后者开始时刻）不算重叠，因为结束时刻不含。

下面的完整程序位于 [`examples/register_alt/main.go`](examples/register_alt/main.go)，可直接运行：

```bash
go run ./examples/register_alt
```

时间线（同一费率项 `seat`，下述日期均指 UTC 零点，结束时刻不含）：先登记 `seat-v1`，单价 150 分，2026-03-01 生效、登记结束 3 月 31 日；再登记 `seat-v3`，单价 200 分，2026-04-01 起持续有效。然后尝试登记 `seat-v2`，单价 180 分，3 月 10 日起替代 v1 但**不填结束时间**——它会延续到 v3 的有效时间内，返回 `ErrOverlap`。把结束时间改为 4 月 1 日后沿用 `seat-v2` 标识再次登记，即成功。最后把报价受理时刻设为 3 月 10 日，用各自的新请求标识、数量 4 分别引用 v1 和 v2。

```go
// 命令 register_alt 演示“登记替代费率版本”时一次失败、修正后成功的完整过程，
// 以及登记返回错误与报价受理后被拒绝之间的区别。
//
// 运行：
//
//	go run ./examples/register_alt
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

func main() {
	// 下述日期均指 UTC 零点，结束时刻不含。
	// 演示时钟：报价受理时刻从变量 now 读取，因此无论哪天运行，输出都确定、可复现。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now。
	var now time.Time
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	v1Start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	v1End := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	v2Start := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	v2End := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	v3Start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// ① 先登记 seat-v1：单价 150 分，2026-03-01 生效，登记结束为 2026-03-31（不含）。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     v1Start,
		End:       &v1End,
	}); err != nil {
		panic(err)
	}
	fmt.Println("① 登记 seat-v1：150 分，2026-03-01 起，登记结束 2026-03-31（不含）：成功")

	// ② 再登记 seat-v3：单价 200 分，2026-04-01 起持续有效（不填结束时间）。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
	}); err != nil {
		panic(err)
	}
	fmt.Println("② 登记 seat-v3：200 分，2026-04-01 起持续有效（不填结束）：成功")

	// ③ 尝试登记 seat-v2：单价 180 分，2026-03-10 起替代 seat-v1，但不填结束时间。
	//    seat-v1 虽然会被截短到 3 月 10 日，seat-v2 本身却会持续有效，
	//    延伸进 seat-v3 自 4 月 1 日起的有效时间，因此与同费率项的另一个版本重叠。
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "seat-v1",
		// End 故意留空：nil 表示持续有效
	})
	fmt.Printf("③ seat-v2（180 分，3 月 10 日起替代 v1，不填结束）登记结果：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrOverlap)=%v —— 返回错误即说明本次登记没有生效\n",
		errors.Is(err, tariff.ErrOverlap))

	// ④ 失败后立即查询：账本里仍只有 v1、v3 两个版本。
	//    v1 的登记结束仍是 3 月 31 日、实际结束也仍是 3 月 31 日，
	//    不存在被 v2 替代的关系；v3 完全不受影响。
	printViews("④ 登记失败后查询 seat 的版本（应仍只有两个，且无任何替代关系）", book)

	// ⑤ 沿用 seat-v2 这个版本标识，把结束时间改为 2026-04-01（不含）再次登记。
	//    失败的登记不占用版本标识；v2 的结束时刻与 v3 的开始时刻同为 4 月 1 日零点，
	//    两端半开区间在这一点相接，互不重叠。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2End,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	fmt.Printf("⑤ 沿用 seat-v2 标识、补上结束 %s（不含）再次登记：成功（err=<nil>）\n",
		v2End.Format(time.RFC3339))

	// ⑥ 成功后查询：注意 v1 的“登记结束”仍是 3 月 31 日（永不改变），
	//    而“实际有效结束”被截短到 3 月 10 日；v1 被 v2 替代，v2 替代 v1。
	printViews("⑥ 登记成功后查询 seat 的版本（三个版本，v1 已被截短并与 v2 相接）", book)

	// 报价受理时刻设为 2026-03-10 00:00（UTC 零点）：
	// 这正是 v1 实际有效区间的结束点（不含），也是 v2 有效区间的起点（含）。
	now = v2Start

	// ⑦ 用全新请求标识、数量 4 引用 seat-v1。调用 err 为 nil（报价已受理），
	//    但 Confirmed=false、原因 version_expired —— 受理后拒绝，不是调用错误。
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v1-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑦ 3 月 10 日用新标识引用 seat-v1（quote-v1-at-handoff，数量 4）", expired)
	fmt.Printf("   err==nil 但 Confirmed=%v、原因=%s —— 这是“受理后拒绝”，与③的登记返回错误不是一回事\n",
		expired.Confirmed, expired.Reason)

	// ⑧ 另一个全新请求标识、数量 4 引用 seat-v2：按 180 分确认，总价 720 分。
	confirmed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑧ 3 月 10 日用新标识引用 seat-v2（quote-v2-at-handoff，数量 4）", confirmed)
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}

func printViews(title string, b *tariff.Book) {
	fmt.Printf("%s：\n", title)
	views, err := b.ItemVersions("seat")
	if err != nil {
		panic(err)
	}
	fmt.Printf("   版本数量=%d\n", len(views))
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 分 生效=%s 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice, v.Start.Format(time.RFC3339),
			fmtEnd(v.End), fmtEnd(v.EffectiveEnd), v.Replaces, v.SupersededBy)
	}
}

func printOutcome(title string, o tariff.Outcome) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求 item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	if o.Confirmed {
		fmt.Printf("   结果=已确认 单价=%d 分 总价=%d 分\n", o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 原因=%s（err 为 nil，拒绝不是调用错误）\n", o.Reason)
	}
}
```

输出（受理时刻由示例时钟设定，不依赖运行当天的日期）：

```text
① 登记 seat-v1：150 分，2026-03-01 起，登记结束 2026-03-31（不含）：成功
② 登记 seat-v3：200 分，2026-04-01 起持续有效（不填结束）：成功
③ seat-v2（180 分，3 月 10 日起替代 v1，不填结束）登记结果：err=tariff: version interval overlaps an existing version of the item
   errors.Is(err, tariff.ErrOverlap)=true —— 返回错误即说明本次登记没有生效
④ 登记失败后查询 seat 的版本（应仍只有两个，且无任何替代关系）：
   版本数量=2
   seat-v1：单价=150 分 生效=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-31T00:00:00Z 替代="" 被替代=""
   seat-v3：单价=200 分 生效=2026-04-01T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="" 被替代=""
⑤ 沿用 seat-v2 标识、补上结束 2026-04-01T00:00:00Z（不含）再次登记：成功（err=<nil>）
⑥ 登记成功后查询 seat 的版本（三个版本，v1 已被截短并与 v2 相接）：
   版本数量=3
   seat-v1：单价=150 分 生效=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=180 分 生效=2026-03-10T00:00:00Z 登记结束=2026-04-01T00:00:00Z 实际有效结束=2026-04-01T00:00:00Z 替代="seat-v1" 被替代=""
   seat-v3：单价=200 分 生效=2026-04-01T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="" 被替代=""
⑦ 3 月 10 日用新标识引用 seat-v1（quote-v1-at-handoff，数量 4）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-10T00:00:00Z
   结果=被拒绝 原因=version_expired（err 为 nil，拒绝不是调用错误）
   err==nil 但 Confirmed=false、原因=version_expired —— 这是“受理后拒绝”，与③的登记返回错误不是一回事
⑧ 3 月 10 日用新标识引用 seat-v2（quote-v2-at-handoff，数量 4）：
   请求 item=seat version=seat-v2 数量=4；首次受理时刻=2026-03-10T00:00:00Z
   结果=已确认 单价=180 分 总价=720 分
```

对照输出可确认三件事：

- **如何判断登记是否生效**：③的 `RegisterVersion` 返回非空错误（`ErrOverlap`），④的查询里没有 `seat-v2`、v1 的两种结束时间都仍是 3 月 31 日且没有替代关系——错误返回加查询未变，二者一致才说明失败被正确识别；⑤返回 `err=<nil>` 且⑥出现三个版本，才是登记生效。
- **登记结束 ≠ 实际有效结束**：⑥里 v1 的登记结束仍为 3 月 31 日（登记时填写的值，永不改变），实际有效结束则是 3 月 10 日（被 v2 截断的交接点），同时 v1 显示被 `seat-v2` 替代、v2 显示替代 `seat-v1`。这两个值不能合并理解。
- **登记错误 ≠ 报价拒绝**：③是登记阶段返回错误，账本无变更；⑦是报价已受理（`err == nil`）后因 `version_expired` 拒绝（`Confirmed=false`），二者不能混为一谈——报价成功必须同时满足 `err == nil` **且** `Confirmed == true`，如⑧按 180 分确认总价 720 分。

## 完整示例

下面的程序围绕同一费率项 `seat` 演示完整核对过程，代码位于
[`examples/reconcile/main.go`](examples/reconcile/main.go)，可直接运行：

```bash
go run ./examples/reconcile
```

示例通过公开选项 `tariff.WithClock` 注入一个可手动推进的时钟，因此输出确定、可复现：读者无需等待真实时间流逝，也不必自行编写时钟或补齐初始化代码。生产环境直接 `tariff.NewBook()` 即使用真实时间。

时间线：旧版本 `seat-v1` 单价 150 分，3 月 2 日在有效期内以数量 4 报价并确认总价 600 分；随后登记 3 月 10 日 00:00 起替代它的 `seat-v2`，并把受理时刻推进到交接点。

```go
// 命令 reconcile 是“费率变化后如何核对一笔报价”的完整可运行示例。
//
// 运行：
//
//	go run ./examples/reconcile
package main

import (
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// 登记旧版本 seat-v1：单价 150 分/单位，
	// 登记的有效期为 2026-03-01 至 2026-03-31（结束时刻不含）。
	oldEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		End:       &oldEnd,
	}))

	// 2026-03-02 10:00，旧版本有效期内，用标识 quote-001 报价，数量 4。
	now = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	first, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	must(err)
	printOutcome("① 有效期内首次报价（quote-001）", first)

	// 登记新版本 seat-v2：单价 180，2026-03-10 00:00 起替代 seat-v1。
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
		Replaces:  "seat-v1",
	}))
	fmt.Println("② 新版本登记后的版本视图：")
	views, err := book.ItemVersions("seat")
	must(err)
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice, fmtEnd(v.End), fmtEnd(v.EffectiveEnd),
			v.Replaces, v.SupersededBy)
	}

	// 将受理时刻推进到交接点本身（有效区间结束时刻不含该点）。
	now = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	got, err := book.Lookup("quote-001")
	must(err)
	printOutcome("③ 交接点按原标识 Lookup（quote-001）", got)
	fmt.Printf("   与首次结果完全一致：%v\n", got == first)

	replay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	must(err)
	printOutcome("④ 交接点原样重试（quote-001）", replay)
	fmt.Printf("   与首次结果完全一致：%v\n", replay == first)

	fresh, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-002",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑤ 交接点用新标识引用旧版本（quote-002）", fresh)
	fmt.Printf("   本次返回 err==nil 但 Confirmed=%v，拒绝原因=%s，不能当作成功\n",
		fresh.Confirmed, fresh.Reason)

	// 再登记 seat-v3：2026-04-01 起替代 seat-v2，用于演示“版本尚未生效”的首次拒绝。
	must(book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Replaces:  "seat-v2",
	}))

	// 3 月 20 日：v3 尚未生效。非空标识的首次拒绝也会被保存。
	now = time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)
	early, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-003",
		ItemID:    "seat",
		VersionID: "seat-v3",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑥ v3 生效前首次报价（quote-003）", early)

	// 推进到 4 月 2 日：v3 已生效，但原样重试仍返回 3 月 20 日的首次拒绝。
	now = time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC)
	earlyReplay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-003",
		ItemID:    "seat",
		VersionID: "seat-v3",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑦ v3 生效后原样重试（quote-003）", earlyReplay)
	fmt.Printf("   与首次拒绝完全一致：%v（受理时刻仍是 %s）\n",
		earlyReplay == early, earlyReplay.AcceptedAt.Format(time.RFC3339))

	again, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-004",
		ItemID:    "seat",
		VersionID: "seat-v3",
		Quantity:  4,
	})
	must(err)
	printOutcome("⑧ 改用新标识按 v3 重新报价（quote-004）", again)

	// 相同标识改动数量（改动费率项或版本同理）→ 请求内容冲突，原记录不被覆盖。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  5,
	})
	fmt.Printf("⑨ 相同标识改数量：err=%v\n", err)
	unchanged, err := book.Lookup("quote-001")
	must(err)
	fmt.Printf("   原记录未被覆盖：Confirmed=%v 数量=%d 单价=%d 总价=%d\n",
		unchanged.Confirmed, unchanged.Request.Quantity, unchanged.UnitPrice, unchanged.Total)

	// 从未受理过的标识 → 未找到，这是 err 非空的情况。
	_, err = book.Lookup("quote-999")
	fmt.Printf("⑩ 查询从未受理的标识：err=%v\n", err)

	// 空标识：返回拒绝结果且 err 为 nil，但该拒绝不会保存。
	empty, err := book.Quote(tariff.QuoteRequest{ItemID: "seat", VersionID: "seat-v2", Quantity: 1})
	must(err)
	fmt.Printf("⑪ 空标识报价：Confirmed=%v 原因=%s err=%v\n", empty.Confirmed, empty.Reason, err)
	_, err = book.Lookup("")
	fmt.Printf("   空标识查询：err=%v（空标识的拒绝未保存）\n", err)

	// 报价结果只保存在当前账本实例内。
	freshBook := tariff.NewBook()
	_, err = freshBook.Lookup("quote-001")
	fmt.Printf("⑫ 新建账本查询 quote-001：err=%v\n", err)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func fmtEnd(t *time.Time) string {
	if t == nil {
		return "无（持续有效）"
	}
	return t.Format(time.RFC3339)
}

func printOutcome(title string, o tariff.Outcome) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求 item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	if o.Confirmed {
		fmt.Printf("   结果=已确认 单价=%d 分 总价=%d 分\n", o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 原因=%s（err 为 nil，拒绝不是调用错误）\n", o.Reason)
	}
}
```

输出（时间为示例时钟推进到的确定时刻，不依赖真实时间）：

```text
① 有效期内首次报价（quote-001）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-02T10:00:00Z
   结果=已确认 单价=150 分 总价=600 分
② 新版本登记后的版本视图：
   seat-v1：单价=150 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=180 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="seat-v1" 被替代=""
③ 交接点按原标识 Lookup（quote-001）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-02T10:00:00Z
   结果=已确认 单价=150 分 总价=600 分
   与首次结果完全一致：true
④ 交接点原样重试（quote-001）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-02T10:00:00Z
   结果=已确认 单价=150 分 总价=600 分
   与首次结果完全一致：true
⑤ 交接点用新标识引用旧版本（quote-002）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-10T00:00:00Z
   结果=被拒绝 原因=version_expired（err 为 nil，拒绝不是调用错误）
   本次返回 err==nil 但 Confirmed=false，拒绝原因=version_expired，不能当作成功
⑥ v3 生效前首次报价（quote-003）：
   请求 item=seat version=seat-v3 数量=4；首次受理时刻=2026-03-20T09:00:00Z
   结果=被拒绝 原因=version_not_yet_effective（err 为 nil，拒绝不是调用错误）
⑦ v3 生效后原样重试（quote-003）：
   请求 item=seat version=seat-v3 数量=4；首次受理时刻=2026-03-20T09:00:00Z
   结果=被拒绝 原因=version_not_yet_effective（err 为 nil，拒绝不是调用错误）
   与首次拒绝完全一致：true（受理时刻仍是 2026-03-20T09:00:00Z）
⑧ 改用新标识按 v3 重新报价（quote-004）：
   请求 item=seat version=seat-v3 数量=4；首次受理时刻=2026-04-02T09:00:00Z
   结果=已确认 单价=200 分 总价=800 分
⑨ 相同标识改数量：err=tariff: request id already used with different content
   原记录未被覆盖：Confirmed=true 数量=4 单价=150 总价=600
⑩ 查询从未受理的标识：err=tariff: request id not found
⑪ 空标识报价：Confirmed=false 原因=empty_request_id err=<nil>
   空标识查询：err=tariff: request id not found（空标识的拒绝未保存）
⑫ 新建账本查询 quote-001：err=tariff: request id not found
```

对照输出即可区分两类信息：`Lookup`/原样重试取回的是**历史报价结果**（③④仍是 3 月 2 日确认的 600 分），而用新标识当场报价检验的是**当前版本是否有效**（⑤旧版本已失效、⑧新版本有效）。
