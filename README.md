# 本地费率版本与报价

`tariff` 是一个在本机运行的费率版本与报价账本（`tariff.Book`），支持：

- 按费率项登记带整数分单价、生效起点和结束时刻（不含）的费率版本；
- 登记一个**替代旧版本**的新版本，账本自动把旧版本的实际有效期截断到交接时刻；
- 按费率项和**指定时刻**查询当时生效的单个版本（`EffectiveVersionAt`），选择依据是账本已登记的实际有效区间；
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

## 按指定时刻查询生效版本

`ItemVersions` 只能列出一个费率项的全部版本；想知道**某个瞬间应当采用哪一版**时，使用账本的公开入口：

```go
view, err := book.EffectiveVersionAt("seat", t)
```

选择依据是查询时账本已经登记的**实际有效区间** `[EffectiveStart, EffectiveEnd)`：

- 开始时刻**包含**在内，结束时刻**不包含**；实际结束为 `nil` 表示持续有效。
- 旧版本被替代后，实际有效区间止于交接时刻，从交接时刻起就不会再被选中——**即使登记的结束时间仍在将来**；替代它的新版本到期后，也**不能回退**到旧版本。
- 查询针对的是**调用方指定的时刻**，可以是过去也可以是未来，与账本时钟（`WithClock`）无关，不是只返回“当前”正在生效的版本。
- 时间按实际时刻比较，同一瞬间用不同时区表示，结论一致；选择只在指定费率项内进行，**其他费率项中同名的版本不参与**。

成功时返回的单个 `VersionView` 与 `ItemVersions` 的元素结构相同：版本标识、整数分单价、登记起止时间、实际有效区间以及替代关系（`Replaces` / `SupersededBy`）都在其中，可以直接核对选中依据。

### 两类失败要分开

- **费率项从未登记过版本**：返回 `tariff.ErrItemNotFound`（与 `ItemVersions` 的既有错误相同）。
- **费率项存在，但指定瞬间没有任何生效版本**：返回 `tariff.ErrNoEffectiveVersion`（可用 `errors.Is` 判别）。早于首版开始、落在两版之间的空档、或全部版本都已到期都属于这一类——账本不会拿时间上邻近的版本补位。

例如旧版 3 月 1 日开始、登记到 3 月 31 日；新版 3 月 10 日起替代旧版、3 月 20 日结束：查 3 月 9 日返回旧版，查 3 月 10 日返回新版，查 3 月 20 日则返回 `ErrNoEffectiveVersion`（结束时刻不含，且旧版不恢复）。

### 只读、独立

该查询只读取版本信息：**不受理报价、不占用请求标识**，`Quote`、`ItemVersions`、`Lookup` 的既有规则和已保存的确认价、拒绝原因都不受影响。返回的视图是独立副本，调用方修改其中的结束时间不会改变账本，也不会影响后续查询与报价。

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

## 区分“版本不可用”与“总价溢出”

`Outcome.Reason` 为 `total_overflow` 表示：数量为合法正整数、引用的版本在受理时刻**有效**，但 `单价 × 数量` 超出了有符号 64 位整数可表示的范围，账本无法给出总价，只能拒绝。它与 `version_not_yet_effective`、`version_expired` 是不同性质的拒绝，判断顺序是固定的：

- 数量为合法正整数时，账本**先判断指定版本在受理时刻是否有效**，只有有效版本才会进入金额判断、因总价超过上限被拒绝；版本不可用时，即使同样的数量与单价相乘在数学上必然溢出，原因也是版本问题而不是 `total_overflow`。
- 版本是否到期看的是被替代后的**实际有效结束**（`EffectiveEnd`），不是登记时填写的结束时间：开始时刻**包含**在有效期内，结束时刻**不包含**。旧版被替代后，从交接时刻起即失效，即使它登记的结束日期尚未到。
- 引用不可用版本**不会自动改用同项另一版**：交接前引用新版不会改用当时有效的旧版，交接后引用旧版也不会改用新版，拒绝结果的来源（`Outcome.Request`）始终是调用方指定的版本。
- 出现版本类拒绝（`version_not_yet_effective` / `version_expired` / `version_not_found`）**不表示**这组数量与单价相乘一定没有溢出——账本根本没有走到金额判断；同样，`total_overflow` 只说明金额无法表示，不说明版本无效。两类结论不能互相推断。
- 上限本身不是非法金额：总价**恰好等于**有符号 64 位整数最大值时仍可确认，只有**超过**上限才被拒绝。

这些拒绝的调用错误均为 `nil`（拒绝是正常受理结果，不是调用错误），且拒绝结果中**单价和总价均为零**——零金额不代表登记的费率是零，只是“没有可给出的金额”；也不能仅凭调用没有返回错误就当作确认报价，必须检查 `Confirmed`。

### 同一费率项两版对照的完整示例

程序位于 [`examples/overflow/main.go`](examples/overflow/main.go)，可直接运行：

```bash
go run ./examples/overflow
```

所有日期均为 2026 年 UTC 零点、结束时刻不含；账本初始化、费率登记和受理时刻全部在代码中给出，并通过公开选项 `tariff.WithClock` 注入可手动推进的时钟，因此无论在哪一天运行，输出都确定、可复现，读者无需补写初始化、费率登记或受理时间设置。生产环境直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一费率项 `seat`，两版单价都是有符号 64 位整数最大值，单位分）：

1. 旧版 `seat-v1`：2026-03-01 00:00 起生效，登记结束为 2026-03-31 00:00；
2. 新版 `seat-v2`：2026-03-10 00:00 起替代旧版并持续有效——旧版的登记结束仍是 3 月 31 日，实际有效结束被截短到 3 月 10 日；
3. 交接前（3 月 9 日）以数量 2 引用新版，得到 `version_not_yet_effective`；
4. 交接时刻（3 月 10 日 00:00）以数量 2 引用旧版，得到 `version_expired`——尽管旧版登记的结束日期尚未到；
5. 交接时刻以数量 2 引用新版，才得到 `total_overflow`（2 × MaxInt64 超出可表示范围）；
6. 交接时刻以数量 1 引用新版，总价恰好等于上限，仍可确认。

