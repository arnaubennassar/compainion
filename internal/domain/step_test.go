package domain

import "testing"

func TestStepTransitions(t *testing.T) {
	allowed := [][2]string{
		{"pending", "in_progress"},
		{"pending", "cancelled"},
		{"in_progress", "done"},
		{"in_progress", "failed"},
		{"in_progress", "blocked"},
		{"in_progress", "interrupted"},
		{"blocked", "pending"},
		{"blocked", "cancelled"},
		{"failed", "pending"},
		{"failed", "cancelled"},
		{"interrupted", "pending"},
		{"interrupted", "cancelled"},
	}
	for _, p := range allowed {
		if err := StepTransitions.Check(p[0], p[1]); err != nil {
			t.Errorf("StepTransitions.Check(%s, %s) = %v, want nil", p[0], p[1], err)
		}
	}
}

func TestStepTransitionsRejected(t *testing.T) {
	rejected := [][2]string{
		{"done", "pending"},
		{"pending", "done"},
		{"pending", "blocked"},
		{"cancelled", "pending"},
		{"done", "failed"},
		{"blocked", "done"},
		{"", "pending"}, // ready is derived, never a stored status
		{"ready", "in_progress"},
	}
	for _, p := range rejected {
		err := StepTransitions.Check(p[0], p[1])
		if err == nil {
			t.Errorf("StepTransitions.Check(%s, %s) = nil, want conflict error", p[0], p[1])
			continue
		}
		de, ok := err.(*Error)
		if !ok {
			t.Fatalf("Check(%s, %s) error is %T, want *domain.Error", p[0], p[1], err)
		}
		if de.Kind != Conflict {
			t.Errorf("Check(%s, %s) kind = %v, want Conflict", p[0], p[1], de.Kind)
		}
	}
}

func TestTaskTransitionsMatchStep(t *testing.T) {
	if len(TaskTransitions) == 0 {
		t.Fatal("TaskTransitions is empty")
	}
	for from, tos := range StepTransitions {
		got, ok := TaskTransitions[from]
		if !ok {
			t.Errorf("TaskTransitions missing from-state %q", from)
			continue
		}
		if len(got) != len(tos) {
			t.Errorf("TaskTransitions[%q] = %v, want %v", from, got, tos)
		}
	}
}

func TestPlanTransitions(t *testing.T) {
	allowed := [][2]string{
		{"draft", "approved"},
		{"draft", "cancelled"},
		{"approved", "running"},
		{"approved", "cancelled"},
		{"approved", "draft"},
		{"running", "blocked"},
		{"running", "done"},
		{"running", "failed"},
		{"running", "cancelled"},
		{"blocked", "running"},
		{"blocked", "failed"},
		{"blocked", "cancelled"},
	}
	for _, p := range allowed {
		if err := PlanTransitions.Check(p[0], p[1]); err != nil {
			t.Errorf("PlanTransitions.Check(%s, %s) = %v, want nil", p[0], p[1], err)
		}
	}
	rejected := [][2]string{
		{"draft", "running"},
		{"done", "running"},
		{"failed", "draft"},
		{"cancelled", "draft"},
		{"blocked", "approved"},
	}
	for _, p := range rejected {
		if err := PlanTransitions.Check(p[0], p[1]); err == nil {
			t.Errorf("PlanTransitions.Check(%s, %s) = nil, want conflict error", p[0], p[1])
		}
	}
}

func TestAgentTransitions(t *testing.T) {
	terminal := map[string]bool{"finished": true, "failed": true}
	statuses := []string{"starting", "running", "waiting", "lost", "finished", "failed"}
	for _, from := range statuses {
		for _, to := range statuses {
			err := AgentTransitions.Check(from, to)
			if terminal[from] {
				if err == nil && from != to {
					t.Errorf("AgentTransitions.Check(%s, %s) = nil, want conflict (terminal)", from, to)
				}
				continue
			}
			if from == "lost" && to != "lost" && !(to == "running" || to == "failed" || to == "finished") {
				if err == nil {
					t.Errorf("AgentTransitions.Check(lost, %s) = nil, want conflict", to)
				}
				continue
			}
			if err != nil {
				t.Errorf("AgentTransitions.Check(%s, %s) = %v, want nil", from, to, err)
			}
		}
	}
	// self-transition on a non-terminal status is a no-op and allowed
	if err := AgentTransitions.Check("running", "running"); err != nil {
		t.Errorf("AgentTransitions.Check(running, running) = %v, want nil", err)
	}
	if err := AgentTransitions.Check("finished", "finished"); err != nil {
		t.Errorf("AgentTransitions.Check(finished, finished) = %v, want nil (idempotent no-op)", err)
	}
}
