# 本地费率版本与报价

`tariff` 是一个在本机运行的费率版本与报价账本（`tariff.Book`），支持：

- 按费率项登记带整数分单价、生效起点和结束时刻（不含）的费率版本；
- 登记一个**替代旧版本**的新版本，账本自动把旧版本的实际有效期截断到交接时刻；
- 按费率项和**指定时刻**查询当时生效的单个版本（`EffectiveVersionAt`），选择依据是账本已登记的实际有效区间；
- 按费率项和**时间范围**列出这段期间生效过的版本（`VersionsInRange`），一次一个费率项，不必事先知道版本标识；
- 在指定时刻对指定版本报价，得到**首次受理结果**（确认并给出单价、总价，或带原因拒绝）；
- 按请求标识进行幂等重试和事后查询——费率变化后可据此核对一笔报价应沿用原请求标识还是发起新报价；
- 不必先知道请求标识，直接按**费率项**列出该项的全部首次受理结果（`ItemOutcomes`），确认与拒绝都在其中。

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

## 不知道请求标识时，怎样按费率项核对已受理报价

`Lookup` 必须先知道请求标识；查看费率版本时往往拿不出某笔报价的标识。账本提供另一个公开入口，按**费率项**取回当前账本中请求来源属于该项的全部首次受理结果：

```go
outcomes, err := book.ItemOutcomes("seat")
```

匹配与返回规则：

- **确认与拒绝都列出**：不因为引用的版本现在已经失效而筛掉记录，也不拿当前单价重算已保存的金额。旧版按 150 分、数量 4 确认的 600 分永远显示旧版、150 分和 600 分；交接后新请求引用同一旧版被拒绝时，记录显示旧版和 `version_expired`，拒绝结果里的零金额只是“没有可给出的金额”，不能当作免费确认价。
- **以已经保存的请求来源为准**：只匹配 `Outcome.Request.ItemID` 等于给定费率项的记录，其他费率项即使用了同名版本也不会混入。费率项**从未登记过版本**也不影响查询——已有合法数量的请求因 `version_not_found` 被拒绝时，这份拒绝同样列在其中，不能因为没有费率版本而让查询失败。
- **非空费率项标识没有任何匹配记录时返回空列表且不报错**（与按版本查询的 `ErrItemNotFound` 不同）；**空费率项标识**返回 `tariff.ErrEmptyItemIDQuery`，可用 `errors.Is` 明确识别这类输入错误。
- **同一份首次结果只出现一次**：相同标识、相同内容的原样重试只取回首次结果；相同标识、不同内容的冲突是调用错误、不产生记录；空请求标识的拒绝（`empty_request_id`）不保存。这三种情况都不会增加列表内容。
- **排序确定**：按首次受理时刻从早到晚排列；时刻相同时按请求标识的字符串顺序排列。
- **只读、独立**：该查询只读取当前账本已有的结果，不受理报价、不占用请求标识，`Quote`、`Lookup`、`ItemVersions`、`EffectiveVersionAt` 的规则都不受影响。返回的切片和其中每条记录都是独立副本，调用方改写后，后续单笔查询、原样重试和再次列表查询仍返回账本保存的原始结果。记录只保存在当前账本实例内，重新 `NewBook()` 后列表为空。

### 按费率项查看的完整示例

程序位于 [`examples/byitem/main.go`](examples/byitem/main.go)，可直接运行：

```bash
go run ./examples/byitem
```

时间线：旧版 `seat-v1` 单价 150 分，3 月 2 日按数量 4 确认 600 分；3 月 10 日登记 180 分的 `seat-v2` 替代旧版后，新请求引用旧版得到 `version_expired` 拒绝；另一费率项 `addon` 从未登记过版本，合法请求得到 `version_not_found` 拒绝。随后分别按 `seat`、`addon` 列示，并演示查无记录、空标识与副本独立性。

```go
// 命令 byitem 是“按费率项查看已受理报价”的完整可运行示例：
// 核对报价时不必先知道请求标识，直接给出费率项标识，即可取得当前账本中
// 请求来源属于该项的全部首次受理结果——确认与拒绝都列出，旧确认价不被
// 当前费率重算；费率项从未登记过版本时，version_not_found 的拒绝同样查得到。
//
// 运行：
//
//	go run ./examples/byitem
package main

import (
	"errors"
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

	// ① 登记旧版本 seat-v1：单价 150 分，登记有效期至 2026-03-31（不含）。
	oldEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		End:       &oldEnd,
	}); err != nil {
		panic(err)
	}

	// ② 2026-03-02 10:00，旧版有效期内，用 quote-001 按数量 4 报价，确认 600 分。
	now = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	first, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-001",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("② 旧版有效期内首次报价（quote-001）", first)

	// ③ 登记新版 seat-v2：单价 180，2026-03-10 起替代 seat-v1。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
		Replaces:  "seat-v1",
	}); err != nil {
		panic(err)
	}

	// ④ 交接后，新请求 quote-002 仍引用旧版，得到 version_expired 拒绝。
	now = time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-002",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("④ 交接后新请求引用旧版（quote-002）", expired)

	// ⑤ 另一费率项 addon 从未登记过任何版本；合法数量的请求因
	//    version_not_found 被拒绝，这份拒绝同样按费率项保存、可查。
	notFound, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-addon-1",
		ItemID:    "addon",
		VersionID: "addon-v1",
		Quantity:  1,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑤ 未登记版本的费率项首次报价（quote-addon-1）", notFound)

	// ⑥ 空请求标识的拒绝不保存；稍后的列表不应出现它。
	empty, err := book.Quote(tariff.QuoteRequest{
		ItemID: "seat", VersionID: "seat-v1", Quantity: 1,
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("⑥ 空请求标识报价：Confirmed=%v 原因=%s err=%v（该拒绝不保存）\n\n",
		empty.Confirmed, empty.Reason, err)

	// ⑦ 不必知道请求标识，直接按费率项 seat 取出全部首次受理结果。
	//    旧确认仍是旧版、150 分、600 分；失效拒绝的零金额不会被当成免费确认价。
	list, err := book.ItemOutcomes("seat")
	if err != nil {
		panic(err)
	}
	fmt.Printf("⑦ 按费率项 seat 查看已受理报价（共 %d 条，按首次受理时刻排列）：\n", len(list))
	for _, o := range list {
		printOutcome("  ·", o)
	}

	// ⑧ 从未登记过版本的 addon 也能查到 version_not_found 拒绝，
	//    不会因为费率项没有版本而让查询失败。
	addonList, err := book.ItemOutcomes("addon")
	if err != nil {
		panic(err)
	}
	fmt.Printf("⑧ 按费率项 addon 查看（共 %d 条，未登记版本也能查到拒绝）：\n", len(addonList))
	for _, o := range addonList {
		printOutcome("  ·", o)
	}

	// ⑨ 非空但没有任何匹配记录的费率项：返回空列表、不报错。
	none, err := book.ItemOutcomes("never-used")
	fmt.Printf("⑨ 查无记录的非空费率项 never-used：条数=%d err=%v\n\n", len(none), err)

	// ⑩ 空费率项标识：返回调用方可明确识别的输入错误。
	_, err = book.ItemOutcomes("")
	fmt.Printf("⑩ 空费率项标识：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrEmptyItemIDQuery) = %v\n\n",
		errors.Is(err, tariff.ErrEmptyItemIDQuery))

	// ⑪ 列表是独立副本：改写返回记录不影响后续单笔查询、原样重试与再次列表。
	list[0].Total = 1
	list[0].Confirmed = false
	list[0].Request.Quantity = 99
	got, err := book.Lookup("quote-001")
	if err != nil {
		panic(err)
	}
	fmt.Printf("⑪ 改写列表元素后再 Lookup（quote-001）：Confirmed=%v 数量=%d 单价=%d 总价=%d（仍是首次结果）\n",
		got.Confirmed, got.Request.Quantity, got.UnitPrice, got.Total)
	again, err := book.ItemOutcomes("seat")
	if err != nil {
		panic(err)
	}
	fmt.Printf("   再次按费率项查看仍为 %d 条，首条总价=%d 数量=%d，未被外部改写污染\n",
		len(again), again[0].Total, again[0].Request.Quantity)
}

func printOutcome(title string, o tariff.Outcome) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求标识=%s item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.RequestID, o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	if o.Confirmed {
		fmt.Printf("   结果=已确认 单价=%d 分 总价=%d 分\n\n", o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 原因=%s 单价=%d 总价=%d（零金额不是免费确认价）\n\n",
			o.Reason, o.UnitPrice, o.Total)
	}
}
```

