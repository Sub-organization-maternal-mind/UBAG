//go:build helpervoice

package main

import (
	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/helper/voicehub"
)

// newVoiceMedia builds the node's media endpoint: voice.MediaHub on the loopback
// relays of the configured audio environments, inside the configured UDP range.
// It is compiled in only with -tags helpervoice (see the package comment).
func newVoiceMedia(st helper.Settings) (helper.VoiceMedia, func(), error) {
	hub, err := voicehub.New(voicehub.Config{
		Environments: st.Voice.Environments, UDPMin: st.Voice.UDPMin, UDPMax: st.Voice.UDPMax,
		NAT1To1IP: st.Voice.NAT1To1IP, ServerViaTURN: st.Voice.ServerViaTURN,
	})
	if err != nil {
		return nil, nil, err
	}
	return hub, hub.CloseAll, nil
}
