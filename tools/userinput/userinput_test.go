package userinput

import "testing"

func TestCheckTakesOneAnswerForEachQuestionAsked(t *testing.T) {
	r := Request{Questions: []Question{{ID: "a", Header: "A", Question: "Which?"}, {ID: "b", Header: "B", Question: "Why?"}}}
	if err := r.Check(Answers{"a": {Answers: []string{"this"}}, "b": {Answers: []string{"because"}}}, 100); err != nil {
		t.Fatalf("answers to both: %v", err)
	}
	for name, answers := range map[string]Answers{
		"one missing":       {"a": {Answers: []string{"this"}}},
		"a blank answer":    {"a": {Answers: []string{" "}}, "b": {Answers: []string{"because"}}},
		"one never asked":   {"a": {Answers: []string{"this"}}, "b": {Answers: []string{"because"}}, "c": {Answers: []string{"extra"}}},
		"over the limit":    {"a": {Answers: []string{"this one is long"}}, "b": {Answers: []string{"and so is this one"}}},
		"no answers at all": {},
	} {
		limit := 100
		if name == "over the limit" {
			limit = 10
		}
		if err := r.Check(answers, limit); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestQuestionsNeedDistinctIDsAndText(t *testing.T) {
	for name, r := range map[string]Request{
		"a repeated id":  {Questions: []Question{{ID: "a", Header: "A", Question: "?"}, {ID: "a", Header: "B", Question: "?"}}},
		"a blank header": {Questions: []Question{{ID: "a", Header: " ", Question: "?"}}},
		"a blank option": {Questions: []Question{{ID: "a", Header: "A", Question: "?", Options: []Option{{Label: ""}}}}},
	} {
		if err := r.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
