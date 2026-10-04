package tariff

import (
	"testing"
	"time"
)

// “数量不合法时先说明数量问题”的回归保障。
//
// 固定时间线（全部为 UTC 零点，结束时刻不含，经注入时钟确定，不依赖运行当天
// 或本机时区）：
//
//	旧版本 seat/seat-v1：单价 150 分，2026-03-01 00:00 起生效
//	新版本 seat/seat-v2：单价 180 分，2026-03-10 00:00 起替代 seat-v1
//
// 因此交接前（如 3 月 5 日）引用 seat-v2 属于版本尚未生效，
// 交接后（如 3 月 12 日）引用 seat-v1 属于版本已经失效；
// 引用未登记版本、或费率项根本不存在则属于版本未找到。
//
// 约定：数量必须为正整数。零或负数量与上述任一版本问题同时出现时，
// 拒绝原因必须是 invalid_quantity，不能报成版本类原因；
// 拒绝属于正常受理（err 为 nil），金额为零，并完整保留原请求内容。
var (
	iqOldStart      = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	iqHandoff       = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	iqBeforeHandoff = time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	iqAfterHandoff  = time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	// 查询与重试时把时钟拨到另一个时刻，证明保存的受理时刻不会被重算。
	iqLater = time.Date(2026, 4, 2, 8, 30, 0, 0, time.UTC)
)

// newQuantityPrecedenceBook 返回登记好 seat-v1/seat-v2 交接关系的账本。
func newQuantityPrecedenceBook(t *testing.T, start time.Time) (*Book, func(time.Time)) {
	t.Helper()
	now, setNow := fixedClock(start)
	b := NewBook(WithClock(now))
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: iqOldStart,
	}); err != nil {
		t.Fatalf("register seat-v1: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start: iqHandoff, Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register seat-v2: %v", err)
	}
	return b, setNow
}

// TestInvalidQuantityTakesPrecedenceOverVersionAvailability 覆盖数量问题与
// 版本问题同时出现的四种版本状态：尚未生效、已经失效、版本未登记、费率项不存在。
// 零数量和负数量分别用各自从未使用过的标识首次提交，结果都必须是 invalid_quantity。
func TestInvalidQuantityTakesPrecedenceOverVersionAvailability(t *testing.T) {
	cases := []struct {
		name          string
		instant       time.Time
		itemID        string
		versionID     string
		versionReason RejectReason // 同一版本状态、合法数量时本应得到的版本类原因
	}{
		{"交接前引用新版-尚未生效", iqBeforeHandoff, "seat", "seat-v2", ReasonVersionNotYetEffective},
		{"交接后引用旧版-已经失效", iqAfterHandoff, "seat", "seat-v1", ReasonVersionExpired},
		{"引用未登记版本", iqAfterHandoff, "seat", "seat-ghost", ReasonVersionNotFound},
		{"费率项根本不存在", iqAfterHandoff, "no-such-item", "seat-v1", ReasonVersionNotFound},
	}
	quantities := []struct {
		name string
		qty  int64
	}{
		{"零数量", 0},
		{"负数量", -3},
	}

	const rounds = 3
	for round := 0; round < rounds; round++ {
		for _, c := range cases {
			for _, q := range quantities {
				subName := c.name + "/" + q.name
				t.Run(subName, func(t *testing.T) {
					b, setNow := newQuantityPrecedenceBook(t, c.instant)

					req := QuoteRequest{
						RequestID: "iq-" + c.versionID + "-" + q.name,
						ItemID:    c.itemID,
						VersionID: c.versionID,
						Quantity:  q.qty,
					}
					out, err := b.Quote(req)
					if err != nil {
						t.Fatalf("拒绝属于正常受理，err 应为空: %v", err)
					}
					if out.Confirmed {
						t.Fatalf("数量非法不能确认: %+v", out)
					}
					if out.Reason != ReasonInvalidQuantity {
						t.Fatalf("两种问题同时出现时拒绝原因应为 %q，got %q（不能报成 %q）",
							ReasonInvalidQuantity, out.Reason, c.versionReason)
					}
					if out.UnitPrice != 0 || out.Total != 0 {
						t.Fatalf("拒绝结果单价和总价均应为零: 单价=%d 总价=%d",
							out.UnitPrice, out.Total)
					}
					// 完整保留提交的费率项、版本和原始数量：
					// 负数不能被改成零，也不能改用当时有效的另一版。
					if out.Request != req {
						t.Fatalf("拒绝结果未原样保留请求: %+v vs %+v", out.Request, req)
					}
					if !out.AcceptedAt.Equal(c.instant) {
						t.Fatalf("受理时刻应为本次首次报价时刻 %v，got %v",
							c.instant, out.AcceptedAt)
					}

					// 把时钟拨到另一个时刻：按标识查询必须取回同一份拒绝，
					// 不能查不到，也不能出现确认金额或被改写的受理时刻。
					setNow(iqLater)
					got, err := b.Lookup(req.RequestID)
					if err != nil {
						t.Fatalf("首次拒绝应可按标识查询: %v", err)
					}
					if got != out {
						t.Fatalf("查询结果与首次拒绝不一致: %+v vs %+v", got, out)
					}
					if got.Confirmed || got.UnitPrice != 0 || got.Total != 0 {
						t.Fatalf("保存的拒绝不能出现确认金额: %+v", got)
					}
					// 原样重试同样返回首次拒绝，而不是按另一时刻重新判断版本状态。
					replay, err := b.Quote(req)
					if err != nil {
						t.Fatalf("原样重试不应返回错误: %v", err)
					}
					if replay != out {
						t.Fatalf("重试结果与首次拒绝不一致: %+v vs %+v", replay, out)
					}
				})
			}
		}
	}
}

