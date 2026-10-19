package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultOpenAIModel is used with the openai provider when no model is set.
const DefaultOpenAIModel = "gpt-5"

// OpenAI calls an OpenAI-compatible chat completions API: OpenAI itself,
// OpenRouter, a LiteLLM gateway, or local models (Ollama, LM Studio).
// These cache long repeated prompts on their own, where they cache at all.
type OpenAI struct {
	BaseURL string // ends in /v1
	Key     string // optional for local servers
	// KeyCmd prints a (short-lived) key, e.g. an SSO token for a gateway;
	// it's run when needed and again when the API says 401.
	KeyCmd string
	// Headers go on every request (e.g. a gateway's virtual key).
	Headers map[string]string
	// CacheHints marks the runbook context with Anthropic's cache_control,
	// for gateways that pass it on to Claude.
	CacheHints bool
	Model      string
	// Label names the provider (default "openai").
	Label string
	HTTP  *http.Client

	mu    sync.Mutex
	keyed string
	keyAt time.Time
}

func (o *OpenAI) Name() string {
	if o.Label != "" {
		return o.Label
	}
	return "openai"
}
func (o *OpenAI) ModelName() string { return o.Model }

// key is the API key: fixed, or from KeyCmd (reused for ten minutes,
// unless renew).
func (o *OpenAI) key(ctx context.Context, renew bool) (string, error) {
	if o.KeyCmd == "" {
		return o.Key, nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !renew && o.keyed != "" && time.Since(o.keyAt) < 10*time.Minute {
		return o.keyed, nil
	}
	out, err := exec.CommandContext(ctx, "sh", "-c", o.KeyCmd).Output()
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("getting a key (%s): %s", o.KeyCmd, msg)
	}
	o.keyed, o.keyAt = strings.TrimSpace(string(out)), time.Now()
	return o.keyed, nil
}

func (o *OpenAI) Stream(ctx context.Context, system []Block, history []Message, question string, maxTokens int, onText func(string)) (Usage, error) {
	u, err := o.stream(ctx, system, history, question, maxTokens, onText, false)
	if errors.Is(err, errUnauthorized) && o.KeyCmd != "" {
		u, err = o.stream(ctx, system, history, question, maxTokens, onText, true) // a fresh token
	}
	return u, err
}

var errUnauthorized = errors.New("unauthorized")

func (o *OpenAI) stream(ctx context.Context, system []Block, history []Message, question string, maxTokens int, onText func(string), renew bool) (Usage, error) {
	var usage Usage
	var sysMsg any
	if o.CacheHints {
		// Content parts, the runbook marked for caching (passed on to Claude).
		var parts []map[string]any
		for _, b := range system {
			p := map[string]any{"type": "text", "text": b.Text}
			if b.Cache {
				p["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			parts = append(parts, p)
		}
		sysMsg = parts
	} else {
		var sys []string
		for _, b := range system {
			sys = append(sys, b.Text)
		}
		sysMsg = strings.Join(sys, "\n\n")
	}
	msgs := []any{map[string]any{"role": "system", "content": sysMsg}}
	for _, m := range history {
		msgs = append(msgs, m)
	}
	msgs = append(msgs, Message{Role: "user", Content: question})
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
	key, err := o.key(ctx, renew)
	if err != nil {
		return usage, err
	}
	if key != "" {
		req.Header.Set("authorization", "Bearer "+key)
	}
	for k, v := range o.Headers {
		req.Header.Set(k, v)
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
	if res.StatusCode == http.StatusUnauthorized && !renew {
		return usage, errUnauthorized
	}
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return usage, fmt.Errorf("%s %s: %s (%d)", o.Name(), o.Model, e.Error.Message, res.StatusCode)
		}
		return usage, fmt.Errorf("%s %s: %s", o.Name(), o.Model, res.Status)
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