每笔报价使用各自从未使用过的非空请求标识，输出反映的都是首次受理的判断。

```go
// 命令 overflow 是“区分指定版本不能使用与版本有效但总价无法表示”的完整可运行示例：
// 同一费率项的两个版本单价都是有符号 64 位整数最大值（分），数量为 2 时
// 数学总价超出该类型上限。交接前引用新版得到 version_not_yet_effective，
// 交接时刻引用旧版得到 version_expired（尽管它登记的结束日期尚未到），
// 交接时刻引用新版才得到 total_overflow；同刻以数量 1 引用新版则恰好
// 等于上限、仍可确认。
//
// 运行：
//
//	go run ./examples/overflow
package main

import (
	"fmt"
	"math"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // 旧版生效
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 旧版登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 新版替代旧版的交接点
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记旧版 seat-v1：单价为有符号 64 位整数最大值（分），
	//    2026-03-01 生效，登记结束 2026-03-31（不含）。
	v1RegisteredEnd := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: math.MaxInt64,
		Start:     v1Start,
		End:       &v1RegisteredEnd,
	}); err != nil {
		panic(err)
	}

	// ② 登记新版 seat-v2：单价同样为最大值（分），2026-03-10 起替代旧版、持续有效。
	//    旧版的登记结束仍是 3 月 31 日，实际有效结束从这一刻起被截短到 3 月 10 日。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: math.MaxInt64,
		Start:     v2Start,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	printViews(book, "①② 两版登记完成后的版本视图")

	// 以下每笔报价都使用各自从未使用过的非空请求标识，数量为 2，
	// 因此输出反映的都是首次受理的判断。
	// 数量为合法正整数时，账本先判断指定版本在受理时刻是否有效，
	// 只有有效版本才会进入金额判断、因总价超过上限被拒绝。

	// ③ 交接前（2026-03-09 00:00）引用新版：新版尚未生效，
	//    得到 version_not_yet_effective——即使 2×MaxInt64 在数学上必然溢出，
	//    版本不可用优先于金额判断，也不会自动改用当时有效的旧版。
	now = time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	early, err := book.Quote(tariff.QuoteRequest{
		RequestID: "overflow-v2-before-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  2,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("③ 交接前引用新版（overflow-v2-before-handoff）", early)

	// ④ 交接时刻（2026-03-10 00:00）引用旧版：旧版的实际有效区间止于交接点
	//    （结束时刻不含），尽管它登记的结束 3 月 31 日尚未到，仍得到 version_expired。
	now = v2Start
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "overflow-v1-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  2,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("④ 交接时刻引用旧版（overflow-v1-at-handoff）", expired)

	// ⑤ 交接时刻引用新版：新版有效（开始时刻含在有效期内），
	//    同一超大金额请求这时才因总价无法表示得到 total_overflow。
	overflow, err := book.Quote(tariff.QuoteRequest{
		RequestID: "overflow-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  2,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑤ 交接时刻引用新版（overflow-v2-at-handoff）", overflow)

	// ⑥ 同刻以数量 1 引用新版：总价恰好等于有符号 64 位整数最大值，
	//    上限本身不是非法金额，仍可确认。
	exact, err := book.Quote(tariff.QuoteRequest{
		RequestID: "overflow-v2-quantity-one",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  1,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑥ 交接时刻引用新版、数量 1（overflow-v2-quantity-one）", exact)
}

func printViews(book *tariff.Book, title string) {
	fmt.Printf("%s：\n", title)
	views, err := book.ItemVersions("seat")
	if err != nil {
		panic(err)
	}
	for _, v := range views {
		fmt.Printf("   %s：单价=%d 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
			v.VersionID, v.UnitPrice, fmtEnd(v.End), fmtEnd(v.EffectiveEnd),
			v.Replaces, v.SupersededBy)
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
		fmt.Printf("   结果=被拒绝 原因=%s 单价=%d 总价=%d（err 为 nil，拒绝不是调用错误）\n",
			o.Reason, o.UnitPrice, o.Total)
	}
}
```

输出（受理时刻由示例时钟推进到确定值，不依赖真实日期）：

```text
①② 两版登记完成后的版本视图：
   seat-v1：单价=9223372036854775807 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=9223372036854775807 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="seat-v1" 被替代=""
③ 交接前引用新版（overflow-v2-before-handoff）：
   请求 item=seat version=seat-v2 数量=2；首次受理时刻=2026-03-09T00:00:00Z
   结果=被拒绝 原因=version_not_yet_effective 单价=0 总价=0（err 为 nil，拒绝不是调用错误）
④ 交接时刻引用旧版（overflow-v1-at-handoff）：
   请求 item=seat version=seat-v1 数量=2；首次受理时刻=2026-03-10T00:00:00Z
   结果=被拒绝 原因=version_expired 单价=0 总价=0（err 为 nil，拒绝不是调用错误）
⑤ 交接时刻引用新版（overflow-v2-at-handoff）：
   请求 item=seat version=seat-v2 数量=2；首次受理时刻=2026-03-10T00:00:00Z
   结果=被拒绝 原因=total_overflow 单价=0 总价=0（err 为 nil，拒绝不是调用错误）
⑥ 交接时刻引用新版、数量 1（overflow-v2-quantity-one）：
   请求 item=seat version=seat-v2 数量=1；首次受理时刻=2026-03-10T00:00:00Z
   结果=已确认 单价=9223372036854775807 分 总价=9223372036854775807 分
```

对照输出即可区分“指定版本不能使用”和“版本有效但总价无法表示”：

