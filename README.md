# 本地费率版本与报价

`tariff` 是一个在本机运行的费率版本与报价账本（`tariff.Book`），支持：

- 按费率项登记带整数分单价、生效起点和结束时刻（不含）的费率版本；
- 登记一个**替代旧版本**的新版本，账本自动把旧版本的实际有效期截断到交接时刻；
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

## 登记替代版本：旧版能被截短，不代表新版能占用其他版本的时间

上文讲的是费率变化后如何核对报价；准备**登记替代版本**时还需要知道重叠是怎么判定的：

- 新版本的实际有效区间 `[Start, End)`（`End` 为 `nil` 即持续有效）必须与**同一费率项下其他每个版本的实际有效区间**都不重叠。
- `Replaces` 只会让**被替代的那一个旧版本**从新版本的 `Start` 处截断，被替代版本不参与本次重叠判断；同项的**其他版本照常参与**。旧版本可以被截短，并不代表新版本能够顺延占用同一费率项其他版本的有效时间。
- 区间按半开区间比较：一个版本的结束时刻与另一个版本的开始时刻落在同一瞬间属于**相接，不算重叠**（结束时刻不含在有效期内）。
- 任何校验失败（如 `tariff.ErrOverlap`）都会整次回滚：新版本不入库、版本标识不被占用、既有边界和替代关系都不变。**登记是否生效只看返回的 `error` 是否为 `nil`**，可用 `errors.Is(err, tariff.ErrOverlap)` 判别原因，再用 `ItemVersions` 核对账本状态。

还要把两类“没成功”分开：登记失败是**调用本身返回错误**；而报价引用已失效版本时调用的 `err` 为 `nil`，只是受理结果 `Confirmed == false`、`Reason == version_expired`。不能只用调用是否返回错误来判断一笔报价是否成功。

### 一次登记失败、修正后成功的完整示例

程序位于 [`examples/replacement/main.go`](examples/replacement/main.go)，可直接运行：

```bash
go run ./examples/replacement
```

所有日期均为 UTC 零点、结束时刻不含；时间与费率数据全部在代码中给出，并通过公开选项 `tariff.WithClock` 注入可手动推进的时钟，因此无论在哪一天运行，输出都确定、可复现。生产环境直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一费率项 `seat`）：

1. `seat-v1`：单价 150 分，2026-03-01 生效，登记结束为 2026-03-31；
2. `seat-v3`：单价 200 分，从 2026-04-01 起持续有效（不填结束时间）；
3. 尝试登记 `seat-v2`：单价 180 分，2026-03-10 起替代 v1，但**不填结束时间**——它会持续有效并延伸进 v3 的有效时间，返回 `ErrOverlap`；
4. 沿用标识 `seat-v2`，把结束时间补为 2026-04-01（与 v3 的开始相接）后再次登记，成功；
5. 把报价受理时刻设为 2026-03-10 00:00，用各自的新请求标识、数量 4 分别引用 v1 和 v2：前者得到 `version_expired` 拒绝，后者按 180 分确认总价 720 分。

