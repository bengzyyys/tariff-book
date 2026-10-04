# 本地费率版本与报价

`tariff` 是一个在本机运行的费率版本与报价账本（`tariff.Book`），支持：

- 按费率项登记带整数分单价、生效起点和结束时刻（不含）的费率版本；
- 登记一个**替代旧版本**的新版本，账本自动把旧版本的实际有效期截断到交接时刻；
- 按费率项和指定时刻查询**该瞬间生效的单个版本**（开始含、结束不含，按实际有效区间选择）；
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

## 按费率项和时刻查询生效版本

`ItemVersions` 列出一个费率项的全部版本，但“某一刻到底该用哪一版”仍需调用方自己从登记结束与实际结束中判断。账本提供独立的公开入口直接回答这个问题：

```go
func (b *Book) EffectiveVersion(itemID string, at time.Time) (VersionView, error)
```

- **输入**：费率项标识和一个明确的时刻。时刻由调用方给出，可以是过去也可以是未来，与账本时钟（`WithClock`）无关——查询不会只返回“当前”生效的版本。
- **成功**：返回该费率项在这一瞬间生效的**单个版本**，内容就是现有版本视图 `VersionView`：版本标识、整数分单价、登记起止时间（`Start`/`End`）、实际有效区间（`EffectiveStart`/`EffectiveEnd`）和替代关系（`Replaces`/`SupersededBy`），可据此核对选择依据。
- **选择依据是实际有效区间** `[EffectiveStart, EffectiveEnd)`：开始时刻包含在内，结束时刻不包含在内，`EffectiveEnd == nil` 表示持续有效。被替代的旧版本从交接时刻起就不能被选中，即使登记结束仍在将来；替代它的新版本到期后，也**不会回退**到旧版本。
- 时间按实际时刻比较，不同时区表示同一瞬间时结论一致。

### 两种失败必须区分

- 费率项**存在**，但指定时刻早于首版开始，或落在两版之间的空档：返回 `tariff.ErrNoEffectiveVersion`，不拿邻近版本补位；
- 费率项**从未登记过**：仍返回 `tariff.ErrItemNotFound`。

```go
v, err := book.EffectiveVersion("seat", someTime)
switch {
case errors.Is(err, tariff.ErrItemNotFound):
    // 费率项从未登记
case errors.Is(err, tariff.ErrNoEffectiveVersion):
    // 费率项存在，但该时刻没有生效版本（早于首版或处于空档）
case err != nil:
    // 其他调用错误
default:
    // v 即当时生效的唯一版本
}
```

例如同一费率项的旧版 3 月 1 日起、登记到 3 月 31 日，新版 3 月 10 日起替代旧版并在 3 月 20 日结束（结束时刻不含）：

| 查询时刻 | 结果 |
| --- | --- |
| 3 月 9 日 | 旧版 |
| 3 月 10 日 | 新版（开始时刻含） |
| 3 月 20 日 | `ErrNoEffectiveVersion`（新版结束时刻不含，旧版不恢复） |

### 只读、独立

`EffectiveVersion` 只读取版本信息：它不受理报价、不占用请求标识，也不改变已保存的确认价与拒绝原因；按指定版本报价、版本列表和历史报价查询继续按原规则工作。返回的视图与账本及其他查询结果相互独立，调用方修改其中的结束时间不会改变账本，也不影响后续报价。

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

## 连续替代：被替代版本要看实际有效期，不看登记结束

连续调整同一费率项时容易犯一类错误：准备登记下一个替代版本时，看到**上一版登记时填写的结束日期尚未到**，就把它填成被替代版本。登记结束只是登记时填写的值，真正判定替代时刻是否可用的是该版本**当前的实际有效期**（`EffectiveEnd`，可能已因被替代而提前截短）。

替代时刻的边界规则：

- 新版本的 `Start` 必须**晚于**被替代版本的 `Start`（恰好相等也不行）；
- 新版本的 `Start` 必须落在被替代版本**当前的实际有效期** `[EffectiveStart, EffectiveEnd)` 内——**恰好等于实际结束时刻也会被拒绝**，因为结束时刻不含在有效期内，旧版本在该点已经不再有效；
- 违反任一条都返回 `tariff.ErrInvalidReplacement`（可用 `errors.Is` 判别），整次登记回滚：新版本不入库、版本标识不被占用、既有版本的边界和替代关系都不变。

注意与上一节重叠规则的区别：普通登记中允许相邻区间接续（一个版本的结束与另一个版本的开始落在同一时刻不算重叠），但**填写了 `Replaces` 之后，不允许在旧版本的结束点替代它**——相接只豁免重叠判断，不代表旧版本在结束时刻仍然有效。

