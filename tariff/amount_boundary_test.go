package tariff

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// 金额边界回归测试：费率仍使用整数分，单价和数量均为有符号 64 位整数。
//
// 三分单价下数量恰好不溢出的上限为 floor(MaxInt64/3) = 3074457345618258602，
// 3 乘该数量恰好等于 9223372036854775806（MaxInt64-1）；数量再加一，
// 乘积就超出有符号 64 位整数的表示范围。
const threeCentMaxQuantity = int64(3074457345618258602)

// alwaysValidStart 早于任何现实的测试运行时刻；版本登记为自该时刻起持续有效，
// 因此测试在不同真实运行时间下、在注入时钟的不同受理时刻下结论都相同，
// 不依赖等待版本生效。
var alwaysValidStart = time.Date(1, time.January, 2, 0, 0, 0, 0, time.UTC)

// TestQuoteAmountBoundaries 覆盖单价不等于一时数量恰好达到可计算上限与
// 刚刚超过上限的结果，并覆盖一分单价金额上限、合法零单价版本的边界。
func TestQuoteAmountBoundaries(t *testing.T) {
	// 每次运行使用独立账本；前几次注入确定时钟，最后一次直接使用真实时钟，
	// 验证结论不依赖运行所处的真实时间。
	type run struct {
		name    string
		real    bool
		clockAt time.Time
	}
	runs := []run{
		{name: "注入时刻一", clockAt: time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)},
		{name: "注入时刻二", clockAt: time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)},
		{name: "注入时刻三", clockAt: time.Date(2027, 6, 1, 12, 30, 45, 0, east8)},
		{name: "真实时钟", real: true},
	}

	cases := []struct {
		name      string
		itemID    string
		quantity  int64
		confirmed bool
		wantPrice int64
		wantTotal int64
		reason    RejectReason
	}{
		{
			name:      "三分单价数量恰达可计算上限",
			itemID:    "amount-p3",
			quantity:  threeCentMaxQuantity,
			confirmed: true,
			wantPrice: 3,
			wantTotal: math.MaxInt64 - 1, // 9223372036854775806
			reason:    ReasonNone,
		},
		{
			name:      "三分单价数量刚刚超过上限",
			itemID:    "amount-p3",
			quantity:  threeCentMaxQuantity + 1,
			confirmed: false,
			wantPrice: 0,
			wantTotal: 0,
			reason:    ReasonTotalOverflow,
		},
		{
			name:      "一分单价最大正数量即金额上限",
			itemID:    "amount-p1",
			quantity:  math.MaxInt64,
			confirmed: true,
			wantPrice: 1,
			wantTotal: math.MaxInt64,
			reason:    ReasonNone,
		},
		{
			name:      "零单价最大正数量总价为零",
			itemID:    "amount-p0",
			quantity:  math.MaxInt64,
			confirmed: true,
			wantPrice: 0,
			wantTotal: 0,
			reason:    ReasonNone,
		},
		{
			name:      "零单价不放宽数量要求数量为零",
			itemID:    "amount-p0",
			quantity:  0,
			confirmed: false,
			wantPrice: 0,
			wantTotal: 0,
			reason:    ReasonInvalidQuantity,
		},
		{
			name:      "零单价不放宽数量要求数量为负",
			itemID:    "amount-p0",
			quantity:  -1,
			confirmed: false,
			wantPrice: 0,
			wantTotal: 0,
			reason:    ReasonInvalidQuantity,
		},
		{
			name:      "零单价不放宽数量要求数量为最小负值",
			itemID:    "amount-p0",
			quantity:  math.MinInt64,
			confirmed: false,
			wantPrice: 0,
			wantTotal: 0,
			reason:    ReasonInvalidQuantity,
		},
	}

	for runIdx, r := range runs {
		t.Run(r.name, func(t *testing.T) {
			var b *Book
			var setNow func(time.Time)
			if r.real {
				b = NewBook() // 生产默认：受理时刻取自真实 time.Now
			} else {
				now, sn := fixedClock(r.clockAt)
				b, setNow = NewBook(WithClock(now)), sn
			}
			// 同一费率项内不允许登记重叠区间，三个单价分别登记在独立费率项上；
			// 登记与时间交接行为不在本测试的保护范围内，这里只登记持续有效的版本。
			prices := map[string]int64{"amount-p3": 3, "amount-p1": 1, "amount-p0": 0}
			for itemID, price := range prices {
				if err := b.RegisterVersion(RegisterRequest{
					ItemID: itemID, VersionID: "v1", UnitPrice: price, Start: alwaysValidStart,
				}); err != nil {
					t.Fatalf("register %s: %v", itemID, err)
				}
			}

			reqs := make([]QuoteRequest, len(cases))
			first := make([]Outcome, len(cases))
			for i, c := range cases {
				req := QuoteRequest{
					RequestID: fmt.Sprintf("amt-%d-%d", runIdx, i),
					ItemID:    c.itemID,
					VersionID: "v1",
					Quantity:  c.quantity,
				}
				reqs[i] = req
				out, err := b.Quote(req)
				// 溢出与非法数量都属于报价被受理之后的拒绝：调用本身的 error
				// 必须为空，不能把这一结果当成调用失败而丢掉请求来源。
				if err != nil {
					t.Fatalf("%s: 受理后的拒绝不应返回 error: %v", c.name, err)
				}
				if out.Confirmed != c.confirmed {
					t.Fatalf("%s: Confirmed=%v want %v (%+v)", c.name, out.Confirmed, c.confirmed, out)
				}
				if out.UnitPrice != c.wantPrice || out.Total != c.wantTotal {
					t.Fatalf("%s: 单价/总价=%d/%d want %d/%d",
						c.name, out.UnitPrice, out.Total, c.wantPrice, c.wantTotal)
				}
				if out.Reason != c.reason {
					t.Fatalf("%s: 拒绝原因=%q want %q", c.name, out.Reason, c.reason)
				}
				// 报价继续引用请求指定的费率项和版本，数量原样保留。
				if out.Request != req {
					t.Fatalf("%s: 请求来源未保留: %+v vs %+v", c.name, out.Request, req)
				}
				// 受理时刻必须取自这次请求首次被接收的时刻（版本在该时刻有效）。
				if out.AcceptedAt.IsZero() || out.AcceptedAt.Before(alwaysValidStart) {
					t.Fatalf("%s: 受理时刻异常: %v", c.name, out.AcceptedAt)
				}
				if !r.real && !out.AcceptedAt.Equal(r.clockAt) {
					t.Fatalf("%s: 受理时刻=%v want %v", c.name, out.AcceptedAt, r.clockAt)
				}
				first[i] = out
			}

			// 时钟推进到另一个时刻（真实时钟运行则小睡片刻，仅用于让“之后”成立；
			// 不影响确定性结论——取回的永远是保存的首次受理时刻）。
			if r.real {
				time.Sleep(2 * time.Millisecond)
			} else {
				setNow(r.clockAt.Add(48 * time.Hour))
			}
			for i, c := range cases {
				// 通过现有查询功能取回首次结果，保留费率项、版本、数量、金额、
				// 拒绝原因和首次受理时刻。
				got, err := b.Lookup(reqs[i].RequestID)
				if err != nil {
					t.Fatalf("%s: 查询首次结果: %v", c.name, err)
				}
				if got != first[i] {
					t.Fatalf("%s: 保存的首次结果与返回不一致: %+v vs %+v", c.name, first[i], got)
				}
				// 不能出现首次报价已拒绝而查询却显示确认的情况。
				if !c.confirmed && got.Confirmed {
					t.Fatalf("%s: 首次结果为拒绝(%q)，查询却显示确认", c.name, c.reason)
				}
				if !got.AcceptedAt.Equal(first[i].AcceptedAt) {
					t.Fatalf("%s: 查询受理时刻=%v，应保留首次接收时刻 %v",
						c.name, got.AcceptedAt, first[i].AcceptedAt)
				}
				// 原样重试同样返回首次结果。
				replay, err := b.Quote(reqs[i])
				if err != nil {
					t.Fatalf("%s: 原样重试: %v", c.name, err)
				}
				if replay != first[i] {
					t.Fatalf("%s: 重试未原样返回首次结果: %+v vs %+v", c.name, first[i], replay)
				}
			}

			// 对溢出场景再单独点明：账本中保存的是 total_overflow 拒绝而非确认，
			// 拒绝结果中的单价及总价仍为零。
			var overflowIdx = -1
			for i, c := range cases {
				if c.reason == ReasonTotalOverflow {
					overflowIdx = i
				}
			}
			if overflowIdx < 0 {
				t.Fatal("测试表缺少 total_overflow 场景")
			}
			got, err := b.Lookup(reqs[overflowIdx].RequestID)
			if err != nil {
				t.Fatalf("查询溢出拒绝: %v", err)
			}
			if got.Confirmed || got.Reason != ReasonTotalOverflow {
				t.Fatalf("溢出请求不能留下已确认记录: %+v", got)
			}
			if got.UnitPrice != 0 || got.Total != 0 {
				t.Fatalf("溢出拒绝的单价/总价应为零: %d/%d", got.UnitPrice, got.Total)
			}
		})
	}
}
