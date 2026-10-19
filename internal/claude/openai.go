package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultOpenAIModel is used with the openai provider when no model is set.
const DefaultOpenAIModel = "gpt-5"

// OpenAI calls an OpenAI-compatible chat completions API: OpenAI itself,
// OpenRouter, a LiteLLM gateway, or local models (Ollama, LM Studio).
// These cache long repeated prompts on their own, where they cache at all.
type OpenAI struct {
	BaseURL string // ends in /v1
	Key     string // optional for local servers
	Model   string
	HTTP    *http.Client
}

func (o *OpenAI) Name() string      { return "openai" }
func (o *OpenAI) ModelName() string { return o.Model }

func (o *OpenAI) Stream(ctx context.Context, system []Block, history []Message, question string, maxTokens int, onText func(string)) (Usage, error) {
	var usage Usage
	var sys []string
	for _, b := range system {
		sys = append(sys, b.Text)
	}
	msgs := []Message{{Role: "system", Content: strings.Join(sys, "\n\n")}}
	msgs = append(append(msgs, history...), Message{Role: "user", Content: question})
	body, err := json.Marshal(map[string]any{
		"model": o.Model, "messages": msgs, "stream": true, "max_completion_tokens": maxTokens,
		"stream_options": map[string]bool{"include_usage": true},
	})
	if err != nil {
		return usage, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return usage, err
	}
	req.Header.Set("content-type", "application/json")
	if o.Key != "" {
		req.Header.Set("authorization", "Bearer "+o.Key)
	}
	hc := o.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return usage, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return usage, fmt.Errorf("%s: %s (%d)", o.Model, e.Error.Message, res.StatusCode)
		}
		return usage, fmt.Errorf("%s: %s", o.Model, res.Status)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
				Details    struct {
					Cached int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		for _, c := range ch.Choices {
			if c.Delta.Content != "" {
				onText(c.Delta.Content)
			}
		}
		if u := ch.Usage; u != nil {
			usage = Usage{Input: u.Prompt - u.Details.Cached, CacheRead: u.Details.Cached, Output: u.Completion}
		}
	}
	return usage, sc.Err()
}