输出（受理时刻由示例时钟推进到确定值，不依赖真实时间）：

```text
② 旧版有效期内首次报价（quote-001）：
   请求标识=quote-001 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-02T10:00:00Z
   结果=已确认 单价=150 分 总价=600 分

④ 交接后新请求引用旧版（quote-002）：
   请求标识=quote-002 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-11T09:00:00Z
   结果=被拒绝 原因=version_expired 单价=0 总价=0（零金额不是免费确认价）

⑤ 未登记版本的费率项首次报价（quote-addon-1）：
   请求标识=quote-addon-1 item=addon version=addon-v1 数量=1；首次受理时刻=2026-03-11T09:00:00Z
   结果=被拒绝 原因=version_not_found 单价=0 总价=0（零金额不是免费确认价）

⑥ 空请求标识报价：Confirmed=false 原因=empty_request_id err=<nil>（该拒绝不保存）

⑦ 按费率项 seat 查看已受理报价（共 2 条，按首次受理时刻排列）：
  ·：
   请求标识=quote-001 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-02T10:00:00Z
   结果=已确认 单价=150 分 总价=600 分

  ·：
   请求标识=quote-002 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-11T09:00:00Z
   结果=被拒绝 原因=version_expired 单价=0 总价=0（零金额不是免费确认价）

⑧ 按费率项 addon 查看（共 1 条，未登记版本也能查到拒绝）：
  ·：
   请求标识=quote-addon-1 item=addon version=addon-v1 数量=1；首次受理时刻=2026-03-11T09:00:00Z
   结果=被拒绝 原因=version_not_found 单价=0 总价=0（零金额不是免费确认价）

⑨ 查无记录的非空费率项 never-used：条数=0 err=<nil>

⑩ 空费率项标识：err=tariff: item id must not be empty
   errors.Is(err, tariff.ErrEmptyItemIDQuery) = true

⑪ 改写列表元素后再 Lookup（quote-001）：Confirmed=true 数量=4 单价=150 总价=600（仍是首次结果）
   再次按费率项查看仍为 2 条，首条总价=600 数量=4，未被外部改写污染
```

对照输出即可读懂这条查询口径：⑦ 中两笔记录都保留——旧确认仍显示旧版、150 分和 600 分，新拒绝显示旧版与 `version_expired`，后者的零金额不是确认价；⑤⑧ 说明查询以已保存的请求来源为准，没有费率版本也能查到拒绝；⑨⑩ 区分“查无记录”（空列表、`err == nil`）与“输入错误”（空标识、`ErrEmptyItemIDQuery`）；⑪ 证明返回结果是独立副本。

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

## 按时间范围查看生效版本

`EffectiveVersionAt` 回答“某个瞬间用哪一版”；核对**一段时间**的费率安排时，不必从 `ItemVersions` 的全部版本里自行挑选，使用账本的公开入口一次取回这段时间内生效过的版本列表：

```go
views, err := book.VersionsInRange("seat", from, to)
```

调用方给出**费率项、范围开始和结束时刻**，一次只查询一个费率项，不必事先知道版本标识。命中与返回规则：

- **范围为半开区间 `[from, to)`**：开始时刻包含、结束时刻不包含。只有版本的**实际有效区间** `[EffectiveStart, EffectiveEnd)` 与该范围存在交集时才列出；仅在边界处相接（版本结束即范围开始，或版本开始即范围结束）**不算命中**。持续有效（`EffectiveEnd == nil`）的版本按没有结束边界处理。
- **以查询时账本已登记的实际有效区间为准**：被替代的旧版按截短后的实际结束参与判断——它登记的结束仍在范围内也不会被列入；替代它的新版到期后旧版也不恢复。范围内的空档**不补入**相邻版本。范围可以在过去或未来，与账本时钟（`WithClock`）无关。
- **时间按实际瞬间比较**：同一瞬间换用不同时区表示，结论一致；选择只在指定费率项内进行，其他费率项即使有同名版本也不参与。
- **返回列表沿用现有版本视图**：版本标识、整数分单价、登记起止、实际有效区间和替代关系（`Replaces` / `SupersededBy`）都保留账本原值，**不会把版本边界改成查询范围的边界**。每个命中的版本只出现一次，按实际生效起点从早到晚排列。

例如同一项的旧版从 3 月 1 日开始、登记至 3 月 31 日，新版在 3 月 10 日替代它并于 3 月 20 日结束：查看 3 月 9 日至 3 月 11 日得到旧版和新版；查看 3 月 10 日至 3 月 25 日只得到新版；查看 3 月 20 日至 3 月 25 日为空列表。

### 错误与空结果要分开

- **结束时刻不晚于开始时刻**：一律返回 `tariff.ErrInvalidRange`（可用 `errors.Is` 判别），不返回版本列表——即使费率项从未登记过，范围错误也优先。
- **范围合法但费率项从未登记过版本**：返回 `tariff.ErrItemNotFound`（与 `ItemVersions`、`EffectiveVersionAt` 的既有错误相同）。
- **费率项存在但范围内没有命中版本**：返回空列表且不报错。

### 只读、独立

该查询只读取版本信息：**不受理报价、不占用请求标识**，也不改写费率或已保存的受理结果，`Quote`、`Lookup`、`ItemVersions`、`EffectiveVersionAt` 的既有规则都不受影响。返回的视图是独立副本，调用方改写其中的结束时间不会影响后续查询和报价。

### 按时间范围查看的完整示例

程序位于 [`examples/inrange/main.go`](examples/inrange/main.go)，可直接运行：

```bash
go run ./examples/inrange
```

所有日期均为 2026 年 UTC 零点、结束时刻不含；账本初始化和费率登记全部在代码中给出，读者无需自行补代码。`VersionsInRange` 的查询范围由调用方显式给出、`RegisterVersion` 不读账本时钟，因此示例直接使用 `tariff.NewBook()`（真实时钟），无论在哪一天运行，输出都确定、可复现。

时间线（同一费率项 `seat`）：

