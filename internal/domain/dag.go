package domain

import "sort"

// deps: stepID -> ids it depends on.
func WouldCycle(deps map[string][]string, stepID, dependsOn string) bool {
	if stepID == dependsOn {
		return true
	}
	seen := map[string]bool{}
	var walk func(n string) bool
	walk = func(n string) bool { // can we reach stepID from n following depends-on edges?
		if n == stepID {
			return true
		}
		if seen[n] {
			return false
		}
		seen[n] = true
		for _, d := range deps[n] {
			if walk(d) {
				return true
			}
		}
		return false
	}
	return walk(dependsOn)
}

func Ready(status map[string]string, deps map[string][]string) []string {
	var out []string
	for id, st := range status {
		if st != "pending" {
			continue
		}
		ok := true
		for _, d := range deps[id] {
			if status[d] != "done" {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, id)
		}
	}
	sort.Strings(out) // ULIDs => creation order
	return out
}
