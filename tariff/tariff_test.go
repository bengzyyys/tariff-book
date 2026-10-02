package tariff

import (
	"fmt"
	"testing"
	"time"
)

type manualClock struct{ t time.Time }

func (c *manualClock) now() time.Time  { return c.t }
func (c *manualClock) set(t time.Time) { c.t = t }

func fixedZone(hours int) *time.Location {
	return time.FixedZone(fmt.Sprintf("UTC%+d", hours), hours*3600)
}

func ptrTime(t time.Time) *time.Time { return &t }

func newTestBook(t *testing.T, at time.Time) (*Book, *manualClock) {
	t.Helper()
	clk := &manualClock{t: at}
	return New(WithClock(clk.now)), clk
}

func registerVersion(t *testing.T, b *Book, in RegisterVersionInput) {
	t.Helper()
	if err := b.RegisterVersion(in); err != nil {
		t.Fatalf("RegisterVersion(%s/%s): %v", in.Item, in.ID, err)
	}
}

func quote(t *testing.T, b *Book, req QuoteRequest) *QuoteResult {
	t.Helper()
	res, err := b.Quote(req)
	if err != nil {
		t.Fatalf("Quote(%s): %v", req.RequestID, err)
	}
	return res
}
