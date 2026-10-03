package tariff

import (
	"fmt"
	"testing"
	"time"
)

// “版本尚未生效”的首次拒绝也会被保存的回归测试。
//
// 时间线（注入时钟，不依赖真实时间跨过生效边界）：
//
//	版本 seat/v-next：单价 250 分，at(100) 起生效（含），at(200) 结束（不含）
//	首次报价：at(50)，生效前 → 受理但拒绝，原因 version_not_yet_effective
//	重试点：at(100) 生效时刻本身、at(150) 生效后仍在结束时间之前
//
// 约定：非空请求标识的首次结果（无论确认还是拒绝）都会被保存；
// 版本生效后用原标识原样重试仍返回那次首次拒绝，只有换用从未使用过的
// 新标识才能按已生效的版本确认报价。
func TestNotYetEffectiveRejectionSurvivesEffectiveInstant(t *testing.T) {
	now, setNow := fixedClock(at(50))
	b := NewBook(WithClock(now))

	// 单价为整数分、数量为合法正整数、结束时间晚于开始时间：
	// 场景中不混入数量错误或金额溢出。
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "v-next", UnitPrice: 250,
		Start: at(100), End: atPtr(200),
	}); err != nil {
		t.Fatalf("register v-next: %v", err)
	}

	req := QuoteRequest{RequestID: "early", ItemID: "seat", VersionID: "v-next", Quantity: 3}

	// 生效前首次提交：调用本身不返回错误，结果是未确认的拒绝。
	first, err := b.Quote(req)
	if err != nil {
		t.Fatalf("拒绝是正常受理，err 应为空: %v", err)
	}
	if first.Confirmed {
		t.Fatalf("生效前报价不能确认: %+v", first)
	}
	if first.Reason != ReasonVersionNotYetEffective {
		t.Fatalf("拒绝原因=%q, want %q", first.Reason, ReasonVersionNotYetEffective)
	}
	if first.UnitPrice != 0 || first.Total != 0 {
		t.Fatalf("拒绝结果单价和总价都应为零: 单价=%d 总价=%d", first.UnitPrice, first.Total)
	}
	// 完整保留请求标识、费率项、版本、数量和首次受理时刻。
	if first.Request != req {
		t.Fatalf("首次结果未保留原请求内容: %+v vs %+v", first.Request, req)
	}
	if !first.AcceptedAt.Equal(at(50)) {
		t.Fatalf("首次受理时刻=%v, want %v", first.AcceptedAt, at(50))
	}

	// 首次拒绝也会保存：按标识查询能找到这份拒绝，而不是当作未受理过。
	got, err := b.Lookup("early")
	if err != nil {
		t.Fatalf("首次拒绝应可按标识查询: %v", err)
	}
	if got != first {
		t.Fatalf("查询结果与首次拒绝不一致: %+v vs %+v", got, first)
	}

	// 进入有效期的两个代表时刻：生效起点本身（含在有效区间内）、
	// 以及生效后仍在结束时间之前。
	for _, instant := range []time.Time{at(100), at(150)} {
		setNow(instant)

		// 原标识原内容重试：仍是先前的拒绝，不按当前费率重新计算，
		// 受理时刻仍是首次受理的 at(50)。生效起点上的拒绝来自幂等保留，
		// 不能误当作版本依旧不可用——下面用新标识确认这一点。
		replay, err := b.Quote(req)
		if err != nil {
			t.Fatalf("%v 重试: %v", instant, err)
		}
		if replay != first {
			t.Fatalf("%v 重试被重算: %+v -> %+v", instant, first, replay)
		}
		if replay.Confirmed || replay.Reason != ReasonVersionNotYetEffective ||
			replay.UnitPrice != 0 || replay.Total != 0 {
			t.Fatalf("%v 重试必须仍是首次拒绝: %+v", instant, replay)
		}
		if !replay.AcceptedAt.Equal(at(50)) {
			t.Fatalf("%v 重试的受理时刻被改写: %v", instant, replay.AcceptedAt)
		}

		// 查询也继续指向第一次受理的请求与时间。
		got, err := b.Lookup("early")
		if err != nil {
			t.Fatalf("%v 查询原标识: %v", instant, err)
		}
		if got != first {
			t.Fatalf("%v 查询结果被改写: %+v -> %+v", instant, first, got)
		}

		// 换用从未使用过的标识，费率项、版本、数量一致：
		// 版本已生效，新报价正常确认，单价来自所引用版本，总价为单价乘数量。
		freshID := fmt.Sprintf("fresh-%d", instant.Unix())
		fresh, err := b.Quote(QuoteRequest{
			RequestID: freshID, ItemID: "seat", VersionID: "v-next", Quantity: 3,
		})
		if err != nil {
			t.Fatalf("%v 新标识报价: %v", instant, err)
		}
		if !fresh.Confirmed || fresh.Reason != ReasonNone {
			t.Fatalf("%v 版本已生效，新标识报价应确认: %+v", instant, fresh)
		}
		if fresh.UnitPrice != 250 || fresh.Total != 750 {
			t.Fatalf("%v 新报价金额: 单价=%d 总价=%d, want 250/750", instant, fresh.UnitPrice, fresh.Total)
		}
		if !fresh.AcceptedAt.Equal(instant) {
			t.Fatalf("%v 新报价受理时刻=%v", instant, fresh.AcceptedAt)
		}

		// 新报价确认后，原请求的确认状态、拒绝原因、金额与首次受理时刻不变；
		// 双方各自按标识查询，互不覆盖。
		gotOld, err := b.Lookup("early")
		if err != nil {
			t.Fatalf("%v 新报价确认后查询原标识: %v", instant, err)
		}
		if gotOld != first {
			t.Fatalf("%v 新报价确认改写了原请求的结果: %+v -> %+v", instant, first, gotOld)
		}
		gotFresh, err := b.Lookup(freshID)
		if err != nil {
			t.Fatalf("%v 查询新标识: %v", instant, err)
		}
		if gotFresh != fresh {
			t.Fatalf("%v 新标识查询结果不一致: %+v vs %+v", instant, fresh, gotFresh)
		}
		if gotOld == gotFresh {
			t.Fatalf("%v 两份结果应可稳定区分: %+v", instant, gotOld)
		}
	}

	// 两个时刻都走过后，原标识的结果依旧是 at(50) 的首次拒绝。
	final, err := b.Lookup("early")
	if err != nil {
		t.Fatal(err)
	}
	if final != first {
		t.Fatalf("原标识的最终结果被改写: %+v -> %+v", first, final)
	}
}
