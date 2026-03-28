package hosting

import (
	"fmt"
	"sync"
	"testing"
)

func TestOperatorMapper_RegisterAndLookup(t *testing.T) {
	om := NewOperatorMapper()
	om.Register("cf1", "op1")
	om.Register("cf2", "op2")

	got, ok := om.OperatorFor("cf1")
	if !ok || got != "op1" {
		t.Errorf("cf1: want (op1, true), got (%q, %v)", got, ok)
	}
	got, ok = om.OperatorFor("cf2")
	if !ok || got != "op2" {
		t.Errorf("cf2: want (op2, true), got (%q, %v)", got, ok)
	}
}

func TestOperatorMapper_UnknownCampfire(t *testing.T) {
	om := NewOperatorMapper()
	_, ok := om.OperatorFor("unknown")
	if ok {
		t.Error("expected false for unknown campfire")
	}
}

func TestOperatorMapper_OverwriteMapping(t *testing.T) {
	om := NewOperatorMapper()
	om.Register("cf1", "op1")
	om.Register("cf1", "op2") // overwrite
	got, ok := om.OperatorFor("cf1")
	if !ok || got != "op2" {
		t.Errorf("overwrite: want (op2, true), got (%q, %v)", got, ok)
	}
}

func TestOperatorMapper_ThreadSafe(t *testing.T) {
	om := NewOperatorMapper()
	const n = 200
	var wg sync.WaitGroup
	wg.Add(n * 2)

	// Half writers, half readers, all concurrent.
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			om.Register(fmt.Sprintf("cf%d", i), fmt.Sprintf("op%d", i))
		}()
		go func() {
			defer wg.Done()
			om.OperatorFor(fmt.Sprintf("cf%d", i))
		}()
	}
	wg.Wait()
	// Verify all campfires can be read back after concurrent writes settle.
	for i := 0; i < n; i++ {
		got, ok := om.OperatorFor(fmt.Sprintf("cf%d", i))
		if !ok {
			t.Errorf("cf%d not found after concurrent write", i)
			continue
		}
		if got != fmt.Sprintf("op%d", i) {
			t.Errorf("cf%d: want op%d, got %s", i, i, got)
		}
	}
}
