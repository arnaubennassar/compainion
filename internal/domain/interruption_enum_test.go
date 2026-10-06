package domain

import "testing"

func TestAnswerTypesEnum(t *testing.T) {
	want := []string{"choice", "multi_choice", "free_text", "confirm"}
	if len(AnswerTypes) != len(want) {
		t.Fatalf("AnswerTypes = %v, want %v", AnswerTypes, want)
	}
	for i, v := range want {
		if AnswerTypes[i] != v {
			t.Errorf("AnswerTypes[%d] = %q, want %q", i, AnswerTypes[i], v)
		}
	}
}
