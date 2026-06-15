package engine

import (
	"sync"
	"testing"
)

func TestVarsSetGet(t *testing.T) {
	v := NewVars()
	k := NewKey[int]("answer")

	if _, ok := Get(v, k); ok {
		t.Fatal("Get on empty Vars must return ok=false")
	}

	Set(v, k, 42)
	got, ok := Get(v, k)
	if !ok || got != 42 {
		t.Fatalf("Get = (%v, %v), want (42, true)", got, ok)
	}

	Set(v, k, 7)
	got, _ = Get(v, k)
	if got != 7 {
		t.Fatalf("Get after overwrite = %v, want 7", got)
	}
}

func TestVarsTypeSafety(t *testing.T) {
	v := NewVars()
	intKey := NewKey[int]("same-name")
	strKey := NewKey[string]("same-name")

	Set(v, intKey, 1)
	if got, ok := Get(v, strKey); ok {
		t.Fatalf("Get with wrong-typed key must return ok=false, got %q", got)
	}
	if got, ok := Get(v, intKey); !ok || got != 1 {
		t.Fatalf("Get with original key = (%v, %v), want (1, true)", got, ok)
	}
}

func TestVarsConcurrentAccess(t *testing.T) {
	v := NewVars()
	k := NewKey[int]("counter")
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			Set(v, k, i)
		}()
		go func() {
			defer wg.Done()
			Get(v, k)
		}()
	}
	wg.Wait()
}

func TestStatusWorse(t *testing.T) {
	cases := []struct {
		a, b, want Status
	}{
		{StatusOK, StatusWarn, StatusWarn},
		{StatusWarn, StatusOK, StatusWarn},
		{StatusWarn, StatusFailed, StatusFailed},
		{StatusSkipped, StatusOK, StatusSkipped},
		{StatusFailed, StatusSkipped, StatusFailed},
		{StatusOK, StatusOK, StatusOK},
	}
	for _, c := range cases {
		if got := c.a.Worse(c.b); got != c.want {
			t.Errorf("%v.Worse(%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