```go
// 命令 replacement 是“如何登记一个替代旧版本的费率版本”的完整可运行示例：
// 一次因重叠失败、修正结束时间后成功，并在交接点分别引用旧、新版本报价。
//
// 运行：
//
//	go run ./examples/replacement
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // v1 登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 替代 v1 的交接点（不含 v1）
	v2End   = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)  // v2 登记结束（不含），与 v3 开始相接
	v3Start = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

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

	// ② 再登记 seat-v3：单价 200 分，从 2026-04-01 起持续有效（不填结束时间）。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
	}); err != nil {
		panic(err)
	}

	printViews(book, "①② v1、v3 登记完成后的版本视图")

	// ③ 试图登记 seat-v2：单价 180 分，2026-03-10 起替代 v1，但不填结束时间。
	//    v2 的实际有效区间会是 [03-10, ∞)：旧版本 v1 可以被它截短，
	//    并不代表 v2 能占用同项其他版本（v3，[04-01, ∞)）的有效时间，因此返回 ErrOverlap。
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "seat-v1",
	})
	fmt.Printf("③ 登记 seat-v2（替代 v1，不填结束时间）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrOverlap) = %v；", errors.Is(err, tariff.ErrOverlap))
	fmt.Println("只有 err==nil 才表示登记生效，返回 ErrOverlap 即整次登记未生效")

	// ④ 失败后查询：仍是 v1、v3 两个版本，v2 不存在；
	//    v1 的实际结束仍是登记结束 3 月 31 日，没有出现被 v2 替代的关系；v3 也不受影响。
	printViews(book, "④ 登记失败后的版本视图（应与①②完全一致）")

	// ⑤ 沿用同一个版本标识 seat-v2，把结束时间补为 2026-04-01 00:00（不含）后再次登记。
	//    失败的登记不占用版本标识；v2 的区间 [03-10, 04-01) 恰好止于 v3 开始时刻，
	//    一个版本的结束与另一个版本的开始落在同一时刻属于相接，不算重叠。
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
	fmt.Println("⑤ 补填结束时间 2026-04-01 后沿用标识 seat-v2 再次登记：err=<nil>，登记生效")

	// ⑥ 成功后的查询：
	//    v1 的“登记结束”仍是 3 月 31 日（登记时填的值，永不改变），
	//    “实际有效结束”被截短到 3 月 10 日，并显示 v2 替代了 v1——两者不是同一个值。
	printViews(book, "⑥ 登记成功后的版本视图")

	// ⑦ 把报价受理时刻推进到交接点 2026-03-10 00:00（结束时刻不含该点），
	//    用两个各自的新请求标识、数量 4 分别引用 v1 与 v2。
	now = v2Start

	oldQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v1-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑦ 交接点用新标识引用旧版本 v1（quote-v1-at-handoff）", oldQuote)
	fmt.Printf("   调用 err==nil，但 Confirmed=%v、原因=%s：报价已被受理后拒绝，不能当成成功\n",
		oldQuote.Confirmed, oldQuote.Reason)

	newQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑧ 交接点用新标识引用新版本 v2（quote-v2-at-handoff）", newQuote)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 4 = 总价 %d 分\n",
		newQuote.Confirmed, newQuote.UnitPrice, newQuote.Total)
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

输出（受理时刻由示例时钟推进到确定值，不依赖真实日期）：

```text
①② v1、v3 登记完成后的版本视图：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-31T00:00:00Z 替代="" 被替代=""
   seat-v3：单价=200 开始=2026-04-01T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="" 被替代=""
③ 登记 seat-v2（替代 v1，不填结束时间）：err=tariff: version interval overlaps an existing version of the item
   errors.Is(err, tariff.ErrOverlap) = true；只有 err==nil 才表示登记生效，返回 ErrOverlap 即整次登记未生效
④ 登记失败后的版本视图（应与①②完全一致）：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-31T00:00:00Z 替代="" 被替代=""
   seat-v3：单价=200 开始=2026-04-01T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="" 被替代=""
⑤ 补填结束时间 2026-04-01 后沿用标识 seat-v2 再次登记：err=<nil>，登记生效
⑥ 登记成功后的版本视图：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-04-01T00:00:00Z 实际有效结束=2026-04-01T00:00:00Z 替代="seat-v1" 被替代=""
   seat-v3：单价=200 开始=2026-04-01T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="" 被替代=""
⑦ 交接点用新标识引用旧版本 v1（quote-v1-at-handoff）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-10T00:00:00Z
   结果=被拒绝 原因=version_expired（err 为 nil，拒绝不是调用错误）
   调用 err==nil，但 Confirmed=false、原因=version_expired：报价已被受理后拒绝，不能当成成功
⑧ 交接点用新标识引用新版本 v2（quote-v2-at-handoff）：
   请求 item=seat version=seat-v2 数量=4；首次受理时刻=2026-03-10T00:00:00Z
   结果=已确认 单价=180 分 总价=720 分
   Confirmed=true，单价 180 分 × 数量 4 = 总价 720 分
