package tariff

import (
	"fmt"
	"testing"
	"time"
)

// 交接场景的固定时刻：费率时间按东八区登记，受理时刻按 UTC 推进。
// 东八区 2026-03-10 00:00 与 UTC 2026-03-09 16:00 是同一时刻（交接点）。
var (
	east8       = time.FixedZone("UTC+8", 8*3600)
	handoffEast = time.Date(2026, 3, 10, 0, 0, 0, 0, east8)
	handoffUTC  = time.Date(2026, 3, 9, 16, 0, 0, 0, time.UTC)
)

// registerHandoffVersions 登记同一费率项的旧版与替代新版：
// 旧版 150 分，东八区 3 月 1 日起生效，登记结束为 3 月 31 日零点；
// 新版 180 分，东八区 3 月 10 日零点替代旧版，不登记结束时间。
func registerHandoffVersions(t *testing.T, b *Book) {
	t.Helper()
	oldEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, east8)
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v1", UnitPrice: 150,
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, east8),
		End:   &oldEnd,
	}); err != nil {
		t.Fatalf("register old: %v", err)
	}
	if err := b.RegisterVersion(RegisterRequest{
		ItemID: "seat", VersionID: "seat-v2", UnitPrice: 180,
		Start:    handoffEast,
		Replaces: "seat-v1",
	}); err != nil {
		t.Fatalf("register new: %v", err)
	}
	if !handoffEast.Equal(handoffUTC) {
		t.Fatal("test premise broken: handoff instants differ")
	}
}

// 登记结束时间仍显示月底，实际有效结束时间显示交接点；新版保留替代来源。
func TestHandoffVersionViews(t *testing.T) {
	b := NewBook()
	registerHandoffVersions(t, b)

	views, err := b.ItemVersions("seat")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("want 2 versions, got %d", len(views))
	}
	old, nv := views[0], views[1]
	if old.VersionID != "seat-v1" || nv.VersionID != "seat-v2" {
		t.Fatalf("unexpected order: %v, %v", old.VersionID, nv.VersionID)
	}

	wantRegisteredEnd := time.Date(2026, 3, 31, 0, 0, 0, 0, east8)
	if old.End == nil || !old.End.Equal(wantRegisteredEnd) {
		t.Fatalf("old registered end must stay at month end: %v", old.End)
	}
	if old.EffectiveEnd == nil || !old.EffectiveEnd.Equal(handoffEast) {
		t.Fatalf("old effective end must be the handoff point: %v", old.EffectiveEnd)
	}
	if old.SupersededBy != "seat-v2" {
		t.Fatalf("old superseded-by: %q", old.SupersededBy)
	}
	if nv.Replaces != "seat-v1" {
		t.Fatalf("new must keep replacement source: %q", nv.Replaces)
	}
	if nv.End != nil || nv.EffectiveEnd != nil {
		t.Fatalf("new should be open-ended: %+v", nv)
	}
}