1. 旧版 `seat-v1`：单价 150 分，2026-03-01 00:00 起生效，登记结束为 2026-03-31 00:00；
2. 新版 `seat-v2`：单价 180 分，2026-03-10 00:00 起替代旧版，2026-03-20 00:00 结束——旧版的登记结束仍是 3 月 31 日，实际有效结束被截短到 3 月 10 日；
3. 分别查看三段范围：3 月 9 日至 11 日（跨过交接点）、3 月 10 日至 25 日（旧版仅与范围开始相接）、3 月 20 日至 25 日（新版仅与范围开始相接）；
4. 再演示两种调用失败：结束不晚于开始（`ErrInvalidRange`）、费率项从未登记（`ErrItemNotFound`），以及两者同时出现时范围错误优先。

```go
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
```

输出（查询范围由调用方显式给出，不依赖运行当天日期）：

```text
① 登记 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z)，err=<nil>
② 登记 seat-v2：单价 180 分，[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)，替代 seat-v1，err=<nil>
   两版登记完成后的版本视图：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代="seat-v2"
   seat-v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-20T00:00:00Z 实际有效结束=2026-03-20T00:00:00Z 替代="seat-v1" 被替代=""
③ 查看 seat 的 [2026-03-09, 2026-03-11)：跨过交接点，旧版、新版都命中：
   查询 item=seat 范围 [2026-03-09T00:00:00Z, 2026-03-11T00:00:00Z)，命中 2 个版本：
   · seat-v1：单价=150 分 登记起止=[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z) 实际有效区间=[2026-03-01T00:00:00Z, 2026-03-10T00:00:00Z)
   · seat-v2：单价=180 分 登记起止=[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z) 实际有效区间=[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)
④ 查看 seat 的 [2026-03-10, 2026-03-25)：旧版仅与范围开始相接，只列新版：
   查询 item=seat 范围 [2026-03-10T00:00:00Z, 2026-03-25T00:00:00Z)，命中 1 个版本：
   · seat-v2：单价=180 分 登记起止=[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z) 实际有效区间=[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)
⑤ 查看 seat 的 [2026-03-20, 2026-03-25)：新版仅与范围开始相接，没有命中版本：
   查询 item=seat 范围 [2026-03-20T00:00:00Z, 2026-03-25T00:00:00Z)，命中 0 个版本：
⑥ 查看 seat 的 [2026-03-11, 2026-03-09)（结束不晚于开始）：err=tariff: range end must be after range start
   errors.Is(err, tariff.ErrInvalidRange) = true；范围错误是调用失败，没有版本列表
⑦ 查看从未登记的 addon 的 [2026-03-09, 2026-03-11)：err=tariff: rate item not found
   errors.Is(err, tariff.ErrItemNotFound) = true
⑧ 查看 addon 的 [2026-03-11, 2026-03-09)（未登记 + 非法范围）：err=tariff: range end must be after range start
   errors.Is(err, tariff.ErrInvalidRange) = true；范围错误优先于费率项未登记
```

对照输出即可读懂这条查询口径：

- **③** 跨过交接点的范围按实际生效先后列出旧版和新版。注意旧版那一行：登记起止仍是 `[03-01, 03-31)`——返回的是账本保存的版本信息，起止时间**不会被裁成查询范围**；它参与判断用的是**实际有效区间** `[03-01, 03-10)`，所以实际结束才是 3 月 10 日。
- **④** 旧版的实际结束（3 月 10 日）恰好等于范围开始，仅在边界处相接不算命中，因此只列新版；范围后半段（3 月 20 日之后）是没有版本生效的空档，账本不会补入邻近的旧版或任何其他版本。
- **⑤** 新版的结束（3 月 20 日）恰好等于范围开始，同样不算命中；新版结束后旧版也不会恢复。费率项存在但范围内没有命中版本时，返回**空列表且 `err` 为 `nil`**——这是正常空结果，与 ⑥⑦⑧ 的调用失败不是一回事：空结果不能当成失败处理，⑥⑦⑧ 的失败也不能显示成“没有生效版本”。
- **⑥⑦⑧** 区分两种调用失败：结束不晚于开始返回 `ErrInvalidRange` 且不给出版本列表；范围合法但费率项从未登记返回 `ErrItemNotFound`；两者同时出现时范围错误优先。三者都不是“空列表”，调用方应用 `errors.Is` 分别识别。

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

## 总价为零时怎样判断报价是否成立

上文各节都强调：被拒绝的结果中单价和总价都是零，零金额只是“没有可给出的金额”。但费率本身也允许登记为零——`RegisterRequest.UnitPrice` 可以为零（不能为负），零单价版本确认一笔报价后，结果中的单价和总价**同样都是零**。于是“单价为零、总价为零”对应两种完全不同的情况：

- **已确认的零价报价**：`Confirmed == true`、`Reason` 为空，单价零来自登记的零单价，总价零是 `0 × 数量` 的真实计算结果；
- **没有可用金额的拒绝**：`Confirmed == false`、`Reason` 给出拒绝原因，单价和总价为零只是因为账本没有可给出的金额。

因此**不能仅凭金额判断报价是否成立，也不能仅凭调用没有返回错误判断**——拒绝的调用错误同样是 `nil`。唯一的判断依据是 `Confirmed` 与 `Reason`：`Confirmed == true` 且 `Reason` 为空才是已确认的零价报价；`Confirmed == false` 时按 `Reason` 处理拒绝。

零单价不放宽任何其他受理规则：

- **数量仍必须是正整数**：数量为零或负数同样得到 `invalid_quantity` 拒绝，拒绝结果中原样保留提交的原始数量；
- **有效期规则不变**：开始时刻含、结束时刻不含，到期后引用该版本仍得到 `version_expired`，零单价不会让已到期版本继续接受新报价；
- **零单价不会因数量很大而溢出**：`0 × 数量` 恒为零，数量取有符号 64 位整数最大值时依然确认，总价为零不表示溢出，也不表示数量没有被记录——数量保存在 `Outcome.Request.Quantity` 中，与请求一致。

### 同一零单价版本三笔报价的完整示例

程序位于 [`examples/zeroprice/main.go`](examples/zeroprice/main.go)，可直接运行：

```bash
go run ./examples/zeroprice
```

所有日期均为 2026 年 UTC 零点、结束时刻不含；账本初始化、费率登记和受理时刻全部在代码中给出，并通过公开选项 `tariff.WithClock` 注入可手动推进的时钟，因此无论在哪一天运行，输出都确定、可复现，读者无需补写初始化、费率登记或受理时间设置，也无需等待真实时间流逝。生产环境直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一费率项 `seat` 的唯一版本 `seat-v1`，单价 0 分，2026-03-01 00:00 起生效、2026-03-31 00:00 结束）：

1. 生效起点（3 月 1 日 00:00，含在有效期内）以数量 `math.MaxInt64` 报价，得到已确认、单价总价均为零的结果；
2. 有效期内（3 月 15 日 00:00）以数量 0 报价，得到 `invalid_quantity` 拒绝；
3. 结束时刻（3 月 31 日 00:00，不含在有效期内）以合法数量 1 报价，得到 `version_expired` 拒绝。

每笔报价使用各自从未使用过的非空请求标识，输出反映的都是首次受理的判断。

