package ids

import "testing"

func TestNew(t *testing.T) {
	a := New()
	if len(a) != 26 {
		t.Fatalf("New() = %q, length %d, want 26", a, len(a))
	}
}

func TestNewSortsAscending(t *testing.T) {
	prev := ""
	for i := 0; i < 100; i++ {
		id := New()
		if id <= prev {
			t.Fatalf("ids not ascending: prev=%s id=%s at i=%d", prev, id, i)
		}
		prev = id
	}
}