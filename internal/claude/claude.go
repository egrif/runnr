// Package claude asks Claude (Anthropic's Messages API) about runbooks,
// streaming the answer. Long context that's sent again and again (a whole
// runbook) is marked for prompt caching, so later questions about the same
// runbook read it from the cache at a fraction of the price.
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
	"os"
	"strings"
)

// DefaultModel answers quickly and cheaply; set RUNNR_CLAUDE_MODEL (e.g.
// claude-opus-5-5) for deeper reviews.
const DefaultModel = "claude-sonnet-5-5"

// Client calls the Messages API.
type Client struct {
	Key     string
	Model   string
	BaseURL string // default https://api.anthropic.com
	HTTP    *http.Client
}

// FromEnv is a client from ANTHROPIC_API_KEY (nil without one), with
// RUNNR_CLAUDE_MODEL and ANTHROPIC_BASE_URL if set.
func FromEnv() *Client {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil
	}
	c := &Client{Key: key, Model: os.Getenv("RUNNR_CLAUDE_MODEL"), BaseURL: os.Getenv("ANTHROPIC_BASE_URL")}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	return c
}

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
			tb.CacheControl = &cacheControl{Type: "ephemeral"}
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
