package domain

import "strings"

type Scope struct {
	Read      []string `json:"read"`
	Write     []string `json:"write"`
	Forbidden []string `json:"forbidden"`
}

// covered reports whether path p is a itself or lies under a,
// matching whole path segments (src/a covers src/a/b.go, not src/ab).
func covered(p string, allowed []string) bool {
	for _, a := range allowed {
		if p == a || strings.HasPrefix(p, strings.TrimSuffix(a, "/")+"/") {
			return true
		}
	}
	return false
}

// Narrower checks that the step scope stays inside the plan scope.
// Write entries also satisfy read needs. An empty step scope inherits
// the plan scope and is always valid. No step read/write path may fall
// under a plan forbidden path. Step forbidden entries are unioned with
// the plan's by the store, so only read/write are checked here.
func Narrower(plan, step Scope) error {
	for _, p := range step.Read {
		if !covered(p, plan.Read) && !covered(p, plan.Write) {
			return Errf(Unprocessable, "step scope %q outside plan scope", p)
		}
	}
	for _, p := range step.Write {
		if !covered(p, plan.Write) {
			return Errf(Unprocessable, "step scope %q outside plan scope", p)
		}
	}
	for _, f := range plan.Forbidden {
		for _, p := range step.Read {
			if covered(p, []string{f}) {
				return Errf(Unprocessable, "step scope %q falls under plan forbidden path %q", p, f)
			}
		}
		for _, p := range step.Write {
			if covered(p, []string{f}) {
				return Errf(Unprocessable, "step scope %q falls under plan forbidden path %q", p, f)
			}
		}
	}
	return nil
}
