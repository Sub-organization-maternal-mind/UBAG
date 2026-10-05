// Multimodal chat: text + image + audio in one OpenAI-compatible request.
//
//	go run ./multimodal [target] [image.png] [clip.wav]
//
// Without file arguments a 1x1 PNG and half a second of silence are used.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	ubag "github.com/ubag/ubag-go"

	"github.com/ubag/ubag-examples/internal/env"
)

func main() {
	target := env.Arg(1, "chatgpt_web")
	client := env.Client()
	ctx := context.Background()

	// The gateway rejects parts a target does not accept; check first.
	caps, err := client.ListCapabilities(ctx)
	if err != nil {
		log.Fatal(err)
	}
	capability, ok := caps.Find(target)
	if !ok {
		log.Fatalf("unknown target %s", target)
	}
	fmt.Println("accepted images:", capability.InlineMessageParts.ImageURL, "audio:", capability.InlineMessageParts.InputAudio)

	reply, err := client.CreateChatCompletion(ctx, ubag.ChatCompletionRequest{
		Model: target,
		Messages: []ubag.ChatMessage{{
			Role: "user",
			Content: []ubag.JSON{
				ubag.TextPart("Describe the image, then transcribe the audio clip."),
				ubag.ImagePart("image/png", env.BytesOrFallback(env.Arg(2, ""), env.TinyPNG)),
				ubag.AudioPart(env.BytesOrFallback(env.Arg(3, ""), env.SilentWAV), "wav"),
			},
		}},
	})
	var apiErr *ubag.APIError
	if errors.As(err, &apiErr) {
		// Facade errors are OpenAI-shaped; read RawBody. 429/503 carry Retry-After.
		ms, _ := apiErr.RetryAfterMS()
		log.Fatalf("HTTP %d: %s (retry after %d ms)", apiErr.StatusCode, apiErr.RawBody, ms)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(reply.Text())
}
