package llm

type ChatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionResponse struct {
	Choices []ChatCompletionChoice `json:"choices"`
}

type ChatCompletionChoice struct {
	Message ChatCompletionMessage `json:"message"`
}

type GenerationParameters struct {
	Temperature      float32 `json:"temperature"`
	TopK             int32   `json:"top_k"`
	TopP             float32 `json:"top_p"`
	MaxOutputTokens  int     `json:"max_output_tokens"`
	ResponseMIMEType string  `json:"response_mime_type"`
}