```go
// 命令 zeroprice 是“总价为零时怎样判断报价是否成立”的完整可运行示例：
// 同一个零单价版本 seat-v1（单价 0 分）在生效起点以有符号 64 位整数最大值
// 为数量报价，得到已确认、单价和总价均为零的结果；有效期内换标识提交数量零，
// 得到 invalid_quantity 拒绝；结束时刻再换标识提交合法正数量，得到
// version_expired 拒绝。三笔结果的单价和总价都是零、调用错误都是 nil，
// 只有 Confirmed 与 Reason 能区分“已确认的零价报价”和“没有可用金额的拒绝”。
//
// 运行：
//
//	go run ./examples/zeroprice
package main

import (
	"fmt"
	"math"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // 零价版生效起点（含）
	v1End   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) // 零价版结束时刻（不含）
)

func main() {
	// 演示时钟：受理时刻取自变量 now，推进它即可让同一本账本走到不同时期。
	// 生产环境直接 tariff.NewBook() 即使用真实的 time.Now；
	// 这里传入 tariff.WithClock 只是为了让示例输出确定、可复现，无需等待真实时间。
	now := v1Start
	book := tariff.NewBook(tariff.WithClock(func() time.Time { return now }))

	// ① 登记零单价版本 seat-v1：单价为零是合法登记（单价可以为零、不能为负），
	//    2026-03-01 00:00 起生效（含），2026-03-31 00:00 结束（不含）。
	v1RegisteredEnd := v1End
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 0,
		Start:     v1Start,
		End:       &v1RegisteredEnd,
	}); err != nil {
		panic(err)
	}
	printViews(book, "① 零单价版本登记完成后的版本视图")

	// 以下每笔报价都使用各自从未使用过的非空请求标识，
	// 因此输出反映的都是首次受理的判断。

	// ② 生效起点（2026-03-01 00:00，开始时刻含在有效期内）提交数量为
	//    math.MaxInt64 的报价：单价为零时 0×MaxInt64 仍是零，不会溢出，
	//    得到已确认结果——单价、总价均为零，拒绝原因为空，
	//    结果中的费率项、版本、数量和受理时刻与本次请求一致。
	now = v1Start
	confirmed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zeroprice-at-start",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  math.MaxInt64,
	})
	printOutcome("② 生效起点提交最大数量（zeroprice-at-start）", confirmed, err)

	// ③ 有效期内（2026-03-15 00:00）换一个新标识提交数量零：
	//    零单价不放宽数量必须为正整数的要求（负数量同样不合法），
	//    得到 invalid_quantity 拒绝，单价、总价仍为零，
	//    提交的原始数量 0 原样保留在结果中。
	now = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	zeroQty, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zeroprice-quantity-zero",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  0,
	})
	printOutcome("③ 有效期内提交数量零（zeroprice-quantity-zero）", zeroQty, err)

	// ④ 结束时刻（2026-03-31 00:00，结束时刻不含在有效期内）再换一个新标识
	//    提交合法正数量：版本已到期，得到 version_expired 拒绝——
	//    零单价不会让已到期版本继续接受新报价。
	now = v1End
	expired, err := book.Quote(tariff.QuoteRequest{
		RequestID: "zeroprice-at-end",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  1,
	})
	printOutcome("④ 结束时刻提交合法数量（zeroprice-at-end）", expired, err)
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

func printOutcome(title string, o tariff.Outcome, err error) {
	fmt.Printf("%s：\n", title)
	fmt.Printf("   请求 item=%s version=%s 数量=%d；首次受理时刻=%s\n",
		o.Request.ItemID, o.Request.VersionID, o.Request.Quantity,
		o.AcceptedAt.Format(time.RFC3339))
	fmt.Printf("   调用错误 err=%v\n", err)
	if o.Confirmed {
		fmt.Printf("   结果=已确认 Confirmed=true 原因=%q 单价=%d 分 总价=%d 分\n",
			string(o.Reason), o.UnitPrice, o.Total)
	} else {
		fmt.Printf("   结果=被拒绝 Confirmed=false 原因=%s 单价=%d 总价=%d\n",
			o.Reason, o.UnitPrice, o.Total)
	}
}
```

输出（受理时刻由示例时钟推进到确定值，不依赖真实日期）：

```text
① 零单价版本登记完成后的版本视图：
   seat-v1：单价=0 登记结束=2026-03-31T00:00:00Z 实际有效结束=2026-03-31T00:00:00Z 替代="" 被替代=""
② 生效起点提交最大数量（zeroprice-at-start）：
   请求 item=seat version=seat-v1 数量=9223372036854775807；首次受理时刻=2026-03-01T00:00:00Z
   调用错误 err=<nil>
   结果=已确认 Confirmed=true 原因="" 单价=0 分 总价=0 分
③ 有效期内提交数量零（zeroprice-quantity-zero）：
   请求 item=seat version=seat-v1 数量=0；首次受理时刻=2026-03-15T00:00:00Z
   调用错误 err=<nil>
   结果=被拒绝 Confirmed=false 原因=invalid_quantity 单价=0 总价=0
④ 结束时刻提交合法数量（zeroprice-at-end）：
   请求 item=seat version=seat-v1 数量=1；首次受理时刻=2026-03-31T00:00:00Z
   调用错误 err=<nil>
   结果=被拒绝 Confirmed=false 原因=version_expired 单价=0 总价=0
```

对照输出即可区分“已确认的零价报价”和“没有可用金额的拒绝”：

- **①** 版本视图确认登记的单价就是 0 分，不是“没有金额”的占位；
- **②** 生效起点提交最大数量：`Confirmed=true`、原因为空，单价 0 分、总价 0 分是**确认结果**——`0 × MaxInt64` 恒为零，零单价不会因数量很大而溢出；数量 `9223372036854775807` 原样记录在结果的请求来源中，零总价不表示数量没有被记录；结果中的费率项、版本、数量和首次受理时刻（2026-03-01T00:00:00Z，即生效起点）都与这次请求一致；
- **③** 有效期内提交数量零：`Confirmed=false`、原因 `invalid_quantity`——零单价不放宽数量必须为正整数的要求（负数量同样不合法）；单价、总价仍为零，但这里的零只是“没有可给出的金额”；提交的原始数量 0 原样保留在结果的请求来源中；
- **④** 结束时刻提交合法数量：`Confirmed=false`、原因 `version_expired`——结束时刻不含在有效期内，零单价不会让已到期版本继续接受新报价；
- **②③④** 三笔的调用错误都是 `<nil>`、单价和总价都是零：金额和 `err == nil` 都无法区分确认与拒绝，只有 `Confirmed` 与 `Reason` 能区分——② 是已确认的零价报价，③④ 是没有可用金额的拒绝。

## 数量填错后怎样重新报价

`Quantity` 必须是**正整数**。使用一个从未用过的非空请求标识首次提交时，若数量为零或负数：

- 调用错误（`error`）为**空**，返回的是一份正常受理但**被拒绝**的结果：`Confirmed == false`、`Reason == invalid_quantity`、单价和总价均为零；
- 结果仍**原样保留**提交的数量（零就是零、负数不会被改成零）、费率项、版本，以及首次受理时刻。这份首次拒绝与首次确认一样会被保存，之后可按标识 `Lookup` 或原样重试取回。

判断顺序是固定的：**数量问题先于版本问题**。即使指定版本在受理时刻同时尚未生效、已经失效或根本不存在，这种首次请求的原因仍是 `invalid_quantity`，账本不会继续检查版本。因此看到 `invalid_quantity` **不能推断该版本一定可用**（这次受理根本没有判断版本）；结果里的零金额也只是“没有可给出的金额”，**不能理解为免费报价**。要单独验证版本是否可用，需用合法数量、另换标识再报一次（对照见上文 `total_overflow` 一节及 `tariff` 包内的数量优先级测试）。