- **③** 交接前引用新版：数量 2 与单价 MaxInt64 相乘在数学上必然溢出，但新版在受理时刻尚未生效，原因是 `version_not_yet_effective` 而不是 `total_overflow`——账本先判断版本可用性，没有走到金额判断，也没有改用当时有效的旧版。
- **④** 交接时刻引用旧版：旧版登记的结束是 3 月 31 日，看似仍在有效期内，但它被新版替代后的**实际有效结束**是 3 月 10 日 00:00（结束时刻不含），受理时刻恰在交接点，得到 `version_expired`。
- **⑤** 交接时刻引用新版：新版的开始时刻含在有效期内，版本有效，同一笔数量 2 的请求这时才因总价无法表示得到 `total_overflow`。
- **③④⑤** 三笔拒绝的调用错误都是 `nil`，单价和总价都是零：零金额只是“没有可给出的金额”，不代表登记的费率是零（两版单价实际都是 MaxInt64）；也不能仅凭 `err == nil` 当作确认报价，必须检查 `Confirmed`。
- **⑥** 数量 1 时总价恰好等于有符号 64 位整数最大值，仍可确认——上限本身不是非法金额，只有超过上限才被拒绝。

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

## 补登记过去生效的替代版本后，怎样核对已有报价

交接点可以**早于登记当天**：`RegisterVersion` 不读取账本时钟，替代规则只要求新版本的 `Start` 晚于被替代版本的 `Start`、且落在被替代版本当前的实际有效区间内，并与同项其他版本的实际有效区间不重叠（见上文各节）。因此可以事后补登记一个过去就已生效的替代版本。

补登记之后要分清两个查询口径：

- **按时刻选版**（`EffectiveVersionAt`）依据的是**查询时账本已登记的实际有效区间**。补登记改变了旧版的实际有效结束，因此它对过去时刻的回答会随之改变——同一时刻，补登记前答案是旧版，补登记后答案是新版。
- **按请求标识取回**（`Lookup` 或原样重试）的是**首次受理结果**。补登记不会重算已经确认的报价：即使确认时刻**晚于**后来补登记的交接点——按现在的账本看，那个时刻生效的已是新版——已确认记录仍保留原版本、数量、单价、总价和首次受理时刻，确认状态不变。

核对历史确认价应以保存的首次结果为准，不要按当前账本重算。需要按新费率重新报价时，必须使用一个从未使用过的新标识；沿用原标识只改版本会返回 `tariff.ErrRequestIDConflict`，原记录不被覆盖。同样要分清：报价被拒绝是正常受理结果（`err == nil`、`Confirmed == false`、原因在 `Reason`），而标识冲突是调用本身返回错误。

### 补登记前后对照的完整示例

程序位于 [`examples/retroactive/main.go`](examples/retroactive/main.go)，可直接运行：

```bash
go run ./examples/retroactive
```

所有日期与时刻均为 2026 年 UTC（同一时区）、结束时刻不含；时间与费率数据全部在代码中给出，并通过公开选项 `tariff.WithClock` 注入固定时刻的时钟，因此无论在哪一天运行，输出都确定、可复现，不依赖运行当天、也不需要等待真实时间。生产环境直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一费率项 `seat`）：

1. 旧版 `seat-v1`：单价 150 分，2026-03-01 生效，登记结束为 2026-03-31；
2. 2026-03-15 10:00，旧版仍在实际有效期内，用标识 `quote-001`、数量 4 报价，按旧版确认总价 600 分；
3. 仍在 3 月 15 日 10:00 这个受理时刻，补登记新版 `seat-v2`：单价 180 分，2026-03-10 起替代旧版、不填结束时间——交接点早于登记当天，同项没有其他版本与它重叠，登记成功；
4. 补登记前后都查询 3 月 15 日 10:00 的生效版本，答案从旧版变为新版；旧版的登记结束仍是 3 月 31 日，实际有效结束已提前到 3 月 10 日；
5. 按原标识 `Lookup` 及原样重试，仍是旧版、数量 4、单价 150 分、总价 600 分、首次受理时刻 3 月 15 日 10:00，确认状态不变；
6. 用新标识分别引用旧版和新版：旧版得到 `version_expired` 拒绝（调用错误为空），新版确认 720 分；沿用原标识只把版本改成新版，返回 `ErrRequestIDConflict`，原 600 分记录保留。

