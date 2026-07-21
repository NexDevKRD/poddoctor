package alertgroup

import (
	"testing"
	"time"
)

func TestGrouper_FirstObserveAlwaysNotifies(t *testing.T) {
	g := New(time.Minute)
	ok, suppressed := g.Observe("default/OOMKilled")
	if !ok || suppressed != 0 {
		t.Fatalf("first Observe: ok=%v suppressed=%d, want true,0", ok, suppressed)
	}
}

func TestGrouper_SuppressesWithinWindow(t *testing.T) {
	g := New(time.Minute)
	g.Observe("default/OOMKilled")

	for i := 0; i < 5; i++ {
		if ok, _ := g.Observe("default/OOMKilled"); ok {
			t.Fatalf("Observe #%d within window: expected suppressed (ok=false), got ok=true", i)
		}
	}
}

func TestGrouper_ReportsSuppressedCountOnNextWindow(t *testing.T) {
	g := New(10 * time.Millisecond)
	g.Observe("default/OOMKilled")
	g.Observe("default/OOMKilled")
	g.Observe("default/OOMKilled")

	time.Sleep(15 * time.Millisecond)

	ok, suppressed := g.Observe("default/OOMKilled")
	if !ok {
		t.Fatalf("expected notify after window expiry")
	}
	if suppressed != 2 {
		t.Fatalf("suppressed = %d, want 2", suppressed)
	}
}

func TestGrouper_DifferentKeysAreIndependent(t *testing.T) {
	g := New(time.Minute)
	g.Observe("default/OOMKilled")

	if ok, _ := g.Observe("other-ns/OOMKilled"); !ok {
		t.Fatalf("a different namespace key should notify independently")
	}
	if ok, _ := g.Observe("default/ImagePullError"); !ok {
		t.Fatalf("a different root cause key should notify independently")
	}
}