```

对照输出即可判断自己的登记是否生效：

- **③** 返回非空错误且 `errors.Is(.., tariff.ErrOverlap)` 为真：v2 若不填结束时间，区间会延伸到 4 月 1 日之后，与持续有效的 v3 重叠。`Replaces: "seat-v1"` 只能截短 v1，不能让 v2 占用 v3 的时间。
- **④** 失败后的查询与操作前完全一致：只有 v1、v3 两个版本；v1 的登记结束和实际有效结束都仍是 3 月 31 日，`被替代=""`，不存在被 v2 替代的关系；v3 不受任何影响。
- **⑤** 失败的登记没有占用 `seat-v2` 这个版本标识，补上结束时间后沿用同一标识登记成功；v2 的结束与 v3 的开始同为 4 月 1 日零点，端点相接不算重叠。
- **⑥** 成功后再查询，v1 的**登记结束**仍是 3 月 31 日（登记时填写的值，永不因后续登记改变），而**实际有效结束**变为 3 月 10 日，并出现 v1 `被替代="seat-v2"`、v2 `替代="seat-v1"` 的关系。登记结束和实际有效结束是两个不同含义的字段，不能解释成同一个值。
- **⑦⑧** 两次报价调用的 `err` 都为 `nil`：⑦ 是受理后的拒绝（`Confirmed=false`、`version_expired`、金额为 0），⑧ 才是确认（180 分 × 4 = 720 分）。报价成功必须以 `Confirmed == true` 为准。

## 连续调整费率时，该替代哪一版

费率连续调整（甲版被乙版替代、乙版又将被丙版替代）时，容易只盯着旧版本**登记时填写的结束时刻** `End` 判断替代关系，结果把**早已被截短的甲版**再次填进 `Replaces`，登记被拒绝。判断该替代哪一版，只能以 `ItemVersions` 返回的**当前实际有效区间**（`EffectiveEnd`）和替代关系（`Replaces` / `SupersededBy`）为准：

- 登记带 `Replaces` 的新版本时，新版本的 `Start` 必须**严格晚于**被替代版本的 `Start`，并且**严格落在该版本当前的实际有效区间内**：即被替代版本的 `EffectiveEnd` 为 `nil`，或 `Start < EffectiveEnd`。
- `Start` **恰好等于**被替代版本的实际有效结束时刻也会被拒绝（结束时刻不含在有效期内），返回 `tariff.ErrInvalidReplacement`，可用 `errors.Is` 判别。
- 甲版被乙版替代后，甲版的 `EffectiveEnd` 已截短到乙版的 `Start`，即使甲版登记的 `End` 仍是更晚的日期也不会变。因此在那之后再登记丙版时，**不能因为甲版登记的结束还没到，就把交接时刻视为甲版仍可用**——要替代的是当前实际覆盖该时刻的乙版。识别方法：`被替代（SupersededBy）` 已非空、或 `EffectiveEnd` 已不晚于新 `Start` 的版本，都不能再作为 `Replaces` 的目标。
- **普通登记允许相邻区间接续，不等于填写 `Replaces` 后允许在旧版结束点替代它**：不填 `Replaces` 时，新版本开始时刻恰好等于另一版本的结束时刻属于半开区间相接、可以登记；但同一时刻若出现在 `Replaces` 关系中，因要求严格落在旧版实际有效期内，会被拒绝。
- 与其他登记校验失败一样，`ErrInvalidReplacement` 是**调用本身返回错误**：整次登记回滚，新版本不入库、版本标识不被占用，既有的实际结束与替代关系全部不变，修正 `Replaces` 后可沿用同一版本标识重新登记。这与报价的语义不同——报价引用已失效版本时调用的 `err` 为 `nil`，账本正常受理后返回 `Confirmed == false`、`Reason == version_expired` 的拒绝结果；而登记失败没有任何结果被受理，只看返回的 `error` 是否为 `nil`。

### 一次填错被替代版本、改正后成功的完整示例

程序位于 [`examples/chain/main.go`](examples/chain/main.go)，可直接运行：

```bash
go run ./examples/chain
```

所有日期均为 2026 年 UTC 零点、结束时刻不含；时间与费率数据全部在代码中给出，并通过公开选项 `tariff.WithClock` 注入时钟，因此无论在哪一天运行，输出都确定、可复现，不依赖阅读当天的日期。生产环境直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一费率项 `seat` 的连续替代）：

1. 甲版 `seat-a`：单价 150 分，2026-03-01 生效，登记结束为 2026-03-31；
2. 乙版 `seat-b`：单价 180 分，2026-03-10 起替代甲版，登记结束为 2026-03-25——甲版实际有效结束随之截短到 3 月 10 日，但甲版登记的 3 月 31 日原样保留；
3. 准备登记丙版 `seat-c`：单价 200 分，2026-03-20 开始、2026-03-24 结束。先把被替代版本填成**甲版**：甲版的实际有效期已在 3 月 10 日结束，3 月 20 日不在甲版当前实际有效区间内，返回 `ErrInvalidReplacement`；
4. 失败后查询：仍只有甲、乙两版，甲版的实际结束与甲→乙替代关系不变，乙版也没有被截短；
5. 沿用同一标识 `seat-c`，只把被替代版本改为**乙版**（其当前实际有效区间为 [03-10, 03-25)，3 月 20 日在其中），登记成功；
6. 末尾在另一本独立账本上做边界对照：同一时刻普通登记允许区间相接接续，但作为 `Replaces` 的交接点、恰好等于被替代版本实际结束时仍被拒绝。

```go
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
```

输出（由代码中给出的确定时间产生，不依赖运行当天的日期）：

```text
①② 甲、乙两版登记完成后的版本视图：
   seat-a：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-b"
   seat-b：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-25T00:00:00Z 实际有效结束=2026-03-25T00:00:00Z 替代="seat-a" 被替代=""
