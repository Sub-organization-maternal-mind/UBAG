//go:build !helpervoice

package main

import (
	"errors"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
)

// newVoiceMedia is the default build's answer to UBAG_HELPER_VOICE=1: this binary
// does not link the media endpoint (pion and the voice stores), so it refuses to
// start rather than silently serve no voice.
func newVoiceMedia(helper.Settings) (helper.VoiceMedia, func(), error) {
	return nil, nil, errors.New(helper.EnvVoice + " is on but this binary was built without helper-hosted voice: build it with -tags helpervoice")
}