```go
// 命令 retroactive 是“补登记过去生效的替代版本后，怎样核对已有报价”的完整可运行示例：
// 报价按旧版确认后，仍在同一受理时刻补登记一个交接点早于登记当天的新版，
// 观察按时刻选版的答案改变，而已确认的报价保持首次受理结果不变。
//
// 运行：
//
//	go run ./examples/retroactive
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期与时刻均为 2026 年 UTC，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)   // 旧版生效
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)  // 旧版登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)  // 补登记的交接点：早于登记当天
	quoteAt = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC) // 报价受理与补登记发生的时刻
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

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
	fmt.Println("① 登记旧版 seat-v1：单价 150 分，[2026-03-01, 2026-03-31)，err=<nil>")

	// ② 2026-03-15 10:00，旧版仍在实际有效期内，用标识 quote-001 报价，数量 4。
	now = quoteAt
	first, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("② 3 月 15 日 10:00 首次报价（quote-001）", first)

	// ③ 补登记前，查询 3 月 15 日 10:00 的生效版本：是旧版。
	printEffective(book, "③ 补登记前查询 2026-03-15 10:00 的生效版本")

	// ④ 仍在 3 月 15 日 10:00 这个受理时刻，补登记新版 seat-v2：
	//    单价 180 分，2026-03-10 起替代旧版，不填结束时间。
	//    交接点（3 月 10 日）早于登记当天（3 月 15 日）也允许：
	//    替代规则只要求交接点晚于旧版开始、且落在旧版当前的实际有效区间内，
	//    不要求不早于登记时刻；同项也没有其他版本与 [03-10, ∞) 重叠。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}
	fmt.Println("④ 补登记 seat-v2（2026-03-10 起替代旧版，不填结束时间）：err=<nil>，登记生效")

	// ⑤ 版本视图：旧版的登记结束仍是 3 月 31 日（登记时填写的值，永不改变），
	//    实际有效结束已提前到 3 月 10 日——两者不是同一个值。
	printViews(book, "⑤ 补登记后的版本视图")

	// ⑥ 再次查询同一时刻：按时刻选版依据查询时账本已登记的实际有效区间，
	//    因此对过去时刻的回答从旧版变为新版。
	printEffective(book, "⑥ 补登记后查询同一时刻 2026-03-15 10:00 的生效版本")

	// ⑦ 但补登记不会重算已确认的报价：按原请求标识 Lookup，
	//    取回的仍是首次受理结果——旧版、数量 4、单价 150 分、总价 600 分、
	//    首次受理时刻 2026-03-15 10:00，确认状态不变。
	got, err := book.Lookup("quote-001")
	if err != nil {
		panic(err)
	}
	printOutcome("⑦ 补登记后按原标识 Lookup（quote-001）", got)
	fmt.Printf("   与首次结果完全一致：%v\n", got == first)

	// ⑧ 原样重试（相同标识、相同内容）同样返回首次结果。
	replay, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑧ 原样重试（quote-001）", replay)
	fmt.Printf("   与首次结果完全一致：%v\n", replay == first)

	// ⑨ 用新标识引用旧版：旧版的实际有效期止于 3 月 10 日，
	//    在 3 月 15 日 10:00 的受理时刻已失效，得到 version_expired 拒绝。
	//    这是正常受理后的拒绝结果，调用的 err 为 nil。
	oldQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-002",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑨ 用新标识引用旧版（quote-002）", oldQuote)
	fmt.Printf("   调用 err==nil，但 Confirmed=%v、原因=%s：受理后的拒绝，不是调用错误\n",
		oldQuote.Confirmed, oldQuote.Reason)

	// ⑩ 用新标识引用新版：按 180 分确认，总价 720 分。
	newQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-003",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑩ 用新标识引用新版（quote-003）", newQuote)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 4 = 总价 %d 分\n",
		newQuote.Confirmed, newQuote.UnitPrice, newQuote.Total)

	// ⑪ 沿用原标识 quote-001、只把版本改成新版：请求内容冲突，
	//     调用本身返回 ErrRequestIDConflict，原 600 分记录不被覆盖。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v2",
		Quantity:  4,
	})
	fmt.Printf("⑪ 沿用 quote-001 只把版本改成新版：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrRequestIDConflict) = %v\n",
		errors.Is(err, tariff.ErrRequestIDConflict))
	unchanged, err := book.Lookup("quote-001")
	if err != nil {
		panic(err)
	}
	fmt.Printf("   原记录未被覆盖：Confirmed=%v 版本=%s 数量=%d 单价=%d 总价=%d 首次受理时刻=%s\n",
		unchanged.Confirmed, unchanged.Request.VersionID, unchanged.Request.Quantity,
		unchanged.UnitPrice, unchanged.Total, unchanged.AcceptedAt.Format(time.RFC3339))
}

func printEffective(book *tariff.Book, title string) {
	v, err := book.EffectiveVersionAt("seat", quoteAt)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s：%s（单价=%d 分，实际有效结束=%s）\n",
		title, v.VersionID, v.UnitPrice, fmtEnd(v.EffectiveEnd))
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

输出（受理时刻由示例时钟固定在确定值，不依赖真实日期）：

```text
① 登记旧版 seat-v1：单价 150 分，[2026-03-01, 2026-03-31)，err=<nil>
② 3 月 15 日 10:00 首次报价（quote-001）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-15T10:00:00Z
   结果=已确认 单价=150 分 总价=600 分
③ 补登记前查询 2026-03-15 10:00 的生效版本：seat-v1（单价=150 分，实际有效结束=2026-03-31T00:00:00Z）
④ 补登记 seat-v2（2026-03-10 起替代旧版，不填结束时间）：err=<nil>，登记生效
⑤ 补登记后的版本视图：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="seat-v1" 被替代=""
⑥ 补登记后查询同一时刻 2026-03-15 10:00 的生效版本：seat-v2（单价=180 分，实际有效结束=无（持续有效））
⑦ 补登记后按原标识 Lookup（quote-001）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-15T10:00:00Z
   结果=已确认 单价=150 分 总价=600 分
   与首次结果完全一致：true
⑧ 原样重试（quote-001）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-15T10:00:00Z
   结果=已确认 单价=150 分 总价=600 分
   与首次结果完全一致：true
⑨ 用新标识引用旧版（quote-002）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-15T10:00:00Z
   结果=被拒绝 原因=version_expired（err 为 nil，拒绝不是调用错误）
   调用 err==nil，但 Confirmed=false、原因=version_expired：受理后的拒绝，不是调用错误
⑩ 用新标识引用新版（quote-003）：
   请求 item=seat version=seat-v2 数量=4；首次受理时刻=2026-03-15T10:00:00Z
   结果=已确认 单价=180 分 总价=720 分
   Confirmed=true，单价 180 分 × 数量 4 = 总价 720 分
⑪ 沿用 quote-001 只把版本改成新版：err=tariff: request id already used with different content
   errors.Is(err, tariff.ErrRequestIDConflict) = true
   原记录未被覆盖：Confirmed=true 版本=seat-v1 数量=4 单价=150 总价=600 首次受理时刻=2026-03-15T10:00:00Z