// 交接点前后一纳秒与交接点本身，新旧版本的受理结果严格按实际时刻区分；
// 同一时刻换一种时区表示，确认状态、金额与拒绝原因完全一致。
func TestHandoffBoundaryQuotes(t *testing.T) {
	now, setNow := fixedClock(handoffUTC)
	b := NewBook(WithClock(now))
	registerHandoffVersions(t, b)

	// 同一实际时刻的多种时区表示。
	instants := []struct {
		name string
		at   time.Time
	}{
		{"handoff-1ns UTC", handoffUTC.Add(-time.Nanosecond)},
		{"handoff-1ns UTC+8", handoffEast.Add(-time.Nanosecond)},
		{"handoff UTC", handoffUTC},
		{"handoff UTC+8", handoffEast},
		{"handoff+1ns UTC", handoffUTC.Add(time.Nanosecond)},
		{"handoff+1ns UTC+8", handoffEast.Add(time.Nanosecond)},
	}

	seq := 0
	quote := func(versionID string) Outcome {
		t.Helper()
		seq++
		out, err := b.Quote(QuoteRequest{
			RequestID: fmt.Sprintf("handoff-%d", seq),
			ItemID:    "seat",
			VersionID: versionID,
			Quantity:  4,
		})
		if err != nil {
			t.Fatalf("quote must be accepted normally, got err: %v", err)
		}
		return out
	}

	for _, inst := range instants {
		setNow(inst.at)
		before := inst.at.Before(handoffUTC)

		oldOut := quote("seat-v1")
		newOut := quote("seat-v2")

		if before {
			// 交接点前：旧版确认 150*4=600，新版尚未生效。
			if !oldOut.Confirmed || oldOut.UnitPrice != 150 || oldOut.Total != 600 {
				t.Fatalf("%s: old version should confirm 150/600: %+v", inst.name, oldOut)
			}
			if newOut.Confirmed || newOut.Reason != ReasonVersionNotYetEffective {
				t.Fatalf("%s: new version should be not-yet-effective: %+v", inst.name, newOut)
			}
		} else {
			// 交接点及之后：旧版已失效（即使登记结束时间尚未到来），新版确认 180*4=720。
			if oldOut.Confirmed || oldOut.Reason != ReasonVersionExpired {
				t.Fatalf("%s: old version should be expired: %+v", inst.name, oldOut)
			}
			if !newOut.Confirmed || newOut.UnitPrice != 180 || newOut.Total != 720 {
				t.Fatalf("%s: new version should confirm 180/720: %+v", inst.name, newOut)
			}
		}

		// 拒绝是正常受理：err 为空、未确认、单价总价为 0，且保留原请求版本（不自动换版）。
		for _, rej := range []Outcome{oldOut, newOut} {
			if rej.Confirmed {
				continue
			}
			if rej.UnitPrice != 0 || rej.Total != 0 {
				t.Fatalf("%s: rejection must carry zero amounts: %+v", inst.name, rej)
			}
		}
		if !oldOut.Confirmed && oldOut.Request.VersionID != "seat-v1" {
			t.Fatalf("%s: rejection must keep requested version: %+v", inst.name, oldOut)
		}

		// 确认结果保留请求内容与该时刻的实际受理时刻。
		for _, conf := range []Outcome{oldOut, newOut} {
			if !conf.Confirmed {
				continue
			}
			if conf.Request.ItemID != "seat" || conf.Request.Quantity != 4 {
				t.Fatalf("%s: confirmed outcome must keep request: %+v", inst.name, conf)
			}
			if !conf.AcceptedAt.Equal(inst.at) {
				t.Fatalf("%s: accepted-at %v, want instant %v", inst.name, conf.AcceptedAt, inst.at)
			}
		}
	}
}

// 交接前确认的 600 分报价，交接后按原标识查询仍保留首次结果，不按当前费率重算。
func TestHandoffConfirmedQuoteSurvivesLookup(t *testing.T) {
	now, setNow := fixedClock(handoffUTC.Add(-time.Hour))
	b := NewBook(WithClock(now))
	registerHandoffVersions(t, b)

	first, err := b.Quote(QuoteRequest{
		RequestID: "pre-handoff", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil || !first.Confirmed || first.Total != 600 {
		t.Fatalf("pre-handoff quote: %v %+v", err, first)
	}

	// 推进到交接点之后，旧版对新请求已失效。
	setNow(handoffUTC.Add(time.Hour))
	stale, err := b.Quote(QuoteRequest{
		RequestID: "post-handoff-old", ItemID: "seat", VersionID: "seat-v1", Quantity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Confirmed || stale.Reason != ReasonVersionExpired {
		t.Fatalf("old version must be expired after handoff: %+v", stale)
	}

	// 原标识查询：版本、数量、单价、总价与首次受理时刻全部保留。
	got, err := b.Lookup("pre-handoff")
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("lookup recomputed the confirmed quote: %+v -> %+v", first, got)
	}
	if got.Request.VersionID != "seat-v1" || got.Request.Quantity != 4 ||
		got.UnitPrice != 150 || got.Total != 600 {
		t.Fatalf("confirmed record must keep original amounts: %+v", got)
	}
	if !got.AcceptedAt.Equal(handoffUTC.Add(-time.Hour)) {
		t.Fatalf("accepted-at changed: %v", got.AcceptedAt)
	}
}
