package tariff

import (
	"testing"
	"time"
)

func TestRegisterVersion_Basic(t *testing.T) {
	b, _ := newTestBook(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)

	registerVersion(t, b, RegisterVersionInput{
		Item: "power", ID: "v1", UnitPrice: 100, Start: start, End: &end,
	})

	view, err := b.GetItem("power")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if len(view.Versions) != 1 {
		t.Fatalf("versions = %d, want 1", len(view.Versions))
	}
	v := view.Versions[0]
	if v.ID != "v1" || v.UnitPrice != 100 || v.Replaces != "" {
		t.Fatalf("unexpected version: %+v", v)
	}
	if !v.RegisteredStart.Equal(start) || v.RegisteredEnd == nil || !v.RegisteredEnd.Equal(end) {
		t.Fatalf("registered interval = [%v, %v), want [%v, %v)", v.RegisteredStart, v.RegisteredEnd, start, end)
	}
	if !v.EffectiveStart.Equal(start) || v.EffectiveEnd == nil || !v.EffectiveEnd.Equal(end) {
		t.Fatalf("effective interval = [%v, %v), want [%v, %v)", v.EffectiveStart, v.EffectiveEnd, start, end)
	}
}

func TestRegisterVersion_Validation(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := now.Add(-time.Hour)
	tests := []struct {
		name string
		in   RegisterVersionInput
		want Reason
	}{
		{"empty item", RegisterVersionInput{ID: "v", Start: now}, ReasonInvalidItem},
		{"empty id", RegisterVersionInput{Item: "i", Start: now}, ReasonInvalidVersion},
		{"negative price", RegisterVersionInput{Item: "i", ID: "v", UnitPrice: -1, Start: now}, ReasonInvalidPrice},
		{"end before start", RegisterVersionInput{Item: "i", ID: "v", Start: now, End: &end}, ReasonInvalidInterval},
		{"end equal start", RegisterVersionInput{Item: "i", ID: "v", Start: now, End: ptrTime(now)}, ReasonInvalidInterval},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newTestBook(t, now)
			err := b.RegisterVersion(tc.in)
			if !IsReason(err, tc.want) {
				t.Fatalf("err = %v, want reason %s", err, tc.want)
			}
		})
	}
}