因此连续调整时该替代哪一版，要用 `ItemVersions` 查**实际有效结束**来判断：找到在计划交接时刻仍然有效的版本（即 `EffectiveStart < Start < EffectiveEnd` 的版本），把它填入 `Replaces`；不要凭某版登记结束尚未到就选它。

同样要把两类“没成功”分开：登记失败（如 `ErrInvalidReplacement`）是**调用本身返回错误**；而报价引用已失效版本是正常受理后返回的拒绝结果（`err == nil`、`Confirmed == false`、`Reason == version_expired`），两者含义不同。

### 一次误填被替代版本、修正后成功的完整示例

程序位于 [`examples/chain/main.go`](examples/chain/main.go)，可直接运行：

```bash
go run ./examples/chain
```

所有日期均为 2026 年 UTC 零点、结束时刻不含；时间与费率数据全部在代码中给出，并通过公开选项 `tariff.WithClock` 注入可手动推进的时钟，因此无论在哪一天运行，输出都确定、可复现。生产环境直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一费率项 `seat`）：

1. 甲版 `seat-v1`：单价 150 分，2026-03-01 生效，登记结束为 2026-03-31；
2. 乙版 `seat-v2`：单价 180 分，2026-03-10 起替代甲版，登记结束为 2026-03-25——甲版的实际有效期从这一刻起截短为 `[03-01, 03-10)`；
3. 准备登记丙版 `seat-v3`：单价 200 分，2026-03-20 开始、2026-03-24 结束。先把被替代版本填成甲版——甲版的登记结束（3 月 31 日）虽然尚未到，但它的实际有效期已经在 3 月 10 日结束，不能把 3 月 20 日视为可用的替代时刻，返回 `ErrInvalidReplacement`；
4. 失败后查询：仍只有甲、乙两版，甲版的实际结束及甲乙之间的替代关系没有变化，乙版也没有被截短；
5. 沿用同一个丙版标识 `seat-v3`，只把被替代版本改为乙版，登记成功：乙版既替代甲版又被丙版替代，实际结束变为 3 月 20 日，而它登记时填写的 3 月 25 日仍然保留；甲版的第一次交接关系也继续保留。

