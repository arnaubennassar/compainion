package domain

import (
	"reflect"
	"testing"
)

func TestWouldCycle(t *testing.T) {
	deps := map[string][]string{
		"A": {"B"},
		"B": {"C"},
		"C": {},
	}
	cases := []struct {
		name      string
		stepID    string
		dependsOn string
		want      bool
	}{
		{"self dependency", "A", "A", true},
		{"direct cycle", "B", "A", true},
		{"transitive cycle", "C", "A", true},
		{"acyclic forward", "X", "B", false},
		{"new dep not reachable", "A", "C", false},
		{"disjoint nodes", "A", "X", false},
	}
	for _, tc := range cases {
		if got := WouldCycle(deps, tc.stepID, tc.dependsOn); got != tc.want {
			t.Errorf("%s: WouldCycle(%s -> %s) = %v, want %v", tc.name, tc.stepID, tc.dependsOn, got, tc.want)
		}
	}
}

func TestWouldCycleSelfOnly(t *testing.T) {
	if !WouldCycle(map[string][]string{}, "X", "X") {
		t.Error("self dependency must always cycle")
	}
}

func TestReady(t *testing.T) {
	status := map[string]string{
		"A": "pending",
		"B": "pending",
		"C": "in_progress",
		"D": "done",
		"E": "pending",
	}
	deps := map[string][]string{
		"B": {"D"},
		"E": {"C"},
	}
	got := Ready(status, deps)
	if !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("Ready = %v, want [A B] (A: no deps, B: dep done; C not pending, E dep not done)", got)
	}
}

func TestReadyZeroDepsPending(t *testing.T) {
	got := Ready(map[string]string{"G": "pending", "H": "done"}, map[string][]string{})
	if !reflect.DeepEqual(got, []string{"G"}) {
		t.Errorf("Ready = %v, want [G]", got)
	}
}

func TestReadySorted(t *testing.T) {
	status := map[string]string{"Z": "pending", "M": "pending", "A": "pending"}
	deps := map[string][]string{}
	got := Ready(status, deps)
	want := []string{"A", "M", "Z"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Ready = %v, want %v (sorted ascending)", got, want)
	}
}

func TestReadyMissingDepStatus(t *testing.T) {
	// a dep with no known status is not done -> step not ready
	got := Ready(map[string]string{"A": "pending"}, map[string][]string{"A": {"GHOST"}})
	if len(got) != 0 {
		t.Errorf("Ready = %v, want empty", got)
	}
}