数量填错后，三步关系要分清：

1. **查询原记录**：`Lookup(原标识)` 取回的就是首次拒绝，原始数量和首次受理时刻不变；
2. **原样重试**：相同标识、相同内容（含错误数量）再次提交，仍返回同一份首次拒绝，不会按新的当前时刻重新受理；
3. **重新报价必须换标识**：沿用原标识只把数量改正，属于“相同标识、不同内容”，调用本身返回 `tariff.ErrRequestIDConflict`，**没有可使用的新报价结果**，原拒绝记录也不被覆盖、仍能查到。只有换一个**从未用过的新标识**提交正确数量，才会发起一笔新的首次报价。

换新标识并不会绕开账本的受理规则：它只是发起一笔全新的首次报价，新版本是否有效、金额是否溢出，仍按上文既有的版本有效期与金额规则判断。

### 一次数量填错、查询、冲突、换标识重报的完整示例

程序位于 [`examples/requote/main.go`](examples/requote/main.go)，可直接运行：

```bash
go run ./examples/requote
```

所有日期与时刻均为 2026 年 UTC、结束时刻不含；账本初始化、费率登记和受理时刻全部在代码中给出，并通过公开选项 `tariff.WithClock` 注入可手动推进的时钟，因此无论在哪一天运行、处于什么本机时区，输出都确定、可复现，读者无需另外补写初始化代码。生产环境直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一本账本、同一个费率项 `seat` 和版本 `seat-v1`）：

1. 登记 `seat-v1`：单价 150 分，2026-03-01 生效，登记结束为 2026-03-31；
2. 在明确的受理时刻 2026-03-02 10:00（版本有效期内），用一个非空新标识 `quote-wrong-qty` 提交数量 0，得到 `invalid_quantity` 拒绝；
3. 把时钟拨到 3 月 3 日后，按该标识 `Lookup`、再原样提交一次，都取回 ② 那份首次拒绝——原数量 0 与首次受理时刻 3 月 2 日 10:00 保持不变；
4. 只把数量改为 4、沿用原标识提交，返回 `ErrRequestIDConflict`，没有可使用的新报价结果；原拒绝记录仍能查到；
5. 换成另一个从未用过的标识 `quote-right-qty`，以数量 4 引用同一有效版本，确认单价 150 分、总价 600 分，来源和数量都与这次新请求一致。

```go
// 命令 requote 是“数量填错后怎样重新报价”的完整可运行示例：
// 用新标识首次提交数量 0 得到 invalid_quantity 拒绝后，演示查询与原样重试
// 取回的都是同一份首次拒绝；沿用原标识改数量会得到 ErrRequestIDConflict，
// 原拒绝不被覆盖；只有换用从未用过的新标识，才会发起一笔新的首次报价。
//
// 运行：
//
//	go run ./examples/requote
package main

import (
	"errors"
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

	// ① 登记有效版本 seat-v1：单价 150 分/单位，
	//    有效期为 2026-03-01 至 2026-03-31（UTC，结束时刻不含）。
	v1End := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v1",
		UnitPrice: 150,
		Start:     time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		End:       &v1End,
	}); err != nil {
		panic(err)
	}
	fmt.Println("① 登记有效版本 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z)，err=<nil>")

	// ② 2026-03-02 10:00（版本有效期内），用一个从未用过的非空标识提交数量 0。
	//    数量必须是正整数；这次调用的 err 为 nil，但结果是原因 invalid_quantity 的拒绝，
	//    确认状态为否、单价和总价均为零，请求里的原始数量、费率项、版本原样保留。
	now = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	wrongReq := tariff.QuoteRequest{
		RequestID: "quote-wrong-qty",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  0,
	}
	rejected, err := book.Quote(wrongReq)
	if err != nil {
		panic(err)
	}
	printOutcome("② 首次提交数量 0（quote-wrong-qty）", rejected)
	fmt.Printf("   err=%v：拒绝已被保存，但它是正常受理结果，不是调用错误；零金额也不是免费报价\n", err)

	// 把时钟拨到另一个时刻，证明查询与重试取回的受理时刻不会被重算。
	now = time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)

	// ③ 按原标识查询：非空标识的首次拒绝也已保存，取回的就是②那份记录。
	got, err := book.Lookup("quote-wrong-qty")
	if err != nil {
		panic(err)
	}
	printOutcome("③ 按原标识 Lookup（quote-wrong-qty）", got)
	fmt.Printf("   与首次拒绝完全一致：%v\n", got == rejected)

	// ④ 原样再次提交（相同标识、相同内容）：仍返回首次拒绝，
	//    原数量 0 与首次受理时刻 2026-03-02 10:00 保持不变。
	replay, err := book.Quote(wrongReq)
	if err != nil {
		panic(err)
	}
	printOutcome("④ 原样再次提交（quote-wrong-qty）", replay)
	fmt.Printf("   与首次拒绝完全一致：%v（数量仍为 %d，受理时刻仍是 %s，没有按 3 月 3 日重新受理）\n",
		replay == rejected, replay.Request.Quantity, replay.AcceptedAt.Format(time.RFC3339))

	// ⑤ 只把数量改成 4、沿用原标识提交：请求内容与首次不同，
	//    调用本身返回 ErrRequestIDConflict，没有可使用的新报价结果。
	_, err = book.Quote(tariff.QuoteRequest{
		RequestID: "quote-wrong-qty",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	fmt.Printf("⑤ 沿用原标识、只把数量改为 4：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrRequestIDConflict) = %v；这次调用没有可使用的报价结果\n",
		errors.Is(err, tariff.ErrRequestIDConflict))

	// ⑥ 冲突不会覆盖任何记录：原拒绝仍能查到，原因、原始数量、金额与受理时刻都不变。
	still, err := book.Lookup("quote-wrong-qty")
	if err != nil {
		panic(err)
	}
	printOutcome("⑥ 冲突后再查原标识（quote-wrong-qty）", still)
	fmt.Printf("   原拒绝未被覆盖：原因=%s 数量=%d 单价=%d 总价=%d 受理时刻=%s\n",
		still.Reason, still.Request.Quantity, still.UnitPrice, still.Total,
		still.AcceptedAt.Format(time.RFC3339))

	// ⑦ 改用另一个从未用过的标识、数量 4 引用同一有效版本：
	//    这是一笔新的首次报价，按 150 分确认总价 600 分，
	//    来源（费率项、版本）和数量都来自这次新请求，受理时刻是本次时刻。
	fixed, err := book.Quote(tariff.QuoteRequest{
		RequestID: "quote-right-qty",
		ItemID:    "seat",
		VersionID: "seat-v1",
		Quantity:  4,
	})
	if err != nil {
		panic(err)
	}
	printOutcome("⑦ 换新标识、数量 4 重新报价（quote-right-qty）", fixed)
	fmt.Printf("   Confirmed=%v；来源 item=%s version=%s 与数量=%d 均属本次新请求，单价 %d 分 × 4 = 总价 %d 分\n",
		fixed.Confirmed, fixed.Request.ItemID, fixed.Request.VersionID, fixed.Request.Quantity,
		fixed.UnitPrice, fixed.Total)
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

输出（受理时刻由示例时钟推进到确定值，不依赖运行当天日期或本机时区）：

```text
① 登记有效版本 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-31T00:00:00Z)，err=<nil>
② 首次提交数量 0（quote-wrong-qty）：
   请求 item=seat version=seat-v1 数量=0；首次受理时刻=2026-03-02T10:00:00Z
   结果=被拒绝 原因=invalid_quantity 单价=0 总价=0（err 为 nil，拒绝不是调用错误）
   err=<nil>：拒绝已被保存，但它是正常受理结果，不是调用错误；零金额也不是免费报价
