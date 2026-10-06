package domain

import "testing"

func TestNarrower(t *testing.T) {
	plan := Scope{
		Read:  []string{"src"},
		Write: []string{"src"},
	}
	cases := []struct {
		name string
		step Scope
		want bool // want Narrower to succeed (nil error)
	}{
		{"empty step scope inherits", Scope{}, true},
		{"read under plan read", Scope{Read: []string{"src/a/b.go"}}, true},
		{"read equal to plan read", Scope{Read: []string{"src"}}, true},
		{"read outside plan read", Scope{Read: []string{"docs"}}, false},
		{"write under plan write", Scope{Write: []string{"src/x.go"}}, true},
		{"write outside plan write", Scope{Write: []string{"cmd"}}, false},
		// write entries satisfy read too
		{"write covers read need", Scope{Read: []string{"src/a"}, Write: []string{"src"}}, true},
		{"write does not cover outside read", Scope{Read: []string{"docs"}, Write: []string{"src"}}, false},
		{"prefix must be segment aligned", Scope{Read: []string{"src/ab"}}, true},
	}
	// plan "src/a" does not cover "src/ab"
	if err := Narrower(Scope{Read: []string{"src/a"}, Write: []string{"src/a"}}, Scope{Read: []string{"src/ab"}}); err == nil {
		t.Error("plan src/a must not cover step src/ab")
	}
	for _, tc := range cases {
		err := Narrower(plan, tc.step)
		if tc.want && err != nil {
			t.Errorf("%s: Narrower = %v, want nil", tc.name, err)
		}
		if !tc.want && err == nil {
			t.Errorf("%s: Narrower = nil, want unprocessable error", tc.name)
		}
	}
}

func TestNarrowerForbidden(t *testing.T) {
	plan := Scope{Read: []string{"src"}, Write: []string{"src"}, Forbidden: []string{"src/secret.go"}}
	// a step path falling under plan.forbidden -> rejected even if read/write cover it
	err := Narrower(plan, Scope{Write: []string{"src/secret.go"}})
	if err == nil {
		t.Error("step writing a forbidden path must be rejected")
	}
	// step not touching forbidden paths is fine
	if err := Narrower(plan, Scope{Write: []string{"src/ok.go"}}); err != nil {
		t.Errorf("step not touching forbidden paths: Narrower = %v, want nil", err)
	}
}

func TestNarrowerErrorKind(t *testing.T) {
	err := Narrower(Scope{Read: []string{"src"}}, Scope{Read: []string{"docs"}})
	de, ok := err.(*Error)
	if !ok {
		t.Fatalf("Narrower error is %T, want *domain.Error", err)
	}
	if de.Kind != Unprocessable {
		t.Errorf("kind = %v, want Unprocessable", de.Kind)
	}
}
