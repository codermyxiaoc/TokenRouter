package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeriveOpenAIContentSessionSeed_ResponsesLeadingPrefix(t *testing.T) {
	makeBody := func(items ...string) []byte {
		return []byte(`{"model":"gpt-5.4","instructions":"Be concise.","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[` + strings.Join(items, ",") + `]}`)
	}
	prefix := []string{
		`{"role":"system","content":"System A"}`,
		`{"role":"developer","content":[{"type":"input_text","text":"Developer B"}]}`,
	}
	boundaries := []struct {
		name string
		item string
	}{
		{"user", `{"role":"user","content":"First question"}`},
		{"assistant", `{"role":"assistant","content":"Earlier answer"}`},
		{"function_call", `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}`},
		{"function_call_output", `{"type":"function_call_output","call_id":"call_1","output":"Result"}`},
		{"input_text", `{"type":"input_text","text":"First question"}`},
	}
	for _, boundary := range boundaries {
		for _, role := range []string{"system", "developer"} {
			t.Run(boundary.name+"/"+role, func(t *testing.T) {
				items := append(append([]string(nil), prefix...), boundary.item)
				baseline := makeBody(append(items, `{"role":"user","content":"First question"}`)...)
				reminder := `{"role":"` + role + `","content":[{"type":"input_text","text":"Dynamic reminder"}]}`
				continued := makeBody(append(items, reminder, `{"role":"user","content":"First question"}`, `{"role":"user","content":"Follow-up"}`)...)
				seed := deriveOpenAIContentSessionSeed(baseline)
				require.Contains(t, seed, "System A")
				require.Contains(t, seed, "Developer B")
				require.Contains(t, seed, "First question")
				require.Equal(t, seed, deriveOpenAIContentSessionSeed(continued), "later reminders must not change the session identity")
			})
		}
	}

	t.Run("stable fields still distinguish sessions", func(t *testing.T) {
		body := makeBody(append(prefix, `{"role":"user","content":"First question"}`)...)
		seed := deriveOpenAIContentSessionSeed(body)
		for _, change := range [][2]string{
			{"gpt-5.4", "gpt-5.5"},
			{"Be concise.", "Be detailed."},
			{"lookup", "search"},
			{"System A", "System C"},
			{"Developer B", "Developer C"},
			{"First question", "Compacted question"},
		} {
			t.Run(change[0], func(t *testing.T) {
				require.NotEqual(t, seed, deriveOpenAIContentSessionSeed([]byte(strings.ReplaceAll(string(body), change[0], change[1]))))
			})
		}
	})
}
