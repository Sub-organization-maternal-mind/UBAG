package ubag

import (
	"context"
	"encoding/base64"
	"net/http"
)

// Multimodal chat helpers for the OpenAI-compatible facade
// (POST /v1/openai/chat/completions). Content parts are inline only: images
// are data URLs (png/jpeg/webp/gif), audio is base64 wav/mp3. Remote media
// URLs are rejected by the gateway. Per-target support is published by
// GET /v1/capabilities (inline_message_parts).

// TextPart builds a {type:"text"} content part.
func TextPart(text string) JSON {
	return JSON{"type": "text", "text": text}
}

// ImagePart builds an image_url part carrying data as a base64 data URL.
func ImagePart(mimeType string, data []byte) JSON {
	return ImagePartDataURL("data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data))
}

// ImagePartDataURL builds an image_url part from an existing data URL.
func ImagePartDataURL(dataURL string) JSON {
	return JSON{"type": "image_url", "image_url": JSON{"url": dataURL}}
}

// AudioPart builds an input_audio part; format is "wav" or "mp3".
func AudioPart(data []byte, format string) JSON {
	return JSON{"type": "input_audio", "input_audio": JSON{
		"data":   base64.StdEncoding.EncodeToString(data),
		"format": format,
	}}
}

// ChatMessage is one request message; Content is a string or a []JSON of parts.
type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ChatCompletionRequest is the facade request subset the SDK models.
type ChatCompletionRequest struct {
	Model      string        `json:"model"`
	Messages   []ChatMessage `json:"messages"`
	UbagWaitMs *int64        `json:"ubag_wait_ms,omitempty"`
}

// ChatCompletionResponse is the facade chat.completion answer.
type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	UbagJobID string `json:"ubag_job_id,omitempty"`
}

// Text returns the first choice's message content.
func (response *ChatCompletionResponse) Text() string {
	if len(response.Choices) == 0 {
		return ""
	}
	return response.Choices[0].Message.Content
}

// CreateChatCompletion posts a (possibly multimodal) chat request to the
// OpenAI-compatible facade. Facade errors are OpenAI-shaped, so a failure is
// an *APIError with StatusCode and RawBody but no UBAG envelope.
func (client *Client) CreateChatCompletion(ctx context.Context, request ChatCompletionRequest, options ...RequestOption) (*ChatCompletionResponse, error) {
	var out ChatCompletionResponse
	if err := client.requestInto(ctx, http.MethodPost, "/v1/openai/chat/completions", request, client.resolveOptions(options...), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