```go
// 命令 chain 是“连续调整费率时如何选对被替代版本”的完整可运行示例：
// 同一费率项已完成甲→乙一次替代后，登记丙版时误把被替代版本填成甲版，
// 被 ErrInvalidReplacement 拒绝；沿用丙版标识改为替代乙版后成功。
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

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // 甲版生效
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 甲版登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 乙版替代甲版的交接点
	v2End   = time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC) // 乙版登记结束（不含）
	v3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // 丙版计划的交接点
	v3End   = time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC) // 丙版登记结束（不含）
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记甲版 seat-v1：单价 150 分，2026-03-01 生效，登记结束 2026-03-31（不含）。
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

	// ② 登记乙版 seat-v2：单价 180 分，2026-03-10 起替代甲版，登记结束 2026-03-25（不含）。
	//    从这一刻起甲版的实际有效期被截短为 [03-01, 03-10)，
	//    但它登记时填写的结束 3 月 31 日仍然保留。
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

	printViews(book, "①② 甲、乙两版登记完成后的版本视图")

	// ③ 准备登记丙版 seat-v3：单价 200 分，2026-03-20 开始、2026-03-24 结束。
	//    误把被替代版本填成甲版：甲版的登记结束（3 月 31 日）虽然尚未到，
	//    但它的实际有效期已经在 3 月 10 日被乙版截短，
	//    3 月 20 日不在甲版当前的实际有效期 [03-01, 03-10) 内，
	//    不能因为登记结束仍是 3 月 31 日就把 3 月 20 日当作可用的替代时刻，
	//    因此返回 ErrInvalidReplacement。
	v3RegisteredEnd := v3End
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
		End:       &v3RegisteredEnd,
		Replaces:  "seat-v1",
	})
	fmt.Printf("③ 登记 seat-v3（误填替代甲版）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v；",
		errors.Is(err, tariff.ErrInvalidReplacement))
	fmt.Println("登记失败是调用本身返回错误，整次登记未生效")

	// ④ 失败后查询：仍只有甲、乙两版；甲版的实际结束仍是 3 月 10 日、
	//    甲乙之间的替代关系不变；乙版也没有被这次失败的登记截短。
	printViews(book, "④ 登记失败后的版本视图（应与①②完全一致）")

	// ⑤ 沿用同一个版本标识 seat-v3，只把被替代版本改为乙版后再次登记。
	//    失败的登记不占用版本标识；3 月 20 日晚于乙版开始、
	//    且落在乙版当前的实际有效期 [03-10, 03-25) 内，登记成功。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
		End:       &v3RegisteredEnd,
		Replaces:  "seat-v2",
	}); err != nil {
		panic(err)
	}
	fmt.Println("⑤ 沿用标识 seat-v3、改为替代乙版后再次登记：err=<nil>，登记生效")

	// ⑥ 成功后的查询：乙版既替代甲版又被丙版替代，实际结束变为 3 月 20 日，
	//    而它登记时填写的 3 月 25 日仍然保留；甲版的第一次交接关系也继续保留。
	printViews(book, "⑥ 登记成功后的版本视图")

	// ⑦⑧ 把报价受理时刻推进到第二次交接点 2026-03-20 00:00（结束时刻不含该点），
	//     对比两类“没成功”：③的登记失败是调用返回错误，
	//     而引用已失效版本的报价是正常受理后返回的拒绝结果（err 为 nil）。
	now = v3Start

	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑦ 第二次交接点用新标识引用乙版（quote-v2-at-handoff）", expired)
	fmt.Printf("   调用 err==nil，但 Confirmed=%v、原因=%s：受理后的拒绝，与③的调用错误含义不同\n",
		expired.Confirmed, expired.Reason)

	confirmed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-v3-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v3",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑧ 第二次交接点用新标识引用丙版（quote-v3-at-handoff）", confirmed)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 4 = 总价 %d 分\n",
		confirmed.Confirmed, confirmed.UnitPrice, confirmed.Total)
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
①② 甲、乙两版登记完成后的版本视图：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-25T00:00:00Z 实际有效结束=2026-03-25T00:00:00Z 替代="seat-v1" 被替代=""
③ 登记 seat-v3（误填替代甲版）：err=tariff: new version start must be after the replaced version start and within its current effective interval
   errors.Is(err, tariff.ErrInvalidReplacement) = true；登记失败是调用本身返回错误，整次登记未生效
④ 登记失败后的版本视图（应与①②完全一致）：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-25T00:00:00Z 实际有效结束=2026-03-25T00:00:00Z 替代="seat-v1" 被替代=""
⑤ 沿用标识 seat-v3、改为替代乙版后再次登记：err=<nil>，登记生效
⑥ 登记成功后的版本视图：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-25T00:00:00Z 实际有效结束=2026-03-20T00:00:00Z 替代="seat-v1" 被替代="seat-v3"
   seat-v3：单价=200 开始=2026-03-20T00:00:00Z 登记结束=2026-03-24T00:00:00Z 实际有效结束=2026-03-24T00:00:00Z 替代="seat-v2" 被替代=""
⑦ 第二次交接点用新标识引用乙版（quote-v2-at-handoff）：
   请求 item=seat version=seat-v2 数量=4；首次受理时刻=2026-03-20T00:00:00Z
   结果=被拒绝 原因=version_expired（err 为 nil，拒绝不是调用错误）
   调用 err==nil，但 Confirmed=false、原因=version_expired：受理后的拒绝，与③的调用错误含义不同
⑧ 第二次交接点用新标识引用丙版（quote-v3-at-handoff）：
   请求 item=seat version=seat-v3 数量=4；首次受理时刻=2026-03-20T00:00:00Z
   结果=已确认 单价=200 分 总价=800 分
   Confirmed=true，单价 200 分 × 数量 4 = 总价 800 分
```

对照输出即可判断该替代哪一版：

- **①②** 乙版登记后，甲版的**登记结束**仍是 3 月 31 日，但**实际有效结束**已是 3 月 10 日。判断替代时刻是否可用要看后者：3 月 20 日不在甲版的实际有效期 `[03-01, 03-10)` 内。
- **③** 误填 `Replaces: "seat-v1"` 返回 `ErrInvalidReplacement`：不能因为甲版登记结束尚未到，就把 3 月 20 日当作可用的替代时刻。这是调用本身返回的错误，不是受理后的拒绝结果。
- **④** 失败后的查询与操作前完全一致：仍只有甲、乙两版；甲版的实际结束仍是 3 月 10 日、`被替代="seat-v2"` 的第一次交接关系不变；乙版的实际结束仍是 3 月 25 日，没有被这次失败的登记截短。
- **⑤⑥** 失败的登记不占用 `seat-v3` 标识，改为替代乙版后成功。此时乙版同时显示 `替代="seat-v1"` 和 `被替代="seat-v3"`，实际结束变为 3 月 20 日，而它登记时填写的 3 月 25 日仍然保留；甲版的第一次交接关系（实际结束 3 月 10 日、被乙版替代）也继续保留。
- **⑦⑧** 两次报价调用的 `err` 都为 `nil`：⑦ 是受理后的拒绝（`version_expired`），⑧ 才是确认（200 分 × 4 = 800 分）。这与 ③ 的调用错误是两类不同的“没成功”。