func TestRegisterVersion_DuplicateID(t *testing.T) {
	b, _ := newTestBook(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	registerVersion(t, b, RegisterVersionInput{Item: "i", ID: "v1", Start: start})

	err := b.RegisterVersion(RegisterVersionInput{Item: "i", ID: "v1", Start: start.Add(time.Hour)})
	if !IsReason(err, ReasonVersionExists) {
		t.Fatalf("err = %v, want %s", err, ReasonVersionExists)
	}
}

func TestRegisterVersion_OverlapAndAdjacent(t *testing.T) {
	b, _ := newTestBook(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)

	// v1: [start, end)
	registerVersion(t, b, RegisterVersionInput{Item: "i", ID: "v1", Start: start, End: &end})

	// 与 v1 重叠（即使开始时刻相同）→ 拒绝
	err := b.RegisterVersion(RegisterVersionInput{Item: "i", ID: "v2", Start: start})
	if !IsReason(err, ReasonOverlap) {
		t.Fatalf("overlap err = %v, want %s", err, ReasonOverlap)
	}

	// 相邻交接：v2: [end, ...) → 允许
	registerVersion(t, b, RegisterVersionInput{Item: "i", ID: "v2", Start: end})

	// v3: [mid, ...) 与 v1、v2 都重叠 → 拒绝
	err = b.RegisterVersion(RegisterVersionInput{Item: "i", ID: "v3", Start: mid})
	if !IsReason(err, ReasonOverlap) {
		t.Fatalf("overlap err = %v, want %s", err, ReasonOverlap)
	}

	view, _ := b.GetItem("i")
	if len(view.Versions) != 2 {
		t.Fatalf("versions = %d, want 2 (failed registrations must not leave state)", len(view.Versions))
	}
}

func TestRegisterVersion_TimezoneComparedByInstant(t *testing.T) {
	b, _ := newTestBook(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	plus8 := fixedZone(8)

	// v1: [2024-01-01 00:00 +08:00, 2024-01-01 02:00 +08:00)
	v1Start := time.Date(2024, 1, 1, 0, 0, 0, 0, plus8)
	v1End := time.Date(2024, 1, 1, 2, 0, 0, 0, plus8) // 2023-12-31 18:00 UTC
	registerVersion(t, b, RegisterVersionInput{Item: "i", ID: "v1", Start: v1Start, End: &v1End})

	// v2 与 v1 同一实际时刻（UTC 表示），即使字面时间不同也算重叠
	v2Start := time.Date(2023, 12, 31, 16, 0, 0, 0, time.UTC)
	err := b.RegisterVersion(RegisterVersionInput{Item: "i", ID: "v2", Start: v2Start})
	if !IsReason(err, ReasonOverlap) {
		t.Fatalf("same-instant err = %v, want %s", err, ReasonOverlap)
	}

	// v4 起点与 v1 终点是同一实际时刻（不同时区字面）→ 相邻交接，允许
	v4Start := time.Date(2023, 12, 31, 18, 0, 0, 0, time.UTC)
	registerVersion(t, b, RegisterVersionInput{Item: "i", ID: "v4", Start: v4Start})

	view, _ := b.GetItem("i")
	if len(view.Versions) != 2 {
		t.Fatalf("versions = %d, want 2 (failed registration must not leave state)", len(view.Versions))
	}
}

func TestRegisterVersion_Replacement(t *testing.T) {
	b, clk := newTestBook(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	v2Start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	v3Start := time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC)
	v3End := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)

	registerVersion(t, b, RegisterVersionInput{Item: "power", ID: "v1", UnitPrice: 100, Start: start})
	registerVersion(t, b, RegisterVersionInput{
		Item: "power", ID: "v2", UnitPrice: 120, Start: v2Start, Replaces: "v1",
	})
	registerVersion(t, b, RegisterVersionInput{
		Item: "power", ID: "v3", UnitPrice: 130, Start: v3Start, End: &v3End, Replaces: "v2",
	})

	view, err := b.GetItem("power")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	byID := map[string]VersionView{}
	for _, v := range view.Versions {
		byID[v.ID] = v
	}

	// v1 被 v2 替代：实际有效区间 [start, v2Start)，登记区间不变
	v1 := byID["v1"]
	if v1.Replaces != "" {
		t.Fatalf("v1 Replaces = %q, want empty", v1.Replaces)
	}
	if !v1.EffectiveStart.Equal(start) || v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(v2Start) {
		t.Fatalf("v1 effective = [%v, %v), want [%v, %v)", v1.EffectiveStart, v1.EffectiveEnd, start, v2Start)
	}
	if v1.RegisteredEnd != nil {
		t.Fatalf("v1 RegisteredEnd = %v, want nil (registration record must not change)", *v1.RegisteredEnd)
	}

	// v2 被 v3 替代：实际有效区间 [v2Start, v3Start)
	v2 := byID["v2"]
	if v2.Replaces != "v1" {
		t.Fatalf("v2 Replaces = %q, want v1", v2.Replaces)
	}
	if !v2.EffectiveStart.Equal(v2Start) || v2.EffectiveEnd == nil || !v2.EffectiveEnd.Equal(v3Start) {
		t.Fatalf("v2 effective = [%v, %v), want [%v, %v)", v2.EffectiveStart, v2.EffectiveEnd, v2Start, v3Start)
	}

	// v3 实际有效区间 [v3Start, v3End)
	v3 := byID["v3"]
	if v3.Replaces != "v2" {
		t.Fatalf("v3 Replaces = %q, want v2", v3.Replaces)
	}
	if !v3.EffectiveStart.Equal(v3Start) || v3.EffectiveEnd == nil || !v3.EffectiveEnd.Equal(v3End) {
		t.Fatalf("v3 effective = [%v, %v), want [%v, %v)", v3.EffectiveStart, v3.EffectiveEnd, v3Start, v3End)
	}

	// 报价：v1 在 v2 开始后失效；v2 在 v3 开始后失效；v3 到期后失效
	clk.set(v2Start.Add(time.Hour))
	if res := quote(t, b, QuoteRequest{RequestID: "q1", Item: "power", Version: "v1", Quantity: 1}); res.Rejected == nil || res.Rejected.Reason != ReasonExpired {
		t.Fatalf("v1 after replacement: rejected=%v, want %s", res.Rejected, ReasonExpired)
	}
	if res := quote(t, b, QuoteRequest{RequestID: "q2", Item: "power", Version: "v2", Quantity: 1}); res.Confirmed == nil {
		t.Fatalf("v2 at v2Start+1h: rejected=%v, want confirmed", res.Rejected)
	}

	// v3 到期后，已被替代的 v1、v2 不会重新生效
	clk.set(v3End.Add(time.Hour))
	for _, id := range []string{"v1", "v2", "v3"} {
		res := quote(t, b, QuoteRequest{RequestID: "q-" + id, Item: "power", Version: id, Quantity: 1})
		if res.Confirmed != nil {
			t.Fatalf("%s confirmed after all expired, want rejected", id)
		}
	}
}

func TestRegisterVersion_ReplacementValidation(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)

	setup := func(t *testing.T) *Book {
		t.Helper()
		b, _ := newTestBook(t, start)
		registerVersion(t, b, RegisterVersionInput{Item: "i", ID: "v1", Start: start, End: &end})
		return b
	}

	tests := []struct {
		name string
		in   RegisterVersionInput
		want Reason
	}{
		{
			"replaced version missing",
			RegisterVersionInput{Item: "i", ID: "v2", Start: mid, Replaces: "nope"},
			ReasonReplacesNotFound,
		},
		{
			"replaced version belongs to another item",
			RegisterVersionInput{Item: "other", ID: "v9", Start: mid, Replaces: "v1"},
			ReasonReplacesNotFound,
		},
		{
			"new start not later than old start",
			RegisterVersionInput{Item: "i", ID: "v2", Start: start, Replaces: "v1"},
			ReasonReplacesOutside,
		},
		{
			"new start outside old current interval",
			RegisterVersionInput{Item: "i", ID: "v2", Start: end, Replaces: "v1"},
			ReasonReplacesOutside,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := setup(t)
			err := b.RegisterVersion(tc.in)
			if !IsReason(err, tc.want) {
				t.Fatalf("err = %v, want reason %s", err, tc.want)
			}
			// 失败不能留下状态：v1 的区间与替代关系不变
			view, _ := b.GetItem("i")
			if len(view.Versions) != 1 {
				t.Fatalf("versions = %d, want 1", len(view.Versions))
			}
			v1 := view.Versions[0]
			if v1.Replaces != "" || v1.EffectiveEnd == nil || !v1.EffectiveEnd.Equal(end) {
				t.Fatalf("v1 changed after failed registration: %+v", v1)
			}
		})
	}
}

