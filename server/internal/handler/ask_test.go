package handler

import "testing"

func TestValidateAskQuestions(t *testing.T) {
	valid := []AskQuestion{{Text: "Where?", Options: []AskOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B", Recommended: true}}}}
	if err := validateAskQuestions(valid); err != nil {
		t.Fatalf("valid question rejected: %v", err)
	}
	for name, questions := range map[string][]AskQuestion{
		"too many questions": make([]AskQuestion, 5),
		"too few options":    {{Text: "Where?", Options: []AskOption{{ID: "a", Label: "A"}}}},
		"duplicate options":  {{Text: "Where?", Options: []AskOption{{ID: "a", Label: "A"}, {ID: "a", Label: "Again"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateAskQuestions(questions); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
