package llm

import "testing"

func TestStripReasoning(t *testing.T) {
	cases := map[string]string{
		"<think>hmm</think>\nAnswer":    "Answer",
		"reasoning only</think> Answer": "Answer",
		"  Plain answer ":               "Plain answer",
	}
	for in, want := range cases {
		if got := StripReasoning(in); got != want {
			t.Errorf("StripReasoning(%q) = %q, want %q", in, got, want)
		}
	}
}
