package domain

// AnswerTypes is the enum allowed for Question.AnswerType.
var AnswerTypes = []string{"choice", "multi_choice", "free_text", "confirm"}

type Suggestion struct {
	ID          string `json:"id,omitempty"`
	Label       string `json:"label"`
	Rationale   string `json:"rationale,omitempty"`
	Recommended bool   `json:"recommended"`
}

type Question struct {
	Position    int          `json:"position"`
	Text        string       `json:"text"`
	AnswerType  string       `json:"answer_type"`
	Group       string       `json:"group,omitempty"`
	Status      string       `json:"status,omitempty"`
	Suggestions []Suggestion `json:"suggestions"`
}

type Interruption struct {
	ID        string     `json:"id"`
	Topic     string     `json:"topic"`
	Kind      string     `json:"kind"`
	Priority  string     `json:"priority"`
	Digest    string     `json:"digest"`
	Blocking  bool       `json:"blocking"`
	Status    string     `json:"status"`
	CreatedAt string     `json:"created_at"`
	Questions []Question `json:"questions,omitempty"`
}

func validAnswerType(t string) bool {
	for _, a := range AnswerTypes {
		if a == t {
			return true
		}
	}
	return false
}

// ValidateQuestions checks the create-time rules: every question needs at
// least one suggestion, at most one suggestion may be recommended per
// question, answer_type must be in the enum, and positions must be unique.
// Positions are auto-assigned 0..n-1 when omitted.
func ValidateQuestions(qs []Question) error {
	for i := range qs {
		q := &qs[i]
		if len(q.Suggestions) == 0 {
			return Errf(Unprocessable, "question %d has no suggestions", q.Position)
		}
		rec := 0
		for _, s := range q.Suggestions {
			if s.Recommended {
				rec++
			}
		}
		if rec > 1 {
			return Errf(Unprocessable, "question %d has more than one recommended suggestion", q.Position)
		}
		if !validAnswerType(q.AnswerType) {
			return Errf(Unprocessable, "question %d has invalid answer_type %q", q.Position, q.AnswerType)
		}
	}
	// Auto-assign positions 0..n-1 only when none was supplied; otherwise
	// positions are taken as given and must be unique.
	explicit := false
	for _, q := range qs {
		if q.Position != 0 {
			explicit = true
			break
		}
	}
	if !explicit {
		for i := range qs {
			qs[i].Position = i
		}
		return nil
	}
	seen := map[int]bool{}
	for _, q := range qs {
		if seen[q.Position] {
			return Errf(Unprocessable, "duplicate question position %d", q.Position)
		}
		seen[q.Position] = true
	}
	return nil
}

var priorityRank = map[string]int{"urgent": 0, "high": 1, "normal": 2, "low": 3}

// Expiry default actions. pause closes a lapsed worker gate without approving
// anything (the worker resumes when the companion re-engages); deny closes it
// as a refusal; escalate keeps a user-facing decision alive by raising its
// priority instead of silently discarding it.
const (
	ExpiryPause    = "pause"
	ExpiryDeny     = "deny"
	ExpiryEscalate = "escalate"
)

// DefaultExpiryAction maps an interruption kind to its safe default action
// when the caller did not set one: worker approval gates default to pause
// (never auto-approve), every other kind (user decisions and clarifications)
// escalates and persists so it is never silently dropped.
func DefaultExpiryAction(kind string) string {
	if kind == "approval" {
		return ExpiryPause
	}
	return ExpiryEscalate
}

// ValidateDefaultAction checks a caller-supplied default_action against the
// enum (empty means "use the kind default", handled by the caller).
func ValidateDefaultAction(a string) error {
	switch a {
	case ExpiryPause, ExpiryDeny, ExpiryEscalate:
		return nil
	default:
		return Errf(Unprocessable, "invalid default_action %q (pause|deny|escalate)", a)
	}
}

// Less orders interruptions for presentation: priority urgent < high <
// normal < low; ties: blocking first; then older created_at (ULID compare).
func Less(a, b Interruption) bool {
	pa, pb := priorityRank[a.Priority], priorityRank[b.Priority]
	if pa != pb {
		return pa < pb
	}
	if a.Blocking != b.Blocking {
		return a.Blocking
	}
	return a.CreatedAt < b.CreatedAt
}
