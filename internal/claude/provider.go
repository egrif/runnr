package claude

import (
	"context"
	"os"
	"strings"
)

// Provider answers questions, streaming the text as it comes.
type Provider interface {
	Name() string      // anthropic | bedrock | openai
	ModelName() string // what's asked
	Stream(ctx context.Context, system []Block, history []Message, question string, maxTokens int, onText func(string)) (Usage, error)
}

// Cache keeps answers locally, by a fingerprint of everything they depend on.
type Cache interface {
	GetAnswer(key string) (string, bool)
	PutAnswer(key, model, answer string) error
}

// FromEnv picks a provider from the environment (nil: none set up):
//
//	RUNNR_AI_PROVIDER  anthropic | bedrock | openai (default: anthropic with
//	                   ANTHROPIC_API_KEY, else openai with OPENAI_API_KEY or
//	                   RUNNR_AI_BASE_URL)
//	RUNNR_AI_MODEL     the model (RUNNR_CLAUDE_MODEL also works)
//	anthropic: ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, RUNNR_AI_CACHE_TTL (1h)
//	bedrock:   RUNNR_AI_AWS_PROFILE or AWS_PROFILE, AWS_REGION; the model is
//	           a Bedrock model or inference profile id
//	openai:    RUNNR_AI_BASE_URL (e.g. http://localhost:11434/v1 for
//	           Ollama), RUNNR_AI_API_KEY or OPENAI_API_KEY
func FromEnv() Provider {
	model := firstEnv("RUNNR_AI_MODEL", "RUNNR_CLAUDE_MODEL")
	prov := strings.ToLower(os.Getenv("RUNNR_AI_PROVIDER"))
	if prov == "" {
		switch {
		case os.Getenv("ANTHROPIC_API_KEY") != "":
			prov = "anthropic"
		case os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("RUNNR_AI_BASE_URL") != "":
			prov = "openai"
		default:
			return nil
		}
	}
	switch prov {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil
		}
		return &Client{Key: key, Model: or(model, DefaultModel), BaseURL: os.Getenv("ANTHROPIC_BASE_URL"), CacheTTL: os.Getenv("RUNNR_AI_CACHE_TTL")}
	case "bedrock":
		return &Bedrock{
			Profile: firstEnv("RUNNR_AI_AWS_PROFILE", "AWS_PROFILE"),
			Region:  or(firstEnv("AWS_REGION", "AWS_DEFAULT_REGION"), "us-east-1"),
			Model:   or(model, DefaultBedrockModel),
		}
	case "openai":
		return &OpenAI{
			BaseURL: or(os.Getenv("RUNNR_AI_BASE_URL"), "https://api.openai.com/v1"),
			Key:     firstEnv("RUNNR_AI_API_KEY", "OPENAI_API_KEY"),
			Model:   or(model, DefaultOpenAIModel),
		}
	}
	return nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
