// Package jevrouting observes structured routing decisions without applying them.
package jevrouting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Endpoint is the TypeSafe structured decision API.
const Endpoint = "https://api.typesafe.ai/v1/systemone"

// Input contains only the current prompt and explicitly allowed candidates.
type Input struct {
	Prompt       string            `json:"prompt"`
	Models       map[string]string `json:"models"`
	Skills       map[string]string `json:"skills"`
	DefaultModel string            `json:"default_model"`
}

type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer retains the distribution so observations can be evaluated later.
type Answer struct {
	Type          string          `json:"type"`
	Choice        string          `json:"choice,omitempty"`
	Confidence    *float64        `json:"confidence,omitempty"`
	Probabilities json.RawMessage `json:"probabilities,omitempty"`
	Legend        json.RawMessage `json:"legend,omitempty"`
	Score         *float64        `json:"score,omitempty"`
	Noul          *float64        `json:"noul,omitempty"`
}

// Observation records suggestions and the conservative fallback decision.
type Observation struct {
	Model          string            `json:"model"`
	Answers        map[string]Answer `json:"answers"`
	SuggestedModel string            `json:"suggested_model"`
	SuggestedSkill string            `json:"suggested_skill"`
	Fallback       bool              `json:"fallback"`
}

func questions(in Input) map[string]question {
	return map[string]question{
		"intent": {"choice", "What is the primary intent of `prompt`?", map[string]string{
			"code": "Implement or modify code.", "debug": "Diagnose or fix a failure.",
			"review": "Review existing work.", "explain": "Explain or answer a question.",
			"plan": "Plan work without implementing it.", "other": "Another intent or unclear.",
		}},
		"complexity": {"score", "How complex is the work requested in `prompt`?", []string{
			"Simple localized task.", "Several related changes or moderate investigation.",
			"Complex architecture, broad investigation, or difficult reasoning.",
		}},
		"model_tier":   {"choice", "Which configured model tier in `models` best fits `prompt`? Prefer the default when unclear.", in.Models},
		"skill":        {"choice", "Which active skill in `skills` best fits `prompt`? Choose agent_decides if none clearly fits.", in.Skills},
		"needs_review": {"noul", "Does `prompt` explicitly request confirmation or review before taking action?", nil},
	}
}

// Observe makes one bounded request. It never changes models, skills or permissions.
// The endpoint and client arguments allow isolated HTTP tests without API calls.
func Observe(ctx context.Context, client *http.Client, endpoint, key, model string, in Input) (Observation, error) {
	if key == "" {
		return Observation{}, errors.New("TypeSafe API key is not configured")
	}
	if model == "" {
		model = "jev-latest"
	}
	if len(in.Models) == 0 || in.Models[in.DefaultModel] == "" || in.Skills["agent_decides"] == "" {
		return Observation{}, errors.New("invalid routing candidates")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	qs := questions(in)
	body, err := json.Marshal(struct {
		Model     string              `json:"model"`
		State     Input               `json:"state"`
		Questions map[string]question `json:"questions"`
	}{model, in, qs})
	if err != nil {
		return Observation{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Observation{}, errors.New("invalid TypeSafe endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return Observation{}, errors.New("TypeSafe request failed or timed out")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Observation{}, fmt.Errorf("TypeSafe returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return Observation{}, errors.New("invalid TypeSafe response size")
	}
	var out Observation
	if json.Unmarshal(data, &out) != nil {
		return Observation{}, errors.New("invalid TypeSafe JSON response")
	}
	for id, q := range qs {
		a, ok := out.Answers[id]
		if !ok || a.Type != q.Type {
			return Observation{}, fmt.Errorf("missing or invalid TypeSafe answer: %s", id)
		}
		switch q.Type {
		case "choice":
			if q.Criteria.(map[string]string)[a.Choice] == "" || !unit(a.Confidence) {
				return Observation{}, fmt.Errorf("invalid TypeSafe choice: %s", id)
			}
			if !distribution(a.Probabilities, q.Criteria.(map[string]string)) {
				return Observation{}, fmt.Errorf("invalid TypeSafe probabilities: %s", id)
			}
		case "score":
			if a.Score == nil || math.IsNaN(*a.Score) || *a.Score < 0 || *a.Score > 2 || !unit(a.Confidence) {
				return Observation{}, errors.New("invalid TypeSafe complexity")
			}
			if !distribution(a.Probabilities, map[string]string{"0": "", "1": "", "2": ""}) {
				return Observation{}, errors.New("invalid TypeSafe complexity probabilities")
			}
		case "noul":
			if !unit(a.Noul) {
				return Observation{}, errors.New("invalid TypeSafe review probability")
			}
		}
	}
	out.SuggestedModel = out.Answers["model_tier"].Choice
	out.SuggestedSkill = out.Answers["skill"].Choice
	out.Fallback = false
	for _, id := range []string{"intent", "complexity", "model_tier", "skill"} {
		if *out.Answers[id].Confidence < 0.8 {
			out.Fallback = true
		}
	}
	if out.Fallback {
		out.SuggestedModel = in.DefaultModel
		out.SuggestedSkill = "agent_decides"
	}
	return out, nil
}

func unit(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && *v >= 0 && *v <= 1
}

func distribution(raw json.RawMessage, candidates map[string]string) bool {
	var probabilities map[string]*float64
	if json.Unmarshal(raw, &probabilities) != nil || len(probabilities) != len(candidates) {
		return false
	}
	var sum float64
	for key := range candidates {
		p := probabilities[key]
		if !unit(p) {
			return false
		}
		sum += *p
	}
	return math.Abs(sum-1) < 0.01
}

// Summary is a compact display string; raw answers remain available for logs.
func (o Observation) Summary() string {
	return fmt.Sprintf("Jev observe: %s (%.2f), complexity %.2f (%.2f), model %s (%.2f), skill %s (%.2f), review %.2f; recommendation=%s/%s, fallback=%t",
		o.Answers["intent"].Choice, *o.Answers["intent"].Confidence,
		*o.Answers["complexity"].Score, *o.Answers["complexity"].Confidence,
		o.Answers["model_tier"].Choice, *o.Answers["model_tier"].Confidence,
		o.Answers["skill"].Choice, *o.Answers["skill"].Confidence, *o.Answers["needs_review"].Noul, o.SuggestedModel, o.SuggestedSkill, o.Fallback)
}
