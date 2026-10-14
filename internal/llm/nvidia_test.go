package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestChatRetriesABusyModelOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":{"message":"Service temporarily overloaded"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer srv.Close()
	c := New("k", []string{"first", "second"})
	c.URL, c.BusyWait = srv.URL, time.Millisecond
	answer, model, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil || answer != "hello" || model != "first" {
		t.Fatalf("want the first model's answer after one retry, got %q from %q, %v", answer, model, err)
	}
}
