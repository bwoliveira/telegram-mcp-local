package panel

import "testing"

func TestPanelHydrationAggregatesDurableStatusOnlyOnce(t *testing.T) {
	p, h, eng := newStatusFixture(t)
	reads := p.ReadStore.(*statusFixtureStore)
	response := fixtureGET(t, h, "/api/panel/status")
	if response.Code != 200 {
		t.Fatalf("status: HTTP %d", response.Code)
	}
	if eng.calls.Load() != 0 || reads.statusReads.Load() != 1 {
		t.Fatalf("durable status: enriched engine calls=%d, direct aggregate calls=%d; want 0 and 1", eng.calls.Load(), reads.statusReads.Load())
	}
}