// TestValidQuantityControlsVersionReasons 用合法正数量 4 在相同版本状态下形成对照：
// 数量没问题时，版本类原因必须照常返回，版本有效时则按所引用版本的单价确认。
// 这样才能区分数量输入错误与版本无法使用，而不是所有请求都固定返回数量错误。
func TestValidQuantityControlsVersionReasons(t *testing.T) {
	cases := []struct {
		name      string
		instant   time.Time
		itemID    string
		versionID string
		confirmed bool
		reason    RejectReason
		unitPrice int64
		total     int64
	}{
		{"交接前引用新版-尚未生效", iqBeforeHandoff, "seat", "seat-v2", false, ReasonVersionNotYetEffective, 0, 0},
		{"交接后引用旧版-已经失效", iqAfterHandoff, "seat", "seat-v1", false, ReasonVersionExpired, 0, 0},
		{"引用未登记版本", iqAfterHandoff, "seat", "seat-ghost", false, ReasonVersionNotFound, 0, 0},
		{"费率项根本不存在", iqAfterHandoff, "no-such-item", "seat-v1", false, ReasonVersionNotFound, 0, 0},
		{"交接前引用旧版-按150确认600", iqBeforeHandoff, "seat", "seat-v1", true, ReasonNone, 150, 600},
		{"交接后引用新版-按180确认720", iqAfterHandoff, "seat", "seat-v2", true, ReasonNone, 180, 720},
	}

	const rounds = 3
	for round := 0; round < rounds; round++ {
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				b, _ := newQuantityPrecedenceBook(t, c.instant)

				req := QuoteRequest{
					RequestID: "ctrl-" + c.versionID,
					ItemID:    c.itemID,
					VersionID: c.versionID,
					Quantity:  4,
				}
				out, err := b.Quote(req)
				if err != nil {
					t.Fatalf("正常受理不应返回错误: %v", err)
				}
				if out.Confirmed != c.confirmed || out.Reason != c.reason {
					t.Fatalf("数量合法时应区分版本状态: got confirmed=%v reason=%q, want confirmed=%v reason=%q (%+v)",
						out.Confirmed, out.Reason, c.confirmed, c.reason, out)
				}
				if out.UnitPrice != c.unitPrice || out.Total != c.total {
					t.Fatalf("金额: 单价=%d 总价=%d, want %d/%d",
						out.UnitPrice, out.Total, c.unitPrice, c.total)
				}
				if out.Request != req {
					t.Fatalf("结果未原样保留请求: %+v vs %+v", out.Request, req)
				}
				if !out.AcceptedAt.Equal(c.instant) {
					t.Fatalf("受理时刻=%v, want %v", out.AcceptedAt, c.instant)
				}

				// 对照结果同样可按标识取回同一份首次结果。
				got, err := b.Lookup(req.RequestID)
				if err != nil {
					t.Fatalf("首次结果应可按标识查询: %v", err)
				}
				if got != out {
					t.Fatalf("查询结果与首次结果不一致: %+v vs %+v", got, out)
				}
			})
		}
	}
}
