package domain

import (
	"reflect"
	"testing"
)

func TestValidateQuestions(t *testing.T) {
	ok := []Question{
		{Text: "Proceed?", AnswerType: "choice", Suggestions: []Suggestion{{Label: "yes"}, {Label: "no"}}},
		{Text: "Which?", AnswerType: "choice", Suggestions: []Suggestion{{Label: "a"}, {Label: "b", Recommended: true}}},
	}
	if err := ValidateQuestions(ok); err != nil {
		t.Errorf("ValidateQuestions(ok) = %v, want nil", err)
	}
}

func TestValidateQuestionsNeedsSuggestion(t *testing.T) {
	qs := []Question{{Text: "q", AnswerType: "choice"}}
	err := ValidateQuestions(qs)
	de, isDE := err.(*Error)
	if !isDE {
		t.Fatalf("ValidateQuestions error is %T, want *domain.Error", err)
	}
	if de.Kind != Unprocessable {
		t.Errorf("kind = %v, want Unprocessable", de.Kind)
	}
	// a question with an empty suggestions slice is also rejected
	err = ValidateQuestions([]Question{{Text: "q", AnswerType: "choice", Suggestions: []Suggestion{}}})
	if err == nil {
		t.Error("empty suggestions must be rejected")
	}
	// free_text questions are exempt: the plain text answer is the point.
	for _, at := range []string{"choice", "confirm"} {
		if err := ValidateQuestions([]Question{{Text: "q", AnswerType: at}}); err == nil {
			t.Errorf("answer_type %q without suggestions must be rejected", at)
		}
	}
	if err := ValidateQuestions([]Question{{Text: "q", AnswerType: "free_text"}}); err != nil {
		t.Errorf("free_text without suggestions must be accepted, got %v", err)
	}
	if err := ValidateQuestions([]Question{{Text: "q", AnswerType: "free_text", Suggestions: []Suggestion{}}}); err != nil {
		t.Errorf("free_text with empty suggestions must be accepted, got %v", err)
	}
}

func TestValidateQuestionsOneRecommended(t *testing.T) {
	qs := []Question{{
		Text: "q", AnswerType: "choice",
		Suggestions: []Suggestion{{Label: "a", Recommended: true}, {Label: "b", Recommended: true}},
	}}
	if err := ValidateQuestions(qs); err == nil {
		t.Error("two recommended suggestions in one question must be rejected")
	}
}

func TestValidateQuestionsAnswerType(t *testing.T) {
	qs := []Question{{
		Text: "q", AnswerType: "shouting",
		Suggestions: []Suggestion{{Label: "a"}},
	}}
	err := ValidateQuestions(qs)
	if err == nil {
		t.Error("unknown answer_type must be rejected")
	}
	for _, at := range AnswerTypes {
		qs[0].AnswerType = at
		if err := ValidateQuestions(qs); err != nil {
			t.Errorf("answer_type %q should be valid, got %v", at, err)
		}
	}
}

func TestValidateQuestionsPositionsAssigned(t *testing.T) {
	qs := []Question{
		{Text: "a", AnswerType: "choice", Suggestions: []Suggestion{{Label: "y"}}},
		{Text: "b", AnswerType: "choice", Suggestions: []Suggestion{{Label: "a"}}},
	}
	if err := ValidateQuestions(qs); err != nil {
		t.Fatalf("ValidateQuestions = %v, want nil", err)
	}
	if qs[0].Position != 0 || qs[1].Position != 1 {
		t.Errorf("positions = %d, %d; want 0, 1 (auto-assigned)", qs[0].Position, qs[1].Position)
	}
}

func TestValidateQuestionsDuplicatePositionRejected(t *testing.T) {
	qs := []Question{
		{Position: 3, Text: "a", AnswerType: "choice", Suggestions: []Suggestion{{Label: "y"}}},
		{Position: 3, Text: "b", AnswerType: "choice", Suggestions: []Suggestion{{Label: "a"}}},
	}
	if err := ValidateQuestions(qs); err == nil {
		t.Error("duplicate positions must be rejected")
	}
}

func TestLess(t *testing.T) {
	cases := []struct {
		name string
		a, b Interruption
		want bool // want Less(a, b)
	}{
		{"urgent before normal", Interruption{Priority: "urgent"}, Interruption{Priority: "normal"}, true},
		{"high before low", Interruption{Priority: "high"}, Interruption{Priority: "low"}, true},
		{"low after urgent", Interruption{Priority: "low"}, Interruption{Priority: "urgent"}, false},
		{"blocking first within priority", Interruption{Priority: "normal", Blocking: true}, Interruption{Priority: "normal"}, true},
		{"older created_at first", Interruption{Priority: "normal", CreatedAt: "01HZGWEV8AAAAA"}, Interruption{Priority: "normal", CreatedAt: "01HZGWEV8BBBBBB"}, true},
		{"equal is not less", Interruption{Priority: "normal"}, Interruption{Priority: "normal"}, false},
	}
	for _, tc := range cases {
		if got := Less(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: Less(a, b) = %v, want %v", tc.name, got, tc.want)
		}
		// asymmetry sanity where meaningful
		if got := Less(tc.b, tc.a); got == tc.want && tc.name != "equal is not less" && tc.name != "low after urgent" {
			t.Errorf("%s: Less(b, a) = %v, expected opposite of Less(a, b)", tc.name, got)
		}
	}
}

func TestLessBlockingOverridesAge(t *testing.T) {
	a := Interruption{Priority: "normal", CreatedAt: "01HZGWEV8AAAAA", Blocking: true}
	b := Interruption{Priority: "normal", CreatedAt: "01HZGWEV8AAAA9"} // older, not blocking
	if !Less(a, b) {
		t.Error("blocking must win over age within the same priority class")
	}
	if Less(b, a) {
		t.Error("Less(b, a) must be false")
	}
}

func TestSortWithLess(t *testing.T) {
	in := []Interruption{
		{Priority: "low"},
		{Priority: "urgent"},
		{Priority: "normal", Blocking: true},
		{Priority: "normal"},
	}
	sorted := append([]Interruption(nil), in...)
	// simple insertion sort using Less
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && Less(sorted[j], sorted[j-1]); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	want := []Interruption{
		{Priority: "urgent"},
		{Priority: "normal", Blocking: true},
		{Priority: "normal"},
		{Priority: "low"},
	}
	if !reflect.DeepEqual(sorted, want) {
		t.Errorf("sort result = %+v, want %+v", sorted, want)
	}
}