③ 登记丙版（200 分，03-20 起，误填被替代版本=甲版）：err=tariff: new version start must be after the replaced version start and within its current effective interval
   errors.Is(err, tariff.ErrInvalidReplacement) = true
   登记失败是调用本身返回错误：丙版不入库、标识不被占用，与报价受理后的拒绝不同
④ 失败后的版本视图（仍只有甲、乙两版，关系与边界均不变）：
   seat-a：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-b"
   seat-b：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-25T00:00:00Z 实际有效结束=2026-03-25T00:00:00Z 替代="seat-a" 被替代=""
⑤ 沿用标识 seat-c、被替代版本改为乙版再次登记：err=<nil>，登记生效
⑥ 丙版登记成功后的版本视图（乙版一肩挑两头，登记结束仍为 03-25）：
   seat-a：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-b"
   seat-b：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-25T00:00:00Z 实际有效结束=2026-03-20T00:00:00Z 替代="seat-a" 被替代="seat-c"
   seat-c：单价=200 开始=2026-03-20T00:00:00Z 登记结束=2026-03-24T00:00:00Z 实际有效结束=2026-03-24T00:00:00Z 替代="seat-b" 被替代=""
⑦ 边界对照：普通登记允许相接接续，但替代时刻恰好等于实际结束会被拒绝
   在实际结束点 03-20 替代 d 版：err=tariff: new version start must be after the replaced version start and within its current effective interval
   errors.Is(err, tariff.ErrInvalidReplacement) = true：端点相接可普通接续，但不能在结束点替代
   同一时刻不填 Replaces 普通登记 seat-f：err=<nil>，端点相接允许接续
   边界对照账本视图：
   seat-d：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-20T00:00:00Z 实际有效结束=2026-03-20T00:00:00Z 替代="" 被替代=""
   seat-f：单价=200 开始=2026-03-20T00:00:00Z 登记结束=2026-03-25T00:00:00Z 实际有效结束=2026-03-25T00:00:00Z 替代="" 被替代=""
```

对照输出即可判断连续调费时该替代哪一版：

- **②之后**：甲版的登记结束仍是 3 月 31 日，但实际有效结束已是 3 月 10 日、`被替代="seat-b"`。3 月 20 日当前实际生效的是乙版 [03-10, 03-25)，该被丙版替代的是乙版。
- **③** 返回非空错误且 `errors.Is(.., tariff.ErrInvalidReplacement)` 为真：甲版的实际有效期已在 3 月 10 日结束，不能凭它登记的结束仍是 3 月 31 日，就把 3 月 20 日当作甲版可用的替代时刻。
- **④** 失败后的查询与操作前完全一致：仍只有甲、乙两版；甲版实际结束 3 月 10 日和甲→乙替代关系没有变化，乙版的实际结束仍是登记时填写的 3 月 25 日、没有被这次失败登记截短，丙版不存在，`seat-c` 标识也未被占用。
- **⑤⑥** 沿用同一标识只改 `Replaces` 后登记成功：乙版同时呈现 `替代="seat-a"` 与 `被替代="seat-c"`，实际有效结束变为 3 月 20 日，而它登记时填写的结束 3 月 25 日仍原样保留；甲版被乙版替代的第一次交接关系继续保留，甲版两个结束字段（登记 3 月 31 日、实际 3 月 10 日）都不变。
- **⑦** 交接点必须严格落在被替代版本的当前实际有效期内：新 `Start` 晚于被替代版本 `Start`、且早于其 `EffectiveEnd`，恰好等于实际结束时刻也返回 `ErrInvalidReplacement`。同一时刻不填 `Replaces` 的普通登记允许区间相接接续——两条规则各管各的场景，不能互相套用。
- **③是调用错误，不是受理后的拒绝**：登记失败时只有返回的 `error`，账本状态完全不变；这不同于报价引用失效版本时 `err == nil`、`Outcome.Confirmed == false`（`version_expired`）的正常受理后拒绝。
