// Utterance mode: audio in, text out, no live voice leases.
//
//	go run ./utterance [target] [clip.wav]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	ubag "github.com/ubag/ubag-go"

	"github.com/ubag/ubag-examples/internal/env"
)

func main() {
	target := env.Arg(1, "chatgpt_web")
	client := env.Client()
	ctx := context.Background()

	created, err := client.CreateVoiceSession(ctx, ubag.VoiceCreateRequest{Target: target, Mode: ubag.VoiceModeUtterance})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("session", created.SessionID, "job", created.JobID)

	// The backing job is held until the audio arrives under the declared key.
	audio := env.BytesOrFallback(env.Arg(2, ""), env.SilentWAV)
	if _, err := client.PutJobArtifact(ctx, created.JobID, "utterance.wav", audio, "audio/wav"); err != nil {
		log.Fatal(err)
	}

	for {
		time.Sleep(2 * time.Second)
		job, err := client.GetJob(ctx, created.JobID)
		if err != nil {
			log.Fatal(err)
		}
		status, _ := job["status"].(string)
		fmt.Println("job status:", status)
		if ubag.UbagJobStatuses[status].Terminal {
			result, _ := json.MarshalIndent(job["result"], "", "  ")
			fmt.Println(string(result))
			break
		}
	}
	if _, err := client.TerminateVoiceSession(ctx, created.SessionID); err != nil {
		log.Fatal(err)
	}
}
