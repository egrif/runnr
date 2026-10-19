// Package claude asks a model (Claude through Anthropic or Amazon Bedrock,
// or any OpenAI-compatible endpoint) about runbooks. Long context that's
// sent again and again (a whole runbook) is marked for prompt caching
// where the provider supports it, and answers can be kept locally.
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
	"strings"
)

// DefaultModel answers quickly and cheaply; set RUNNR_CLAUDE_MODEL (e.g.
// claude-opus-5-5) for deeper reviews.
const DefaultModel = "claude-sonnet-5-5"

// Client calls Anthropic's Messages API.
type Client struct {
	Key     string
	Model   string
	BaseURL string // default https://api.anthropic.com
	// CacheTTL is how long the prompt cache keeps the runbook: "" (5
	// minutes) or "1h" (writes cost more, but reads last an hour).
	CacheTTL string
	HTTP     *http.Client
}

func (c *Client) Name() string      { return "anthropic" }
func (c *Client) ModelName() string { return c.Model }

// Block is part of the system prompt. Cache marks the end of a prefix to
// cache: it and everything before it are reused by later requests that
// start the same way (for about five minutes after the last use).
type Block struct {
	Text  string
	Cache bool
}

// Usage is what a request used, in tokens. CacheRead is input read from
// the cache (about a tenth of the price); CacheWrite is input written to it.
type Usage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	// Local: the answer came from runnr's own store, at no cost.
	Local bool `json:"local,omitempty"`
}

// Message is one turn of a conversation.
type Message struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

// Stream asks a question (after the conversation so far, if any) and calls
// onText with the answer as it arrives.
func (c *Client) Stream(ctx context.Context, system []Block, history []Message, question string, maxTokens int, onText func(string)) (Usage, error) {
	var usage Usage
	type cacheControl struct {
		Type string `json:"type"`
		TTL  string `json:"ttl,omitempty"`
	}
	type textBlock struct {
		Type         string        `json:"type"`
		Text         string        `json:"text"`
		CacheControl *cacheControl `json:"cache_control,omitempty"`
	}
	var sys []textBlock
	for _, b := range system {
		tb := textBlock{Type: "text", Text: b.Text}
		if b.Cache {
			tb.CacheControl = &cacheControl{Type: "ephemeral", TTL: c.CacheTTL}
		}
		sys = append(sys, tb)
	}
	body, err := json.Marshal(map[string]any{
		"model":      c.Model,
		"max_tokens": maxTokens,
		"stream":     true,
		"system":     sys,
		"messages":   append(append([]Message{}, history...), Message{Role: "user", Content: question}),
	})
	if err != nil {
		return usage, err
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return usage, err
	}
	req.Header.Set("x-api-key", c.Key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	hc := c.HTTP
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
			return usage, fmt.Errorf("claude: %s (%d)", e.Error.Message, res.StatusCode)
		}
		return usage, fmt.Errorf("claude: %s", res.Status)
	}

	// Server-sent events: message_start (input usage), content_block_delta
	// (text), message_delta (output usage), error.
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Usage Usage `json:"usage"`
			} `json:"message"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Usage *Usage `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			usage = ev.Message.Usage
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" {
				onText(ev.Delta.Text)
			}
		case "message_delta":
			if ev.Usage != nil {
				usage.Output = ev.Usage.Output
			}
		case "error":
			msg := "stream error"
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			return usage, errors.New("claude: " + msg)
		}
	}
	return usage, sc.Err()
}