```

对照输出即可分清两个查询口径：

- **③⑥** 是**按时刻选版**：补登记前查 3 月 15 日 10:00 得到旧版，补登记后查同一时刻得到新版。补登记改变了账本已登记的实际有效区间，也就改变了它对过去时刻的回答——这正是“确认时刻晚于后来补登记的交接点”的情形：按现在的账本看，3 月 15 日 10:00 生效的已是新版。
- **⑦⑧** 是**按请求标识取回首次受理结果**：`Lookup` 和原样重试都仍是旧版、数量 4、单价 150 分、总价 600 分、首次受理时刻 3 月 15 日 10:00，确认状态不变。补登记不会撤销或重算已确认的报价，核对历史确认价应以这条保存的首次结果为准，不要按当前账本重算。
- **⑤** 同时展示两种边界：旧版的**登记结束**仍是 3 月 31 日（登记时填写的值，永不改变），**实际有效结束**已提前到 3 月 10 日。
- **⑨⑩** 用各自的新标识当场报价，检验的是**当前版本是否有效**：旧版已失效（`version_expired`，`err` 为 `nil`，是受理后的拒绝而非调用错误），新版确认 180 分 × 4 = 720 分。重新报价必须使用新标识。
- **⑪** 沿用原标识只改版本是**调用本身返回错误**（`ErrRequestIDConflict`），没有可用的报价结果，原 600 分记录不被覆盖——与 ⑨ 的“报价被拒绝”是两类不同的“没成功”。

## 旧版已被较晚版本替代后，还能在它剩余有效期内补登一个更早的替代版本

上一节是“先有旧版，事后补一个过去生效的新版”；还有一种更绕的登记顺序：**同一旧版本先被一个较晚生效的版本截短，随后又在它剩余的实际有效期内，补登记一个更早生效的替代版本**。

这时要抓住两条既有规则：

- 一个版本**已经显示被替代**，只表示它在当前实际结束之后失效，**并不表示它在剩余实际有效期内不能再次作为替代来源**。新版本的 `Start` 只要晚于它的生效起点、且仍落在它**当前的实际有效区间** `[EffectiveStart, EffectiveEnd)` 内（恰好等于实际结束时刻也不行，结束时刻不含），替代它的登记就成立；成功后它的实际结束会被进一步提前，`SupersededBy` 改写为这次补登的版本。
- 但补登版本同样**不能与同项其他版本的实际有效区间重叠**：`Replaces` 只让被替代的那一个旧版本退出本次重叠判断，先登记的较晚版本照常参与。补登版本的结束时刻与较晚版本的开始时刻落在同一瞬间属于**相接，不算重叠**。

因此登记完成后**不能**把三个版本读成“v1、v2、v3 依次替代”的链：每个版本的 `Replaces` 保留的是它**自己登记时**填写的来源，账本不会为了连成链而改写先登记版本的来源。版本列表按**生效时间**排列，也不按登记顺序排列。

### 一次先重叠失败、补结束时间后补登成功的完整示例

程序位于 [`examples/interpose/main.go`](examples/interpose/main.go)，可直接运行：

```bash
go run ./examples/interpose
```

所有日期均为 2026 年 UTC 零点、结束时刻不含；时间与费率数据全部在代码中给出。本示例只登记版本并按指定时刻查询、不调用 `Quote`：`RegisterVersion` 不读取账本时钟，`EffectiveVersionAt` 的查询时刻由调用方显式给出，所以即使直接 `tariff.NewBook()`（真实时钟），输出也确定、可复现，读者无需注入时钟、补写初始化或等待真实日期。生产环境其他场景直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一费率项 `seat`，登记顺序是 v1、v3、v2，而非按生效日期排列）：

1. `seat-v1`：单价 150 分，2026-03-01 生效，登记结束为 2026-03-31；
2. `seat-v3`：单价 200 分，2026-03-20 起替代 v1、持续有效（不填结束时间）——v1 的登记结束仍是 3 月 31 日，实际有效结束先被截短到 3 月 20 日；
3. 准备补登 `seat-v2`：单价 180 分，2026-03-10 起、同样替代 v1。**v1 已显示被 v3 替代并不妨碍它在 3 月 10 日再次被替代**——交接点晚于 v1 生效起点、且仍在它当前剩余的实际有效期 `[03-01, 03-20)` 内；但 v2 第一次不填结束时间，持续有效区间 `[03-10, ∞)` 与 v3 的 `[03-20, ∞)` 重叠，返回 `ErrOverlap`；
4. 失败后只查得到 v1 和 v3：v2 未入库、标识未被占用，v1 的实际结束仍是 3 月 20 日、仍显示被 v3 替代，关系不变；
5. 沿用未成功登记的标识 `seat-v2`，把结束时间补为 2026-03-20（与 v3 开始相接、不含）后再次登记，成功。

```go
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
```

输出（全部由代码中写死的 2026 年 UTC 时刻决定，不依赖真实日期，也不依赖账本时钟）：

```text
① 登记 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z)，err=<nil>
② 登记 seat-v3：单价 200 分，2026-03-20T00:00:00Z 起替代 seat-v1、持续有效，err=<nil>
①② 登记顺序为 v1、v3 时的版本视图：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-20T00:00:00Z 替代="" 被替代="seat-v3"
   seat-v3：单价=200 开始=2026-03-20T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="seat-v1" 被替代=""
③ 拟补登的交接点 2026-03-10T00:00:00Z 是否仍可用 seat-v1 作为替代来源：
   seat-v1：生效起点=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 当前实际有效结束=2026-03-20T00:00:00Z 被替代="seat-v3"
   交接点晚于生效起点=true，且早于当前实际有效结束=true：已被替代不等于不能再次被替代，seat-v1 仍可作为替代来源
④ 首次登记 seat-v2（替代 v1，不填结束时间）：err=tariff: version interval overlaps an existing version of the item
   errors.Is(err, tariff.ErrOverlap) = true；持续有效区间与 v3 重叠，整次登记未生效
⑤ 登记失败后的版本视图（仍只有 v1、v3，v2 未入库、标识未被占用）：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-20T00:00:00Z 替代="" 被替代="seat-v3"
   seat-v3：单价=200 开始=2026-03-20T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="seat-v1" 被替代=""
⑥ 补填结束时间 2026-03-20T00:00:00Z 后沿用标识 seat-v2 再次登记：err=<nil>，登记生效
⑦ 补登成功后的版本视图（按生效时间排列，而非登记顺序 v1→v3→v2）：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-20T00:00:00Z 实际有效结束=2026-03-20T00:00:00Z 替代="seat-v1" 被替代=""
   seat-v3：单价=200 开始=2026-03-20T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="seat-v1" 被替代=""