func TestRegisterVersion_ReplacementConflictAtomicity(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	v2Start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	v3Start := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)

	b, _ := newTestBook(t, start)
	registerVersion(t, b, RegisterVersionInput{Item: "i", ID: "v1", Start: start})
	registerVersion(t, b, RegisterVersionInput{
		Item: "i", ID: "v2", Start: v2Start, End: &end, Replaces: "v1",
	})

	// v3 也要替代 v1，但开始时刻晚于 v2 开始时刻：
	// v1 当前有效区间已被 v2 截断为 [start, v2Start)，v3 起点落在其外；
	// 且 v3 区间与 v2 重叠。整次登记必须被拒绝，v1/v2 不变。
	err := b.RegisterVersion(RegisterVersionInput{
		Item: "i", ID: "v3", Start: v3Start, Replaces: "v1",
	})
	if !IsReason(err, ReasonReplacesOutside) && !IsReason(err, ReasonOverlap) {
		t.Fatalf("err = %v, want %s or %s", err, ReasonReplacesOutside, ReasonOverlap)
	}

	view, _ := b.GetItem("i")
	if len(view.Versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(view.Versions))
	}
	byID := map[string]VersionView{}
	for _, v := range view.Versions {
		byID[v.ID] = v
	}
	if byID["v1"].EffectiveEnd == nil || !byID["v1"].EffectiveEnd.Equal(v2Start) {
		t.Fatalf("v1 effective end changed: %+v", byID["v1"])
	}
	if byID["v2"].Replaces != "v1" {
		t.Fatalf("v2 replacement changed: %+v", byID["v2"])
	}
}

func TestGetItem_NotFound(t *testing.T) {
	b, _ := newTestBook(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	_, err := b.GetItem("missing")
	if !IsReason(err, ReasonItemNotFound) {
		t.Fatalf("err = %v, want %s", err, ReasonItemNotFound)
	}
}
