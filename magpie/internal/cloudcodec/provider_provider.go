package cloudcodec

// Protocol is a wire API magpie can speak to an upstream.
type Protocol string

const (
	Chat      Protocol = "chat"      // OpenAI Chat Completions
	Responses Protocol = "responses" // OpenAI Responses
	Anthropic Protocol = "anthropic" // Anthropic Messages
	Gemini    Protocol = "gemini"    // Google Gemini's generateContent: served to clients; spoken upstream at a custom provider's Gemini URL, Vertex AI and Factory's generate route
)