⑧ 按指定时刻查询生效版本（选择依据是账本已登记的实际有效区间）：
   查询 2026-03-09T00:00:00Z：seat-v1（单价=150，实际有效区间 [2026-03-01T00:00:00Z, 2026-03-10T00:00:00Z)）
   查询 2026-03-10T00:00:00Z：seat-v2（单价=180，实际有效区间 [2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)）
   查询 2026-03-15T12:00:00Z：seat-v2（单价=180，实际有效区间 [2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)）
   查询 2026-03-19T23:59:59Z：seat-v2（单价=180，实际有效区间 [2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)）
   查询 2026-03-20T00:00:00Z：seat-v3（单价=200，实际有效区间 [2026-03-20T00:00:00Z, 无（持续有效）)）
   查询 2026-03-31T00:00:00Z：seat-v3（单价=200，实际有效区间 [2026-03-20T00:00:00Z, 无（持续有效）)）
```

对照输出即可读懂这组版本关系：

- **①②** 先登记的 v3 已把 v1 的**实际有效结束**截短到 3 月 20 日，但 v1 的**登记结束**仍是 3 月 31 日——判断交接点能否再次替代 v1 要看前者，而不是后者。
- **③** `被替代="seat-v3"` 不等于 v1 不能再被替代：3 月 10 日晚于 v1 的生效起点（3 月 1 日）、又早于它当前的实际有效结束（3 月 20 日），仍落在剩余实际有效期内，v1 可以再次作为替代来源。调用方用 `ItemVersions` 读这两个边界即可自行判断。
- **④⑤** 旧版能再次被截短，不代表新版能占用同项**其他版本**的时间：v2 不填结束时间时 `[03-10, ∞)` 与持续有效的 v3 重叠，返回 `ErrOverlap`。失败整次回滚：只查得到 v1、v3；v1 的实际结束仍是 3 月 20 日、`被替代="seat-v3"` 不变；失败登记不占用 `seat-v2` 标识。
- **⑥⑦** 补上 3 月 20 日的结束时间后沿用原标识成功：v2 的结束与 v3 的开始端点相接（结束时刻不含），不算重叠。此时列表按**生效时间**排列为 v1、v2、v3，而不是登记顺序 v1、v3、v2；v1 的登记结束仍是 3 月 31 日，实际结束进一步提前到 3 月 10 日，`SupersededBy` 改为 `seat-v2`。
- **⑦** 这组关系**不是** v1→v2→v3 依次替代的链：v2 和 v3 的 `Replaces` 都保留 `seat-v1`（各自登记时填写的来源，账本不改写），v3 的开始仍是 3 月 20 日、单价仍为 200 分、仍持续有效；v2 的 `被替代=""`，它与 v3 只是时间相接，互不替代。
- **⑧** 按指定时刻选择只看**实际有效区间**：`[03-01, 03-10)` 选中 v1（150 分），3 月 10 日交接点（含）至 3 月 20 日前选中 v2（180 分），3 月 20 日交接点（含）起选中 v3（200 分）。到了 v1 登记结束 3 月 31 日当天也仍是 v3，不会因为 v3 “直接替代了 v1”之外还夹着 v2 而回退。

## 不同费率项使用同名版本时，怎样登记替代版本

版本标识只在**同一费率项内**唯一：同一本账本允许 `seat` 和 `room` 各自登记一个名为 `v1` 的版本，两者单价、有效期可以不同，跨费率项的有效期重叠也不是冲突。登记替代版本时，`Replaces` 填的只是版本标识，替代来源按**本次登记指定的费率项**（`RegisterRequest.ItemID`）查找：

- 被截短到交接时刻、被标记 `被替代` 的只是**本项**的同名版本；其他费率项的同名版本单价、登记结束、实际有效结束和替代关系都不受影响，本项的替代登记也不会在其他项留下任何版本。
- 交接时间校验（新版本的 `Start` 必须晚于来源的开始、且落在来源**当前的实际有效区间**内）只看本项的来源版本。本项来源已被截短、拟交接时刻不在其实际有效期内时，即使其他项的同名版本当时仍然有效，也返回 `tariff.ErrInvalidReplacement`——不能借另一项的同名版本替本项通过校验；来源就在本项，因此也不会误报成“来源属于另一项”。
- 本项找不到来源、而**其他项**存在同名版本时，返回 `tariff.ErrReplaceTargetWrongItem`；**所有费率项**都没有所填标识时，返回 `tariff.ErrReplaceTargetNotFound`（均可用 `errors.Is` 判别）。这两种错误都不会借用其他项的版本完成登记。
- 与所有登记失败一样，上述任何一种错误都整次回滚：新版本不入库、版本标识不被占用、两个费率项既有版本的边界和替代关系都不变。登记是否生效只看返回的 `error` 是否为 `nil`。
- 报价同样按“费率项 + 版本”各自受理：交接时刻用各自的新请求标识引用 `seat/v2` 与 `room/v1`，分别按各自版本的单价确认，互不影响。

### 两个费率项各有一个 v1 的完整示例

程序位于 [`examples/crossitem/main.go`](examples/crossitem/main.go)，可直接运行：

```bash
go run ./examples/crossitem
```

所有日期均为 2026 年 UTC 零点、结束时刻不含；账本初始化、费率数据和受理时刻全部在代码中给出，并通过公开选项 `tariff.WithClock` 注入可手动推进的时钟，因此无论在哪一天运行，输出都确定、可复现，不需要读者补写任何辅助代码。生产环境直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一本账本，两个费率项）：

1. `seat/v1`：单价 150 分，`room/v1`：单价 300 分，有效期都是 2026-03-01 至 2026-03-31（结束时刻不含）；
2. 给 `seat` 登记 `v2`：单价 180 分，2026-03-10 起替代 `v1`——`seat/v1` 的登记结束仍是 3 月 31 日，实际有效结束变为 3 月 10 日，`seat` 两版之间的替代关系保留；`room/v1` 的单价、两种结束时间和替代关系均不变；
3. 把受理时刻推进到交接点 2026-03-10 00:00，各用一个新请求标识、数量 4 引用 `seat/v2` 和 `room/v1`，分别确认 720 分与 1200 分；
4. 再展示一次错误的替代登记：给 `seat` 登记 2026-03-20 开始的 `v3`（单价 200 分），却仍把 `v1` 填作替代来源——`seat/v1` 的实际有效期已止于 3 月 10 日，即使 `room/v1` 此时仍有效，也返回 `ErrInvalidReplacement`，失败后两项版本信息没有变化；
5. 顺带演示两种“找不到来源”：来源只存在于其他项时返回 `ErrReplaceTargetWrongItem`，所有项都没有该标识时返回 `ErrReplaceTargetNotFound`，两者同样不留痕迹。

```go
// 命令 crossitem 是“不同费率项使用同名版本时，怎样登记替代版本”的完整可运行示例：
// 同一本账本中 seat 与 room 各自登记名为 v1 的版本后，给 seat 登记替代 v1 的 v2——
// 替代来源只在本次登记指定的费率项内查找；随后再演示一次仍把本项已失效的 v1
// 填作替代来源的失败登记，以及两种“找不到来源”的错误。
//
// 运行：
//
//	go run ./examples/crossitem
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // seat/v1 与 room/v1 共同的生效起点
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 两个 v1 共同的登记结束（不含）
	v2Start = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // seat/v2 替代 seat/v1 的交接点
	v3Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // seat/v3 计划的交接点（seat/v1 的实际有效期已止于 3 月 10 日）
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ①② 同一本账本中登记两个费率项的同名版本 v1：
	//    seat/v1 单价 150 分、room/v1 单价 300 分，
	//    有效期都是 2026-03-01 至 2026-03-31（UTC 零点、结束时刻不含）。
	//    版本标识只在同一费率项内唯一，不同费率项可以复用同名版本。
	seatV1End := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v1",
		UnitPrice: 150,
		Start:     v1Start,
		End:       &seatV1End,
	}); err != nil {
		panic(err)
	}
	roomV1End := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "room",
		VersionID: "v1",
		UnitPrice: 300,
		Start:     v1Start,
		End:       &roomV1End,
	}); err != nil {
		panic(err)
	}
	printViews(book, "①② seat 与 room 各自登记 v1 后的版本视图")

	// ③ 给 seat 登记 v2：单价 180 分，2026-03-10 起替代 v1。
	//    Replaces 填的只是版本标识，替代来源按本次登记指定的费率项 seat 查找，
	//    被截短的只是 seat/v1，与 room 的同名 v1 无关。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v2",
		UnitPrice: 180,
		Start:     v2Start,
		Replaces:  "v1",
	}); err != nil {
		panic(err)
	}
	fmt.Println("③ 登记 seat/v2（2026-03-10 起替代 v1）：err=<nil>，登记生效")

	// ④ seat/v1 的登记结束仍是 3 月 31 日（登记时填写的值，永不改变），
	//    实际有效结束变为 3 月 10 日，并保留 seat 两版之间的替代关系；
	//    room/v1 的单价、两种结束时间和替代关系都不受另一项登记的影响。
	printViews(book, "④ seat/v2 登记后的版本视图")

	// ⑤⑥ 把受理时刻推进到交接点 2026-03-10 00:00（结束时刻不含该点），
	//     各用一个从未使用过的新请求标识、数量 4 引用 seat/v2 与 room/v1。
	now = v2Start

	seatQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-seat-v2-at-handoff",
		ItemID:    "seat",
		VersionID: "v2",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑤ 交接点用新标识引用 seat/v2（quote-seat-v2-at-handoff）", seatQuote)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 4 = 总价 %d 分\n",
		seatQuote.Confirmed, seatQuote.UnitPrice, seatQuote.Total)

	roomQuote, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-room-v1-at-handoff",
		ItemID:    "room",
		VersionID: "v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑥ 交接点用新标识引用 room/v1（quote-room-v1-at-handoff）", roomQuote)
	fmt.Printf("   Confirmed=%v，单价 %d 分 × 数量 4 = 总价 %d 分；room/v1 未被另一项的交接截短，仍按自己的单价确认\n",
		roomQuote.Confirmed, roomQuote.UnitPrice, roomQuote.Total)

	// ⑦ 错误的替代登记：给 seat 登记 2026-03-20 开始的 v3，单价 200 分，
	//    却仍把 v1 填作替代来源。seat/v1 的实际有效期已在 3 月 10 日被 v2 截短，
	//    3 月 20 日不在其当前的实际有效区间内；即使 room/v1 在 3 月 20 日仍然有效，
	//    也不能借另一项的同名版本通过交接时间校验。
	//    来源就在本项、只是已失效，因此返回 ErrInvalidReplacement，
	//    不能误报成“来源属于另一项”（ErrReplaceTargetWrongItem）。
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "v3",
		UnitPrice: 200,
		Start:     v3Start,
		Replaces:  "v1",
	})
	fmt.Printf("⑦ 登记 seat/v3（2026-03-20 开始，仍填 v1 作替代来源）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v；",
		errors.Is(err, tariff.ErrInvalidReplacement))
	fmt.Printf("errors.Is(err, tariff.ErrReplaceTargetWrongItem) = %v\n",
		errors.Is(err, tariff.ErrReplaceTargetWrongItem))

	// ⑧ 失败后两项的版本信息与④完全一致：seat 仍只有 v1、v2，room 仍只有 v1；
	//    各项的单价、两种结束时间和替代关系都没有变化，v3 未入库、标识未被占用。
	printViews(book, "⑧ 登记失败后的版本视图（应与④完全一致）")

	// ⑨ 本项找不到来源、其他项却有同名版本：room 下没有 v2，v2 只登记在 seat 下，
	//    返回 ErrReplaceTargetWrongItem——不会借用 seat 的 v2 完成 room 的登记。
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "room",
		VersionID: "v9",
		UnitPrice: 350,
		Start:     v3Start,
		Replaces:  "v2",
	})
	fmt.Printf("⑨ 给 room 登记 v9、替代来源填 v2（v2 只属于 seat）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrReplaceTargetWrongItem) = %v\n",
		errors.Is(err, tariff.ErrReplaceTargetWrongItem))

	// ⑩ 所有费率项都没有所填的标识：返回 ErrReplaceTargetNotFound。
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "room",
		VersionID: "v10",
		UnitPrice: 350,
		Start:     v3Start,
		Replaces:  "v99",
	})
	fmt.Printf("⑩ 给 room 登记 v10、替代来源填 v99（任何项都没有该标识）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrReplaceTargetNotFound) = %v\n",
		errors.Is(err, tariff.ErrReplaceTargetNotFound))

	// ⑪ ⑨⑩ 两次失败同样整次回滚，两项的版本信息仍与④完全一致。
	printViews(book, "⑪ ⑨⑩ 两次失败登记后的版本视图（仍与④完全一致）")
}

