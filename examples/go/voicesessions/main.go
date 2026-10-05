// Backend voice-session management: create, queue handling, heartbeat, mute,
// cleanup. Media (WebRTC) is the client's job: a browser
// (examples/javascript/voice-browser.html) or a Go WebRTC stack produces the
// SDP offer and calls ConnectVoiceSession through YOUR backend.
//
//	go run ./voicesessions [target]
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	ubag "github.com/ubag/ubag-go"

	"github.com/ubag/ubag-examples/internal/env"
)

func main() {
	target := env.Arg(1, "chatgpt_web")
	client := env.Client()
	ctx := context.Background()

	created, err := client.CreateVoiceSession(ctx, ubag.VoiceCreateRequest{Target: target}) // Mode defaults to live
	var apiErr *ubag.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
		// Per-tenant session or queue budget reached: honour Retry-After.
		ms, _ := apiErr.RetryAfterMS()
		log.Fatalf("voice budget reached (%s); retry in %d ms", apiErr.Code(), ms)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(created.Status, "session", created.SessionID) // connecting (201) or queued (202)

	// Always release the provider account and media path.
	defer func() {
		if _, err := client.TerminateVoiceSession(ctx, created.SessionID); err != nil {
			log.Println("terminate:", err)
		}
	}()

	// Status values: queued | connecting | connected | terminated. "connected" only once the
	// provider voice UI is verified ready AND the client media is up; failures end the session
	// with LastError such as "activation_failed: <state>".
	current, err := client.GetVoiceSession(ctx, created.SessionID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("status:", current.Session.Status, current.Session.LastError)

	// During a call renew every ~60s; a lapsed lease frees the provider account.
	if _, err := client.RenewVoiceSessionLease(ctx, created.SessionID); err != nil {
		log.Fatal(err)
	}
	if _, err := client.MuteVoiceSession(ctx, created.SessionID, true); err != nil { // mic off; provider can still speak
		log.Fatal(err)
	}
	list, err := client.ListVoiceSessions(ctx, ubag.VoiceListParams{Target: target, Limit: 10})
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range list.Data {
		fmt.Printf("  %s %s\n", s.SessionID, s.Status)
	}
}