③ 按原标识 Lookup（quote-wrong-qty）：
   请求 item=seat version=seat-v1 数量=0；首次受理时刻=2026-03-02T10:00:00Z
   结果=被拒绝 原因=invalid_quantity 单价=0 总价=0（err 为 nil，拒绝不是调用错误）
   与首次拒绝完全一致：true
④ 原样再次提交（quote-wrong-qty）：
   请求 item=seat version=seat-v1 数量=0；首次受理时刻=2026-03-02T10:00:00Z
   结果=被拒绝 原因=invalid_quantity 单价=0 总价=0（err 为 nil，拒绝不是调用错误）
   与首次拒绝完全一致：true（数量仍为 0，受理时刻仍是 2026-03-02T10:00:00Z，没有按 3 月 3 日重新受理）
⑤ 沿用原标识、只把数量改为 4：err=tariff: request id already used with different content
   errors.Is(err, tariff.ErrRequestIDConflict) = true；这次调用没有可使用的报价结果
⑥ 冲突后再查原标识（quote-wrong-qty）：
   请求 item=seat version=seat-v1 数量=0；首次受理时刻=2026-03-02T10:00:00Z
   结果=被拒绝 原因=invalid_quantity 单价=0 总价=0（err 为 nil，拒绝不是调用错误）
   原拒绝未被覆盖：原因=invalid_quantity 数量=0 单价=0 总价=0 受理时刻=2026-03-02T10:00:00Z
⑦ 换新标识、数量 4 重新报价（quote-right-qty）：
   请求 item=seat version=seat-v1 数量=4；首次受理时刻=2026-03-03T09:00:00Z
   结果=已确认 单价=150 分 总价=600 分
   Confirmed=true；来源 item=seat version=seat-v1 与数量=4 均属本次新请求，单价 150 分 × 4 = 总价 600 分
```

对照输出即可分清“拒绝被保存”和“调用返回错误”，并理解改正数量不会覆盖已受理的记录：

- **②** `err` 为 `<nil>`、`Confirmed=false`、原因 `invalid_quantity`：这是**被保存的拒绝**，不是调用错误。数量原样保留为 0，单价、总价都是零——零金额表示“没有可给出的金额”，不是按 150 分免费报价。
- **③④** 即使受理时钟已拨到 3 月 3 日，`Lookup` 和原样重试取回的仍是 ② 那份首次拒绝：数量 0、首次受理时刻 3 月 2 日 10:00，完全一致。原样重试不是重新受理，只是取回历史结果。
- **⑤** 是**调用本身返回错误**（`ErrRequestIDConflict`）：相同标识改动了数量，账本不受理新内容，也没有任何报价结果可用——与 ② 的“报价被拒绝”是两类不同的“没成功”。
- **⑥** 证明冲突不留改动：原拒绝记录仍能查到，原因、原始数量 0、零金额和首次受理时刻全部不变。改正数量不会覆盖已经受理的记录。
- **⑦** 只有换用从未用过的新标识才发起新的首次报价：这次来源（`seat` / `seat-v1`）与数量 4 都属于新请求，版本有效，才确认单价 150 分、总价 600 分，受理时刻是本次的 3 月 3 日 09:00。新标识并不豁免任何规则——这笔新报价仍受版本有效期与金额规则约束，版本不可用或总价溢出时照样会被拒绝。

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

## 填补费率版本之间的空档：Replaces 留空的登记

前面几节的登记都在**替代旧版本**；还有一种常见情形：同一费率项的两段实际有效区间互不相邻、中间留着空档，事后补登记一个版本把空档填满。**填补空档不需要替代任何版本**：被补版本两侧的旧版各自保留登记起止、实际有效区间和替代关系，正确的登记方式是把新区间写成恰好填满空档、`Replaces` **留空**。

选择登记方式时抓住以下规则：

- **判定只看实际有效区间是否重叠，与登记顺序无关**：新版本的区间 `[Start, End)` 必须与同项每个既有版本的实际有效区间都不重叠；两端与邻版的边界落在同一瞬间是允许的——半开区间里一个版本的结束时刻与另一个版本的开始时刻相同属于**相接，不算重叠**（结束时刻不含）。版本列表始终按**生效时间**排列，因此先登记晚生效的版本、再回头补中间版本没有任何问题。
- **空档在补登前真实存在**：补登之前用 `EffectiveVersionAt` 查询空档时刻返回 `tariff.ErrNoEffectiveVersion`（见上文“按指定时刻查询生效版本”），账本不会拿时间上邻近的版本补位；补登成功后，同一时刻返回新补登的版本。
- **两侧旧版的边界都不移动**：没有 `Replaces` 就没有截断。补登成功后，左侧版本的结束、右侧版本的开始都保持原值，三版的 `Replaces` / `SupersededBy` 全部为空——时间相接不等于发生替代。

### 两个容易填错的条件（就在这次操作旁边）

- **不填结束时间会延伸进后一版的有效期，返回 `ErrOverlap`**：`End` 为 `nil` 表示持续有效，账本**不会自动以后一版的开始作为它的结束**。例如空档右侧的 v3 从 3 月 20 日起持续有效，补登的 v2 若只给 3 月 10 日开始、不填结束，区间就是 `[03-10, ∞)`，与 v3 的 `[03-20, ∞)` 重叠，登记失败。失败整次回滚：账本仍只有原来两版，空档查询仍返回 `ErrNoEffectiveVersion`；失败登记不占用版本标识，把结束补为 3 月 20 日后仍可用同一标识登记。
- **把左侧版本填进 `Replaces` 会返回 `ErrInvalidReplacement`**：相接不等于替代。一旦填写 `Replaces`，新版本的 `Start` 就必须严格落在被替代版本**当前的实际有效区间** `[EffectiveStart, EffectiveEnd)` 内；当 `Start` 恰好等于左侧版本的实际结束时刻时，该点已经不在旧版有效期内（结束时刻不含），不能把“接在旧版后面”当作替代。补空档时把 `Replaces` 留空即可——既不需要、也不允许借替代来“接续”。

### 一次先两次失败、再正确补齐空档的完整示例

程序位于 [`examples/gapfill/main.go`](examples/gapfill/main.go)，可直接运行：

```bash
go run ./examples/gapfill
```

所有日期均为 2026 年 UTC 零点、结束时刻不含；账本初始化和费率数据全部在代码中给出。本示例只登记版本并按指定时刻查询、不调用 `Quote`：`RegisterVersion` 不读取账本时钟，`EffectiveVersionAt` 的查询时刻由调用方显式给出，所以即使直接 `tariff.NewBook()`（真实时钟），无论在哪一天运行输出都确定、可复现，读者无需注入时钟或补写初始化代码。生产环境其他场景直接 `tariff.NewBook()` 即使用真实时间。

时间线（同一费率项 `seat`，**登记顺序是 v3、v1、v2**，而非按生效日期排列）：

1. `seat-v3`：单价 200 分，2026-03-20 起持续有效（不填结束时间），`Replaces` 留空；
2. `seat-v1`：单价 150 分，2026-03-01 开始、2026-03-10 结束（不含），`Replaces` 留空——两版之间在 `[03-10, 03-20)` 留下空档；
3. 补登前查询：3 月 9 日返回 v1，3 月 15 日返回 `ErrNoEffectiveVersion`（不拿邻近版本补位），3 月 20 日 00:00（开始时刻含）已返回 v3；
4. 第一次补登 `seat-v2`：单价 180 分、3 月 10 日开始，**不填结束时间**——区间延伸进 v3 的有效期，返回 `ErrOverlap`，账本不会自动以 v3 的开始作为结束；
5. 第二次补登 `seat-v2`：把结束补为 3 月 20 日，却把 `seat-v1` 填进 `Replaces`——3 月 10 日已经不在 v1 的实际有效期 `[03-01, 03-10)` 内，返回 `ErrInvalidReplacement`；
6. 两次失败后账本都仍只有 v1、v3，空档查询结论不变；第三次沿用标识 `seat-v2`，区间 `[03-10, 03-20)`、`Replaces` 留空，登记成功。

```go
// 命令 gapfill 是“填补费率版本之间的空档”的完整可运行示例：
// 先登记的两版 seat-v3（2026-03-20 起持续有效）与 seat-v1
// （2026-03-01 至 2026-03-10）在 [03-10, 03-20) 之间留下空档，
// 查询空档得到 ErrNoEffectiveVersion，账本不会拿邻近版本补位。
// 补登 seat-v2 时，第一次不填结束时间会延伸进 v3 的有效期，返回 ErrOverlap，
// 账本不会自动以 v3 的开始作为结束；第二次把 v1 填进 Replaces，
// 但 3 月 10 日已经不在 v1 的实际有效期内，返回 ErrInvalidReplacement，
// “接在旧版后面”不是替代。两次失败后账本都仍是原来的两版。
// 最后把 v2 登记为 [2026-03-10, 2026-03-20)、Replaces 留空，恰好补齐空档：
// 不截断、不替代任何旧版，三版按生效时间排列为 v1、v2、v3。
//
// 运行：
//
//	go run ./examples/gapfill
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bengzyyys/tariff-book/tariff"
)

