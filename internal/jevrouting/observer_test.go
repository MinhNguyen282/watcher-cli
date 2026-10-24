package jevrouting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func testInput() Input {
	return Input{Prompt: "Fix a failing test", Models: map[string]string{"large": "provider/coder", "fast": "provider/mini"},
		Skills: map[string]string{"agent_decides": "Agent chooses", "skill_0": "debug: Debug failures"}, DefaultModel: "large"}
}

func testAnswers(in Input) map[string]any {
	out := make(map[string]any)
	for id, q := range questions(in) {
		switch q.Type {
		case "choice":
			choice := map[string]string{"intent": "debug", "model_tier": "fast", "skill": "skill_0"}[id]
			p := make(map[string]float64)
			for key := range q.Criteria.(map[string]string) {
				p[key] = 0
			}
			p[choice] = 1
			out[id] = map[string]any{"type": "choice", "choice": choice, "confidence": 1, "probabilities": p}
		case "score":
			out[id] = map[string]any{"type": "score", "score": 1, "confidence": 1, "probabilities": map[string]float64{"0": 0, "1": 1, "2": 0}}
		case "noul":
			out[id] = map[string]any{"type": "noul", "noul": 0.9}
		}
	}
	return out
}

func TestObserve(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"success", "low_confidence", "unknown_model", "unknown_skill", "missing_answer", "missing_confidence", "invalid_distribution", "invalid_score", "invalid_noul", "http_error", "invalid_json"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := testInput()
			before, err := json.Marshal(in)
			require.NoError(t, err)
			answers := testAnswers(in)
			answer := func(id string) map[string]any { return answers[id].(map[string]any) }
			switch name {
			case "low_confidence":
				answer("model_tier")["confidence"] = 0.2
			case "unknown_model":
				answer("model_tier")["choice"] = "unconfigured"
			case "unknown_skill":
				answer("skill")["choice"] = "disabled_skill"
			case "missing_answer":
				delete(answers, "intent")
			case "missing_confidence":
				delete(answer("skill"), "confidence")
			case "invalid_distribution":
				answer("intent")["probabilities"] = map[string]float64{"debug": 2}
			case "invalid_score":
				answer("complexity")["score"] = 3
			case "invalid_noul":
				answer("needs_review")["noul"] = -1
			}
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, "POST", r.Method)
				require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
				var request struct {
					Model     string
					State     Input
					Questions map[string]question
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, "jev-latest", request.Model)
				require.Equal(t, in, request.State)
				require.Len(t, request.Questions, 5)
				if name == "http_error" {
					w.WriteHeader(429)
					return
				}
				if name == "invalid_json" {
					_, _ = w.Write([]byte("not JSON"))
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"model": "jev-test", "answers": answers}))
			}))
			defer srv.Close()
			got, err := Observe(t.Context(), srv.Client(), srv.URL, "test-key", "", in)
			require.Equal(t, int32(1), calls.Load())
			after, marshalErr := json.Marshal(in)
			require.NoError(t, marshalErr)
			require.Equal(t, before, after)
			if name != "success" && name != "low_confidence" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Contains(t, got.Summary(), "Jev observe:")
			if name == "low_confidence" {
				require.True(t, got.Fallback)
				require.Equal(t, "large", got.SuggestedModel)
				require.Equal(t, "agent_decides", got.SuggestedSkill)
				require.Equal(t, "fast", got.Answers["model_tier"].Choice)
			} else {
				require.False(t, got.Fallback)
				require.Equal(t, "fast", got.SuggestedModel)
				require.Equal(t, "skill_0", got.SuggestedSkill)
			}
		})
	}
}

func TestObserveMissingKeyAndCancellation(t *testing.T) {
	t.Parallel()
	_, err := Observe(t.Context(), http.DefaultClient, Endpoint, "", "", testInput())
	require.ErrorContains(t, err, "not configured")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Observe(ctx, http.DefaultClient, Endpoint, "test-key", "", testInput())
	require.Error(t, err)
}
