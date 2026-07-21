package providers

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

const (
	ProviderOpenAI      = "openai"
	ProviderOpenRouter  = "openrouter"
	ProviderXAI         = "xai"
	ProviderGemini      = "gemini"
	ProviderAnthropic   = "anthropic"
	ProviderDeepSeek    = "deepseek"
	ProviderMistral     = "mistral"
	ProviderGroq        = "groq"
	ProviderTogether    = "together"
	ProviderFireworks   = "fireworks"
	ProviderPerplexity  = "perplexity"
	ProviderCerebras    = "cerebras"
	ProviderZAI         = "zai"
	ProviderHuggingFace = "huggingface"
	ProviderLMStudio    = "lmstudio"
	ProviderOllama      = "ollama"
	ProviderMiniMax     = "minimax"
	ProviderCustom      = "custom"

	StyleResponses = "responses"
	StyleOpenAI    = "openai-compatible"
	StyleGemini    = "gemini"
	StyleAnthropic = "anthropic"
)

var ErrInvalidProvider = errors.New("model provider settings are invalid")

type ModelProvider struct {
	ID           string
	Label        string
	BaseURL      string
	DefaultModel string
	Style        string
	KeyOptional  bool
}

type ModelConfig struct {
	ProviderID string
	Model      string
	BaseURL    string
	APIKey     string
}

var modelProviders = []ModelProvider{
	{ProviderOpenAI, "OpenAI", "https://api.openai.com/v1", "gpt-5.4-mini", StyleResponses, false},
	{ProviderOpenRouter, "OpenRouter", "https://openrouter.ai/api/v1", "~openai/gpt-latest", StyleOpenAI, false},
	{ProviderXAI, "xAI", "https://api.x.ai/v1", "grok-4.5", StyleOpenAI, false},
	{ProviderGemini, "Google Gemini", "https://generativelanguage.googleapis.com", "gemini-2.5-flash", StyleGemini, false},
	{ProviderAnthropic, "Anthropic", "https://api.anthropic.com", "", StyleAnthropic, false},
	{ProviderDeepSeek, "DeepSeek", "https://api.deepseek.com", "deepseek-v4-pro", StyleOpenAI, false},
	{ProviderMistral, "Mistral", "https://api.mistral.ai/v1", "mistral-large-latest", StyleOpenAI, false},
	{ProviderGroq, "Groq", "https://api.groq.com/openai/v1", "", StyleOpenAI, false},
	{ProviderTogether, "Together AI", "https://api.together.ai/v1", "", StyleOpenAI, false},
	{ProviderFireworks, "Fireworks AI", "https://api.fireworks.ai/inference/v1", "", StyleOpenAI, false},
	{ProviderPerplexity, "Perplexity", "https://api.perplexity.ai", "sonar-pro", StyleOpenAI, false},
	{ProviderCerebras, "Cerebras", "https://api.cerebras.ai/v1", "", StyleOpenAI, false},
	{ProviderZAI, "Z.ai", "https://api.z.ai/api/paas/v4", "glm-4.5", StyleOpenAI, false},
	{ProviderHuggingFace, "Hugging Face", "https://router.huggingface.co/v1", "", StyleOpenAI, false},
	{ProviderLMStudio, "LM Studio", "http://127.0.0.1:1234/v1", "", StyleOpenAI, true},
	{ProviderOllama, "Ollama", "http://127.0.0.1:11434/v1", "", StyleOpenAI, true},
	{ProviderMiniMax, "MiniMax", "https://api.minimax.io/v1", "", StyleOpenAI, false},
	{ProviderCustom, "Custom", "", "", StyleOpenAI, true},
}

func ModelProviders() []ModelProvider { return append([]ModelProvider(nil), modelProviders...) }

func ModelProviderByID(id string) (ModelProvider, bool) {
	for _, provider := range modelProviders {
		if provider.ID == strings.ToLower(strings.TrimSpace(id)) {
			return provider, true
		}
	}
	return ModelProvider{}, false
}

// NormalizeModelConfig applies only known provider defaults. It never turns an
// unknown ID into Custom: settings must make that choice explicitly.
func NormalizeModelConfig(config ModelConfig) (ModelConfig, ModelProvider, error) {
	provider, ok := ModelProviderByID(config.ProviderID)
	if !ok {
		return ModelConfig{}, ModelProvider{}, ErrInvalidProvider
	}
	config.ProviderID = provider.ID
	config.Model = strings.TrimSpace(config.Model)
	if config.Model == "" {
		config.Model = provider.DefaultModel
	}
	if len(config.Model) == 0 || len(config.Model) > 256 || strings.ContainsAny(config.Model, "\x00\r\n") {
		return ModelConfig{}, ModelProvider{}, ErrInvalidProvider
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.BaseURL == "" {
		config.BaseURL = provider.BaseURL
	}
	if len(config.BaseURL) > 512 || !safeBaseURL(config.BaseURL, provider) || strings.TrimSpace(config.APIKey) != config.APIKey || len(config.APIKey) > 1024 || (config.APIKey == "" && !provider.KeyOptional) {
		return ModelConfig{}, ModelProvider{}, ErrInvalidProvider
	}
	return config, provider, nil
}

func safeBaseURL(raw string, provider ModelProvider) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	if parsed.Scheme == "http" {
		ip := net.ParseIP(parsed.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	if provider.ID == ProviderCustom {
		if ip := net.ParseIP(parsed.Hostname()); ip != nil && !isPublicProviderIP(ip) {
			return false
		}
	}
	if provider.ID != ProviderCustom && raw != provider.BaseURL {
		return false
	}
	return true
}

func isPublicProviderIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	_, shared, _ := net.ParseCIDR("100.64.0.0/10")
	return shared == nil || !shared.Contains(ip)
}
