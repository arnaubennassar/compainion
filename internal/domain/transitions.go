package domain

type Transitions map[string][]string

func (t Transitions) Check(from, to string) error {
	if from == to {
		return nil
	}
	for _, a := range t[from] {
		if a == to {
			return nil
		}
	}
	return Errf(Conflict, "invalid transition %s -> %s", from, to)
}

var StepTransitions = Transitions{
	"pending":     {"in_progress", "cancelled"},
	"in_progress": {"done", "failed", "blocked", "interrupted"},
	"blocked":     {"pending", "cancelled"},
	"failed":      {"pending", "cancelled"},
	"interrupted": {"pending", "cancelled"},
}

var TaskTransitions = StepTransitions

var PlanTransitions = Transitions{
	"draft":    {"approved", "cancelled"},
	"approved": {"running", "cancelled", "draft"},
	"running":  {"blocked", "done", "failed", "cancelled"},
	"blocked":  {"running", "failed", "cancelled"},
}

var AgentTransitions = Transitions{
	"starting": {"running", "waiting", "lost", "failed", "finished"},
	"running":  {"starting", "waiting", "lost", "failed", "finished"},
	"waiting":  {"starting", "running", "lost", "failed", "finished"},
	"lost":     {"running", "failed", "finished"},
}
