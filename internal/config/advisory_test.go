package config

import (
	"testing"
)

func TestServiceConfig_Advisories_AppendsInArgumentOrder(t *testing.T) {
	t.Parallel()

	var c ServiceConfig
	if got := c.Advisories(); got == nil || len(got) != 0 {
		t.Fatalf("Advisories() before any AddAdvisories call = %v, want an empty, non-nil slice", got)
	}

	c.AddAdvisories(Advisory{Message: "m1"})
	c.AddAdvisories(Advisory{Message: "m2"}, Advisory{Message: "m3"})

	got := c.Advisories()
	want := []string{"m1", "m2", "m3"}
	if len(got) != len(want) {
		t.Fatalf("Advisories() = %v, want %d entries", got, len(want))
	}
	for i, w := range want {
		if got[i].Message != w {
			t.Errorf("Advisories()[%d].Message = %q, want %q", i, got[i].Message, w)
		}
	}
}

// TestServiceConfig_Advisories_CopySemantics proves that appending to a
// slice Advisories() returns never reaches c, even when c's own backing
// array has spare capacity: a later AddAdvisories call must not silently
// overwrite what an earlier caller appended.
func TestServiceConfig_Advisories_CopySemantics(t *testing.T) {
	t.Parallel()

	backing := make([]Advisory, 1, 2)
	backing[0] = Advisory{Message: "a1"}
	c := ServiceConfig{advisories: backing}

	got := c.Advisories()
	got = append(got, Advisory{Message: "ghost"})
	c.AddAdvisories(Advisory{Message: "a2"})

	if len(got) != 2 || got[1].Message != "ghost" {
		t.Fatalf("appending to Advisories() = %+v, want [a1 ghost] unaffected by a later AddAdvisories call", got)
	}
	if final := c.Advisories(); len(final) != 2 || final[0].Message != "a1" || final[1].Message != "a2" {
		t.Errorf("c.Advisories() after AddAdvisories = %+v, want [a1 a2]", final)
	}
}
