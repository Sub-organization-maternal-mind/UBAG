// Capability discovery: what each target accepts and whether live voice can
// start now.   go run ./capabilities
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ubag/ubag-examples/internal/env"
)

func main() {
	caps, err := env.Client().ListCapabilities(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	var ready []string
	for _, target := range caps.Data {
		v := target.Voice
		fmt.Printf("%s\n  images: %s   audio: %s\n", target.Target,
			strings.Join(target.InlineMessageParts.ImageURL, ", "), strings.Join(target.InlineMessageParts.InputAudio, ", "))
		// Supported = adapter declares live voice; Configured = this gateway can serve it;
		// Verified = a live acceptance run is recorded; Available = a free account exists now.
		fmt.Printf("  voice: supported=%t configured=%t verified=%t available=%t free=%d utterance_jobs=%t\n",
			v.Supported, v.Configured, v.Verified, v.Available, v.FreeResources, v.UtteranceJobs)
		if v.Available {
			ready = append(ready, target.Target)
		}
	}
	if len(ready) == 0 {
		fmt.Println("No target can start live voice right now.")
		return
	}
	fmt.Println("Live voice can start now on: " + strings.Join(ready, ", "))
}