func printViews(book *tariff.Book, title string) {
	fmt.Printf("%s：\n", title)
	for _, itemID := range []string{"seat", "room"} {
		views, err := book.ItemVersions(itemID)
		if err != nil {
			panic(err)
		}
		for _, v := range views {
			fmt.Printf("   %s/%s：单价=%d 开始=%s 登记结束=%s 实际有效结束=%s 替代=%q 被替代=%q\n",
				v.ItemID, v.VersionID, v.UnitPrice, v.Start.Format(time.RFC3339),
				fmtEnd(v.End), fmtEnd(v.EffectiveEnd), v.Replaces, v.SupersededBy)
		}
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
①② seat 与 room 各自登记 v1 后的版本视图：
   seat/v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-31T00:00:00Z 替代="" 被替代=""
   room/v1：单价=300 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-31T00:00:00Z 替代="" 被替代=""
③ 登记 seat/v2（2026-03-10 起替代 v1）：err=<nil>，登记生效
④ seat/v2 登记后的版本视图：
   seat/v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="v2"
   seat/v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="v1" 被替代=""
   room/v1：单价=300 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-31T00:00:00Z 替代="" 被替代=""
⑤ 交接点用新标识引用 seat/v2（quote-seat-v2-at-handoff）：
   请求 item=seat version=v2 数量=4；首次受理时刻=2026-03-10T00:00:00Z
   结果=已确认 单价=180 分 总价=720 分
   Confirmed=true，单价 180 分 × 数量 4 = 总价 720 分
⑥ 交接点用新标识引用 room/v1（quote-room-v1-at-handoff）：
   请求 item=room version=v1 数量=4；首次受理时刻=2026-03-10T00:00:00Z
   结果=已确认 单价=300 分 总价=1200 分
   Confirmed=true，单价 300 分 × 数量 4 = 总价 1200 分；room/v1 未被另一项的交接截短，仍按自己的单价确认
⑦ 登记 seat/v3（2026-03-20 开始，仍填 v1 作替代来源）：err=tariff: new version start must be after the replaced version start and within its current effective interval
   errors.Is(err, tariff.ErrInvalidReplacement) = true；errors.Is(err, tariff.ErrReplaceTargetWrongItem) = false
⑧ 登记失败后的版本视图（应与④完全一致）：
   seat/v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="v2"
   seat/v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="v1" 被替代=""
   room/v1：单价=300 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-31T00:00:00Z 替代="" 被替代=""
⑨ 给 room 登记 v9、替代来源填 v2（v2 只属于 seat）：err=tariff: replaced version belongs to another item
   errors.Is(err, tariff.ErrReplaceTargetWrongItem) = true
⑩ 给 room 登记 v10、替代来源填 v99（任何项都没有该标识）：err=tariff: replaced version does not exist
   errors.Is(err, tariff.ErrReplaceTargetNotFound) = true
⑪ ⑨⑩ 两次失败登记后的版本视图（仍与④完全一致）：
   seat/v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="v2"
   seat/v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="v1" 被替代=""
   room/v1：单价=300 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-31T00:00:00Z 替代="" 被替代=""
```

对照输出即可判断同名版本互不影响：

- **①②** 两个费率项各自登记名为 `v1` 的版本，单价分别为 150 分和 300 分，互不构成冲突——版本标识只在同一费率项内唯一。
- **④** `seat/v2` 替代的是 `seat` 自己的 `v1`：`seat/v1` 的**登记结束**仍是 3 月 31 日，**实际有效结束**变为 3 月 10 日，并出现 `seat/v1 被替代="v2"`、`seat/v2 替代="v1"` 的关系；`room/v1` 的单价、登记结束、实际有效结束和替代关系（均为空）与 ①② 完全一致——另一项的同名版本既没有被截短，也没有被替代。
- **⑤⑥** 交接时刻两笔报价各用新标识、数量 4，来源分别明确写出 `item=seat version=v2` 与 `item=room version=v1`：前者按 180 分确认 720 分，后者按 `room/v1` 自己的 300 分确认 1200 分——`room/v1` 在交接时刻仍然有效，不受 `seat` 交接的影响。
- **⑦** 给 `seat` 登记 3 月 20 日开始的 `v3`、仍填 `v1` 作替代来源时，`seat/v1` 的实际有效期已止于 3 月 10 日，交接时刻落不在其内；即使 `room/v1` 在 3 月 20 日仍然有效，也不能借它通过校验——返回 `ErrInvalidReplacement`，且 `errors.Is(err, ErrReplaceTargetWrongItem)` 为 `false`：来源就在本项，不能误报成属于另一项。
- **⑧** 失败后的查询与 ④ 完全一致：`seat` 仍只有 `v1`、`v2`，`room` 仍只有 `v1`，各项的单价、两种结束时间和替代关系都没有变化，`v3` 未入库。
- **⑨⑩** 两种“找不到来源”要分开：本项没有、其他项有同名版本时返回 `ErrReplaceTargetWrongItem`；所有项都没有该标识时返回 `ErrReplaceTargetNotFound`。两者都是调用本身返回错误、整次回滚（⑪ 与 ④ 仍一致），不会借用其他项的版本完成登记。