// 下述日期均指 2026 年 UTC 零点，结束时刻不含。
var (
	v1Start = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)  // v1 生效起点（含）
	v1End   = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // v1 结束时刻（不含），也是空档起点
	v2Start = v1End
	v2End   = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // v2 结束时刻（不含），也是 v3 起点
	v3Start = v2End
	gapAt   = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // 空档内部
)

func main() {
	// 本示例只登记版本并按指定时刻查询，不调用 Quote：
	// RegisterVersion 不读账本时钟，EffectiveVersionAt 的查询时刻由调用方显式给出，
	// 因此即使直接 NewBook()（真实时钟），输出也完全确定、与运行当天无关。
	book := tariff.NewBook()

	// ① 先登记 seat-v3：单价 200 分，2026-03-20 起持续有效（不填结束时间），
	//    不替代任何版本（Replaces 留空）。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v3",
		UnitPrice: 200,
		Start:     v3Start,
	}); err != nil {
		panic(err)
	}
	fmt.Println("① 登记 seat-v3：单价 200 分，2026-03-20T00:00:00Z 起持续有效，Replaces 留空，err=<nil>")

	// ② 再登记 seat-v1：单价 150 分，2026-03-01 生效，2026-03-10 结束（不含），
	//    同样不替代任何版本。登记顺序是先 v3 后 v1。
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
	fmt.Println("② 登记 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-10T00:00:00Z)，Replaces 留空，err=<nil>")

	// ③ 版本列表按生效时间排列为 v1、v3，而不是登记顺序 v3、v1；
	//    两版的登记起止与实际有效区间一致，替代与被替代关系均为空。
	printViews(book, "③ 两版登记完成后的版本视图（按生效时间排列，而非登记顺序）")

	// ④ 补登前按时刻查询：
	//    3 月 9 日落在 v1 内；3 月 15 日落在两版之间的空档 [03-10, 03-20)，
	//    返回 ErrNoEffectiveVersion——账本不会拿时间上邻近的 v1 或 v3 补位；
	//    3 月 20 日（开始时刻含）已是 v3。
	fmt.Println("④ 补登 v2 前按时刻查询：")
	printAt(book, time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	printAt(book, gapAt)
	printAt(book, v3Start)

	// ⑤ 第一次尝试补登 seat-v2：单价 180 分，2026-03-10 起，Replaces 留空，
	//    但不填结束时间。它的实际有效区间会是 [03-10, ∞)，延伸进 v3 的
	//    [03-20, ∞)；填补空档不需要替代任何版本，账本也不会自动以 v3 的开始
	//    作为 v2 的结束，因此返回 ErrOverlap。
	err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
	})
	fmt.Printf("⑤ 登记 seat-v2（不填结束时间，Replaces 留空）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrOverlap) = %v；登记失败是调用本身返回错误，整次登记未生效\n",
		errors.Is(err, tariff.ErrOverlap))

	// ⑥ 失败后整次回滚：仍只有 v1、v3 两个版本，v2 未入库、标识未被占用，
	//    v1 的结束（3 月 10 日）和 v3 的开始（3 月 20 日）都保持原值、关系全空；
	//    空档查询的结论不变。
	printViews(book, "⑥ 第一次失败后的版本视图（仍只有 v1、v3，边界与关系不变）")
	printAt(book, gapAt)

	// ⑦ 第二次尝试补登 seat-v2：这次把结束时间填为 3 月 20 日，却把 v1 填进
	//    Replaces。v1 的实际有效区间是 [03-01, 03-10)，3 月 10 日恰好等于它的
	//    实际结束时刻——结束时刻不含，该点已经不在 v1 的实际有效期内。
	//    v2 只是时间上接在 v1 后面，并不替代 v1，因此返回 ErrInvalidReplacement。
	v2RegisteredEnd := v2End
	err = book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2RegisteredEnd,
		Replaces:  "seat-v1",
	})
	fmt.Printf("⑦ 登记 seat-v2（结束=2026-03-20，但 Replaces 填 seat-v1）：err=%v\n", err)
	fmt.Printf("   errors.Is(err, tariff.ErrInvalidReplacement) = %v；"+
		"3 月 10 日不在 v1 的实际有效期 [03-01, 03-10) 内，“接在旧版后面”不是替代\n",
		errors.Is(err, tariff.ErrInvalidReplacement))

	// ⑧ 第二次失败同样整次回滚：账本仍只有 v1、v3，边界与替代关系不变。
	printViews(book, "⑧ 第二次失败后的版本视图（仍只有 v1、v3，边界与关系不变）")

	// ⑨ 第三次用同一个标识 seat-v2 正确补登：单价 180 分，
	//    [2026-03-10, 2026-03-20) 恰好填满空档，Replaces 留空。
	//    两侧端点与既有版本相接：半开区间端点相接不算重叠，登记成功；
	//    失败的登记不占用版本标识，因此 seat-v2 仍可使用。
	if err := book.RegisterVersion(tariff.RegisterRequest{
		ItemID:    "seat",
		VersionID: "seat-v2",
		UnitPrice: 180,
		Start:     v2Start,
		End:       &v2RegisteredEnd,
	}); err != nil {
		panic(err)
	}
	fmt.Println("⑨ 补登 seat-v2：单价 180 分，[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)，Replaces 留空：err=<nil>，登记生效")

	// ⑩ 补登成功后的版本视图：按生效时间排列为 v1、v2、v3（登记顺序是 v3、v1、v2）；
	//    三版登记的起止时间与实际有效区间完全一致，替代与被替代关系全部为空；
	//    v1 的结束仍是 3 月 10 日、v3 的开始仍是 3 月 20 日，二者都没有被移动。
	printViews(book, "⑩ 补登成功后的版本视图（v1、v2、v3，替代关系全空）")

	// ⑪ 再次按时刻查询：原来的空档时刻现在返回 v2、单价 180 分；
	//    3 月 10 日交接点（开始时刻含）属于 v2，3 月 20 日交接点属于 v3，
	//    结束时刻不含、开始时刻包含；时间相接没有产生任何替代关系。
	fmt.Println("⑪ 补登后按时刻查询（开始时刻包含、结束时刻不包含）：")
	printAt(book, time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC))
	printAt(book, v2Start)
	printAt(book, gapAt)
	printAt(book, v3Start)
	printAt(book, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
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
	if errors.Is(err, tariff.ErrNoEffectiveVersion) {
		fmt.Printf("   查询 %s：无生效版本（%v），账本不拿邻近版本补位\n", at.Format(time.RFC3339), err)
		return
	}
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
① 登记 seat-v3：单价 200 分，2026-03-20T00:00:00Z 起持续有效，Replaces 留空，err=<nil>
② 登记 seat-v1：单价 150 分，[2026-03-01T00:00:00Z, 2026-03-10T00:00:00Z)，Replaces 留空，err=<nil>
③ 两版登记完成后的版本视图（按生效时间排列，而非登记顺序）：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-10T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代=""
   seat-v3：单价=200 开始=2026-03-20T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="" 被替代=""
④ 补登 v2 前按时刻查询：
   查询 2026-03-09T00:00:00Z：seat-v1（单价=150，实际有效区间 [2026-03-01T00:00:00Z, 2026-03-10T00:00:00Z)）
   查询 2026-03-15T00:00:00Z：无生效版本（tariff: no effective version for the rate item at the given time），账本不拿邻近版本补位
   查询 2026-03-20T00:00:00Z：seat-v3（单价=200，实际有效区间 [2026-03-20T00:00:00Z, 无（持续有效）)）
⑤ 登记 seat-v2（不填结束时间，Replaces 留空）：err=tariff: version interval overlaps an existing version of the item
   errors.Is(err, tariff.ErrOverlap) = true；登记失败是调用本身返回错误，整次登记未生效
⑥ 第一次失败后的版本视图（仍只有 v1、v3，边界与关系不变）：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-10T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代=""
   seat-v3：单价=200 开始=2026-03-20T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="" 被替代=""
   查询 2026-03-15T00:00:00Z：无生效版本（tariff: no effective version for the rate item at the given time），账本不拿邻近版本补位
⑦ 登记 seat-v2（结束=2026-03-20，但 Replaces 填 seat-v1）：err=tariff: new version start must be after the replaced version start and within its current effective interval
   errors.Is(err, tariff.ErrInvalidReplacement) = true；3 月 10 日不在 v1 的实际有效期 [03-01, 03-10) 内，“接在旧版后面”不是替代
⑧ 第二次失败后的版本视图（仍只有 v1、v3，边界与关系不变）：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-10T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代=""
   seat-v3：单价=200 开始=2026-03-20T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="" 被替代=""
⑨ 补登 seat-v2：单价 180 分，[2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)，Replaces 留空：err=<nil>，登记生效
⑩ 补登成功后的版本视图（v1、v2、v3，替代关系全空）：
   seat-v1：单价=150 开始=2026-03-01T00:00:00Z 登记结束=2026-03-10T00:00:00Z 实际有效结束=2026-03-10T00:00:00Z 替代="" 被替代=""
   seat-v2：单价=180 开始=2026-03-10T00:00:00Z 登记结束=2026-03-20T00:00:00Z 实际有效结束=2026-03-20T00:00:00Z 替代="" 被替代=""
   seat-v3：单价=200 开始=2026-03-20T00:00:00Z 登记结束=无（持续有效） 实际有效结束=无（持续有效） 替代="" 被替代=""
⑪ 补登后按时刻查询（开始时刻包含、结束时刻不包含）：
   查询 2026-03-09T00:00:00Z：seat-v1（单价=150，实际有效区间 [2026-03-01T00:00:00Z, 2026-03-10T00:00:00Z)）
   查询 2026-03-10T00:00:00Z：seat-v2（单价=180，实际有效区间 [2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)）
   查询 2026-03-15T00:00:00Z：seat-v2（单价=180，实际有效区间 [2026-03-10T00:00:00Z, 2026-03-20T00:00:00Z)）
   查询 2026-03-20T00:00:00Z：seat-v3（单价=200，实际有效区间 [2026-03-20T00:00:00Z, 无（持续有效）)）
   查询 2026-04-01T00:00:00Z：seat-v3（单价=200，实际有效区间 [2026-03-20T00:00:00Z, 无（持续有效）)）
```

对照输出即可选择正确的登记方式并核对结果：

- **③** 版本按**生效时间**列为 v1、v3，尽管登记顺序是先 v3 后 v1；两版的登记起止与实际有效区间一致，替代关系为空。
- **④** 补登前 3 月 15 日返回 `ErrNoEffectiveVersion`：空档里没有生效版本，账本不会拿邻近的 v1 或 v3 补位；3 月 9 日选 v1，3 月 20 日 00:00 因开始时刻包含已选 v3。
- **⑤⑥** v2 不填结束时间时区间是 `[03-10, ∞)`，与持续有效的 v3 重叠，返回 `ErrOverlap`——账本**不会**自动以 v3 的开始（3 月 20 日）作为 v2 的结束。失败整次回滚：账本仍只有 v1、v3，v1 的结束和 v3 的开始都保持原值，空档查询结论不变。
- **⑦⑧** 把 `seat-v1` 填进 `Replaces` 返回 `ErrInvalidReplacement`：v1 的实际有效区间是 `[03-01, 03-10)`，3 月 10 日是它的结束时刻（不含），v2 只是**接在** v1 后面，并没有替代它。这次失败同样回滚，账本状态与操作前一致。
- **⑨⑩** 正确方式是区间恰好填满空档、`Replaces` 留空：两端与邻版边界相接（半开区间相接不算重叠），登记成功。失败登记不占用标识，所以仍可用 `seat-v2`。补登后列表按生效时间为 v1、v2、v3；三版登记起止与实际有效区间一致，`Replaces` / `SupersededBy` 全空——v1 的结束仍是 3 月 10 日、v3 的开始仍是 3 月 20 日，都没有被移动。
- **⑪** 原空档时刻改返回 v2、单价 180 分；3 月 10 日交接点属于 v2、3 月 20 日交接点属于 v3：开始时刻包含、结束时刻不包含，两版与 v2 只是时间相接，没有发生替代。

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
