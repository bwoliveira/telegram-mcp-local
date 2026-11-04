package gotdengine

import (
	"testing"
	"time"

	"github.com/gotd/td/clock"
)

type qrRecordingClock struct {
	clock.Clock
	durations []time.Duration
}

func (c *qrRecordingClock) Timer(d time.Duration) clock.Timer {
	c.durations = append(c.durations, d)
	return qrRecordingTimer{c: c}
}

type qrRecordingTimer struct{ c *qrRecordingClock }

func (t qrRecordingTimer) C() <-chan time.Time   { return nil }
func (t qrRecordingTimer) Stop() bool            { return true }
func (t qrRecordingTimer) Reset(d time.Duration) { t.c.durations = append(t.c.durations, d) }

func TestQRClockShortensInitialTimerAndEveryResetWithoutChangingExpiryClock(t *testing.T) {
	base := &qrRecordingClock{}
	now := time.Unix(2000, 0)
	c := qrClock{Clock: base, now: func() time.Time { return now }}
	if !c.Now().Equal(now) {
		t.Fatal("QR clock ignored configured engine clock")
	}
	timer := c.Timer(time.Minute)
	timer.Reset(30 * time.Second)
	timer.Reset(5 * time.Second)
	timer.Reset(time.Second)
	timer.Reset(0)
	timer.Reset(-time.Second)
	want := []time.Duration{55 * time.Second, 25 * time.Second, 4 * time.Second, 800 * time.Millisecond, 0, 0}
	if len(base.durations) != len(want) {
		t.Fatal(base.durations)
	}
	for i, d := range want {
		if base.durations[i] != d {
			t.Fatalf("timer %d: got %v, want %v", i, base.durations[i], d)
		}
	}
}
