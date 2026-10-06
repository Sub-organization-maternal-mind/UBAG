package voicehub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
	"github.com/ubag/ubag/apps/gateway/internal/voice"
)

// ---- a relay that behaves like deploy/vps/browser/audio-relay.py --------------

// fakeRelay speaks relay protocol v2 on loopback. Like the real relay with
// UBAG_VOICE_RELAY_KEY_FILE it re-reads its key file on EVERY hello, verifies the
// BOUND token (node and lease generation signed), serves one session at a time,
// records what it saw and echoes audio back.
type fakeRelay struct {
	t        *testing.T
	listener net.Listener
	keyFile  string

	mu         sync.Mutex
	hellos     []map[string]any
	refused    []string
	frames     int
	controls   []map[string]any
	active     bool
	readyDelay time.Duration
	closedConn int
}

func startFakeRelay(t *testing.T, keyFile string) *fakeRelay {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRelay{t: t, listener: l, keyFile: keyFile}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go r.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return r
}

func (r *fakeRelay) addr() string { return r.listener.Addr().String() }

func writeFrame(conn net.Conn, kind byte, payload []byte) error {
	out := make([]byte, 5+len(payload))
	binary.LittleEndian.PutUint32(out, uint32(1+len(payload)))
	out[4] = kind
	copy(out[5:], payload)
	_, err := conn.Write(out)
	return err
}

func readFrame(conn net.Conn) (byte, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n < 1 || n > 64<<10 {
		return 0, nil, errors.New("bad frame")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return 0, nil, err
	}
	return buf[0], buf[1:], nil
}

func (r *fakeRelay) refuse(conn net.Conn, reason string) {
	r.mu.Lock()
	r.refused = append(r.refused, reason)
	r.mu.Unlock()
	_ = writeFrame(conn, 2, []byte(`{"op":"error","reason":"`+reason+`"}`))
}

func (r *fakeRelay) serve(conn net.Conn) {
	defer conn.Close()
	kind, payload, err := readFrame(conn)
	if err != nil || kind != 2 {
		return
	}
	var hello map[string]any
	if json.Unmarshal(payload, &hello) != nil || hello["op"] != "hello" {
		r.refuse(conn, "bad_hello")
		return
	}
	key, err := os.ReadFile(r.keyFile)
	key = bytes.TrimSpace(key)
	if err != nil || len(key) == 0 {
		r.refuse(conn, "unconfigured")
		return
	}
	sid, _ := hello["session_id"].(string)
	exp, _ := hello["exp"].(float64)
	node, _ := hello["node_id"].(string)
	gen, _ := hello["generation"].(float64)
	want := voice.RelayTokenBound(key, sid, int64(exp), node, uint64(gen))
	if tok, _ := hello["token"].(string); node == "" || gen == 0 || !hmac.Equal([]byte(tok), []byte(want)) {
		r.refuse(conn, "unauthorized")
		return
	}
	r.mu.Lock()
	if r.active {
		r.mu.Unlock()
		r.refuse(conn, "busy")
		return
	}
	r.active = true
	r.hellos = append(r.hellos, hello)
	delay := r.readyDelay
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active = false
		r.closedConn++
		r.mu.Unlock()
	}()
	time.Sleep(delay)
	if writeFrame(conn, 2, []byte(`{"op":"ready"}`)) != nil {
		return
	}
	for {
		kind, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		switch kind {
		case 1:
			r.mu.Lock()
			r.frames++
			r.mu.Unlock()
			if writeFrame(conn, 1, payload) != nil {
				return
			}
		case 2:
			var msg map[string]any
			if json.Unmarshal(payload, &msg) == nil {
				r.mu.Lock()
				r.controls = append(r.controls, msg)
				r.mu.Unlock()
			}
		}
	}
}

func (r *fakeRelay) snapshot() (hellos, frames, controls, closed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hellos), r.frames, len(r.controls), r.closedConn
}

func (r *fakeRelay) muteControls() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []bool
	for _, c := range r.controls {
		if c["op"] == "mute" {
			m, _ := c["muted"].(bool)
			out = append(out, m)
		}
	}
	return out
}

// ---- rig -----------------------------------------------------------------------

const (
	tSession = "voice_1700000000_aabbccdd"
	tAttempt = "att_voicehub1"
)

var (
	relayKey = []byte(strings.Repeat("a1", 32))
	mediaKey = []byte(strings.Repeat("b2", 32))
)

type recordingSink struct {
	mu        sync.Mutex
	connected chan struct{}
	ended     []string
	mutes     []bool
}

func newSink() *recordingSink { return &recordingSink{connected: make(chan struct{}, 4)} }

func (s *recordingSink) sink() helper.VoiceSink {
	return helper.VoiceSink{
		PeerConnected: func() { s.connected <- struct{}{} },
		MediaEnded:    func(reason string) { s.mu.Lock(); s.ended = append(s.ended, reason); s.mu.Unlock() },
		ClientMuted:   func(m bool) { s.mu.Lock(); s.mutes = append(s.mutes, m); s.mu.Unlock() },
	}
}

func (s *recordingSink) endedReasons() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ended...)
}

func (s *recordingSink) muteEvents() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.mutes...)
}

type rig struct {
	t     *testing.T
	dir   string
	env   helper.VoiceEnvironment
	relay *fakeRelay
	hub   *Hub
	call  helper.VoiceCall
}

func newRig(t *testing.T, mut ...func(*Config)) *rig {
	t.Helper()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "relay-key.9099")
	relay := startFakeRelay(t, keyFile)
	env := helper.VoiceEnvironment{ID: "env-a", CDPEndpoint: "http://127.0.0.1:9222", RelayAddr: relay.addr(), RelayKeyFile: keyFile}
	cfg := Config{Environments: []helper.VoiceEnvironment{env}, IncludeLoopback: true}
	for _, m := range mut {
		m(&cfg)
	}
	hub, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.CloseAll)
	return &rig{t: t, dir: dir, env: env, relay: relay, hub: hub, call: helper.VoiceCall{
		SessionID: tSession, TenantID: "tenant-a", AttemptID: tAttempt, NodeID: "helper-1", Generation: 7,
		Expires: time.Now().Add(10 * time.Minute), Target: "chatgpt_web", IdentityRef: "ident-1", InstanceRef: "env-a", Env: env,
		Credentials: helper.VoiceCredentials{RelayKey: relayKey, MediaKey: mediaKey},
	}}
}

func clientPC(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

// clientOffer builds a client SDP offer with one Opus mic track and a "control"
// data channel; it returns the peer, its mic and the control channel.
func clientOffer(t *testing.T) (*webrtc.PeerConnection, *webrtc.TrackLocalStaticSample, *webrtc.DataChannel, string) {
	t.Helper()
	pc := clientPC(t)
	mic, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "client-mic", "client")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pc.AddTrack(mic); err != nil {
		t.Fatal(err)
	}
	dc, err := pc.CreateDataChannel("control", nil)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := make(chan struct{})
	pc.OnICEGatheringStateChange(func(s webrtc.ICEGatheringState) {
		if s == webrtc.ICEGatheringStateComplete {
			close(gathered)
		}
	})
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(20 * time.Second):
		t.Fatal("client ICE gathering did not complete")
	}
	return pc, mic, dc, pc.LocalDescription().SDP
}

func accept(t *testing.T, pc *webrtc.PeerConnection, answer string) {
	t.Helper()
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatalf("answer rejected by the client: %v", err)
	}
}

func sendSamples(mic *webrtc.TrackLocalStaticSample, n int) {
	for i := 0; i < n; i++ {
		_ = mic.WriteSample(media.Sample{Data: []byte{byte(i), 0xAA, 0xBB}, Duration: 20 * time.Millisecond})
		time.Sleep(10 * time.Millisecond)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	eventuallyWithin(t, what, 10*time.Second, cond)
}

// eventuallyWithin is for a condition whose settling time is bounded by
// something outside the test's control. The clearest case is ICE noticing that
// the peer vanished: Pion detects that with its own failure/consent timers, not
// with anything the test drives, and on the Linux CI runner that takes
// noticeably longer than on a Windows host - the same code passed at 8697f1b
// and then timed out at 10s on two consecutive runs. Such a wait needs a
// budget above this file's other timeouts (15s and 20s are already used here),
// or it fails intermittently with nothing actually wrong. The condition itself
// is unchanged: this widens when we give up, never what is asserted.
func eventuallyWithin(t *testing.T, what string, budget time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fileContent(path string) (string, bool) {
	b, err := os.ReadFile(path)
	return string(b), err == nil
}

// ---- tests ---------------------------------------------------------------------

func TestVoiceHubNegotiatesOverALoopbackRelayWithAPerCallKey(t *testing.T) {
	r := newRig(t)
	sink := newSink()
	pc, mic, _, offer := clientOffer(t)
	answer, err := r.hub.Open(t.Context(), r.call, offer, sink.sink())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	accept(t, pc, answer)

	// The relay was given the per-call key through the key file and verified the
	// BOUND hello (node and lease generation signed) against it.
	if got, ok := fileContent(r.env.RelayKeyFile); !ok || got != string(relayKey) {
		t.Fatalf("relay key file = %q, %v", got, ok)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(r.env.RelayKeyFile); fi.Mode().Perm() != 0o600 {
			t.Fatalf("relay key file mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	hellos, _, _, _ := r.relay.snapshot()
	if hellos != 1 || r.relay.hellos[0]["node_id"] != "helper-1" || r.relay.hellos[0]["generation"] != float64(7) ||
		r.relay.hellos[0]["session_id"] != tSession {
		t.Fatalf("relay hellos = %v", r.relay.hellos)
	}

	// Audio flows client -> relay, and the peer connection is reported up.
	go sendSamples(mic, 100)
	eventually(t, "mic frames at the relay", func() bool { _, f, _, _ := r.relay.snapshot(); return f >= 3 })
	select {
	case <-sink.connected:
	case <-time.After(10 * time.Second):
		t.Fatal("PeerConnected was never reported")
	}
	if r.hub.Active() != 1 {
		t.Fatalf("active = %d", r.hub.Active())
	}

	// Close stops the media at once and removes the key.
	r.hub.Close(r.call.Key())
	if _, ok := fileContent(r.env.RelayKeyFile); ok {
		t.Fatal("the relay key file outlived the call")
	}
	eventually(t, "the relay connection to close", func() bool { _, _, _, c := r.relay.snapshot(); return c == 1 })
	if r.hub.Active() != 0 {
		t.Fatalf("active after Close = %d", r.hub.Active())
	}
	r.hub.Close(r.call.Key()) // idempotent
	if got := sink.endedReasons(); len(got) != 0 {
		t.Fatalf("an explicit Close must not report MediaEnded: %v", got)
	}
}

// A relay that holds some other attempt's key (it reads a different file than the
// one the hub wrote) refuses the bound hello, and nothing is left behind.
func TestVoiceHubRelayWithAnotherKeyRefusesTheCallAndNothingIsLeftBehind(t *testing.T) {
	r := newRig(t)
	r.relay.keyFile = filepath.Join(r.dir, "elsewhere")
	if err := os.WriteFile(r.relay.keyFile, []byte(strings.Repeat("d4", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, offer := clientOffer(t)
	_, err := r.hub.Open(t.Context(), r.call, offer, newSink().sink())
	if !errors.Is(err, helper.ErrVoiceRelayUnavailable) {
		t.Fatalf("a relay that refuses the hello = %v, want ErrVoiceRelayUnavailable", err)
	}
	r.relay.mu.Lock()
	refused := append([]string(nil), r.relay.refused...)
	r.relay.mu.Unlock()
	if len(refused) == 0 || refused[0] != "unauthorized" {
		t.Fatalf("relay refusals = %v", refused)
	}
	if r.hub.Active() != 0 {
		t.Fatal("a refused call left media behind")
	}
	if _, ok := fileContent(r.env.RelayKeyFile); ok {
		t.Fatal("a refused call left its key file behind")
	}
}

func TestVoiceHubRelayDownIsReportedAsUnavailable(t *testing.T) {
	r := newRig(t)
	_ = r.relay.listener.Close()
	_, _, _, offer := clientOffer(t)
	_, err := r.hub.Open(t.Context(), r.call, offer, newSink().sink())
	if !errors.Is(err, helper.ErrVoiceRelayUnavailable) {
		t.Fatalf("relay down = %v, want ErrVoiceRelayUnavailable", err)
	}
	if r.hub.Active() != 0 {
		t.Fatal("state left behind")
	}
	if _, ok := fileContent(r.env.RelayKeyFile); ok {
		t.Fatal("the key file outlived a failed call")
	}
}

func TestVoiceHubReofferReplacesTheClientLegAndKeepsTheRelayKey(t *testing.T) {
	r := newRig(t)
	pc, mic, _, offer := clientOffer(t)
	answer, err := r.hub.Open(t.Context(), r.call, offer, newSink().sink())
	if err != nil {
		t.Fatal(err)
	}
	accept(t, pc, answer)
	go sendSamples(mic, 30)
	eventually(t, "first leg frames", func() bool { _, f, _, _ := r.relay.snapshot(); return f >= 2 })

	pc2, mic2, _, offer2 := clientOffer(t)
	answer2, err := r.hub.Reoffer(t.Context(), r.call.Key(), offer2, true) // muted: the mute carries over
	if err != nil {
		t.Fatalf("Reoffer: %v", err)
	}
	accept(t, pc2, answer2)
	eventually(t, "the relay to be re-dialled", func() bool { h, _, _, _ := r.relay.snapshot(); return h == 2 })
	if got, ok := fileContent(r.env.RelayKeyFile); !ok || got != string(relayKey) {
		t.Fatal("the relay key must stay in place for the re-dial")
	}
	eventually(t, "mute control after the re-dial", func() bool { return len(r.relay.muteControls()) > 0 && r.relay.muteControls()[0] })
	// Muted audio never reaches the provider.
	_, before, _, _ := r.relay.snapshot()
	go sendSamples(mic2, 30)
	time.Sleep(600 * time.Millisecond)
	if _, after, _, _ := r.relay.snapshot(); after > before+1 { // one in-flight frame of the old leg
		t.Fatalf("muted audio leaked to the relay: %d frames", after-before)
	}
	if r.hub.Active() != 1 {
		t.Fatalf("active = %d", r.hub.Active())
	}
	if _, err := r.hub.Reoffer(t.Context(), "att_unknown#1", offer2, false); err == nil {
		t.Fatal("a Reoffer for an unknown call must fail")
	}
}

func TestVoiceHubSetMutedStopsAndResumesAudioAtTheRelay(t *testing.T) {
	r := newRig(t)
	pc, mic, _, offer := clientOffer(t)
	answer, err := r.hub.Open(t.Context(), r.call, offer, newSink().sink())
	if err != nil {
		t.Fatal(err)
	}
	accept(t, pc, answer)
	go sendSamples(mic, 300)
	eventually(t, "frames", func() bool { _, f, _, _ := r.relay.snapshot(); return f >= 3 })
	if !r.hub.SetMuted(r.call.Key(), true) {
		t.Fatal("SetMuted found no live media")
	}
	time.Sleep(200 * time.Millisecond)
	_, a, _, _ := r.relay.snapshot()
	time.Sleep(400 * time.Millisecond)
	if _, b, _, _ := r.relay.snapshot(); b != a {
		t.Fatalf("muted audio leaked: %d frames", b-a)
	}
	r.hub.SetMuted(r.call.Key(), false)
	eventually(t, "frames after unmute", func() bool { _, f, _, _ := r.relay.snapshot(); return f > a })
	if r.hub.SetMuted("att_unknown#1", true) {
		t.Fatal("SetMuted on an unknown call must report false")
	}
}

// The client's control data channel is authorised ONLY by a credential minted
// under this attempt's media key: the app secret, the relay key and another
// attempt's key all fail, and the client's mute reaches the service only after
// authentication.
func TestVoiceHubControlChannelVerifiesWithThePerAttemptMediaKeyOnly(t *testing.T) {
	r := newRig(t)
	sink := newSink()
	pc, _, dc, offer := clientOffer(t)
	answer, err := r.hub.Open(t.Context(), r.call, offer, sink.sink())
	if err != nil {
		t.Fatal(err)
	}
	accept(t, pc, answer)
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })
	select {
	case <-opened:
	case <-time.After(15 * time.Second):
		t.Fatal("control data channel never opened")
	}
	session := voice.Session{ID: tSession, TenantID: "tenant-a", AppID: "app-1"}
	cred := func(key []byte) string {
		return voice.IssueMediaCredential(key, session, time.Now().Add(5*time.Minute))
	}
	send := func(op map[string]any) {
		b, _ := json.Marshal(op)
		_ = dc.SendText(string(b))
	}

	send(map[string]any{"op": "mute", "muted": true})
	time.Sleep(300 * time.Millisecond)
	if len(sink.muteEvents()) != 0 {
		t.Fatal("an unauthenticated mute was honoured")
	}
	for name, key := range map[string][]byte{"the app secret": []byte("dev-secret"), "the relay key": relayKey, "another attempt's key": []byte(strings.Repeat("e5", 32))} {
		send(map[string]any{"op": "auth", "credential": cred(key)})
		send(map[string]any{"op": "mute", "muted": true})
		time.Sleep(300 * time.Millisecond)
		if len(sink.muteEvents()) != 0 {
			t.Fatalf("a credential minted under %s was accepted", name)
		}
	}
	send(map[string]any{"op": "auth", "credential": cred(mediaKey)})
	send(map[string]any{"op": "mute", "muted": true})
	eventually(t, "the authorised mute", func() bool { m := sink.muteEvents(); return len(m) == 1 && m[0] })
}

func TestVoiceHubEndedOnItsOwnIsReportedToTheService(t *testing.T) {
	r := newRig(t)
	sink := newSink()
	pc, _, _, offer := clientOffer(t)
	answer, err := r.hub.Open(t.Context(), r.call, offer, sink.sink())
	if err != nil {
		t.Fatal(err)
	}
	accept(t, pc, answer)
	select {
	case <-sink.connected:
	case <-time.After(10 * time.Second):
		t.Fatal("never connected")
	}
	// The media path ends on its own (here: the client vanishes): the hub reports
	// it and the service then ends the call.
	pc.Close() // the client goes away
	eventuallyWithin(t, "MediaEnded", 20*time.Second, func() bool { return len(sink.endedReasons()) > 0 })
	if reason := sink.endedReasons()[0]; reason == "" || strings.Contains(reason, " ") {
		t.Fatalf("reason %q must be a short token", reason)
	}
}

// Close that races an Open in flight (the dead-man or a terminate arriving while
// the relay is still answering) leaves no media, no key and no entry.
func TestVoiceHubCloseDuringOpenLeavesNothing(t *testing.T) {
	r := newRig(t)
	r.relay.mu.Lock()
	r.relay.readyDelay = 400 * time.Millisecond
	r.relay.mu.Unlock()
	_, _, _, offer := clientOffer(t)
	res := make(chan error, 1)
	go func() {
		_, err := r.hub.Open(t.Context(), r.call, offer, newSink().sink())
		res <- err
	}()
	eventually(t, "the relay to have received the hello", func() bool { h, _, _, _ := r.relay.snapshot(); return h == 1 })
	r.hub.Close(r.call.Key())
	if err := <-res; err == nil {
		t.Fatal("an Open that was closed meanwhile must not succeed")
	}
	if r.hub.Active() != 0 {
		t.Fatal("an entry was left behind")
	}
	if _, ok := fileContent(r.env.RelayKeyFile); ok {
		t.Fatal("the key file was left behind")
	}
	eventually(t, "the relay slot to be released", func() bool { _, _, _, c := r.relay.snapshot(); return c == 1 })
}

// A late Close of an older generation must not take the successor's key file or
// media with it: calls are keyed by attempt and generation, not by session.
func TestVoiceHubLateCloseOfAnOlderGenerationLeavesTheSuccessorAlone(t *testing.T) {
	r := newRig(t)
	pc, _, _, offer := clientOffer(t)
	answer, err := r.hub.Open(t.Context(), r.call, offer, newSink().sink())
	if err != nil {
		t.Fatal(err)
	}
	accept(t, pc, answer)
	older := r.call
	r.hub.Close(older.Key())

	next := r.call
	next.Generation, next.AttemptID = 8, "att_voicehub2"
	next.Credentials.RelayKey = []byte(strings.Repeat("f6", 32))
	pc2, _, _, offer2 := clientOffer(t)
	answer2, err := r.hub.Open(t.Context(), next, offer2, newSink().sink())
	if err != nil {
		t.Fatal(err)
	}
	accept(t, pc2, answer2)

	r.hub.Close(older.Key()) // late and repeated
	if got, ok := fileContent(r.env.RelayKeyFile); !ok || got != string(next.Credentials.RelayKey) {
		t.Fatal("the late Close removed the successor's key file")
	}
	if r.hub.Active() != 1 {
		t.Fatalf("the late Close ended the successor: active = %d", r.hub.Active())
	}
	// And a stale key file is only removed while it still holds the closing call's key.
	path := filepath.Join(r.dir, "k")
	if err := os.WriteFile(path, []byte("B"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeKeyFile(path, []byte("A"))
	if _, ok := fileContent(path); !ok {
		t.Fatal("removeKeyFile removed a key that is not the caller's")
	}
	removeKeyFile(path, []byte("B"))
	if _, ok := fileContent(path); ok {
		t.Fatal("removeKeyFile kept the caller's own key")
	}
}

var candidateRe = regexp.MustCompile(`(?m)^a=candidate:\S+ \d+ udp \d+ (\S+) (\d+) typ (\w+)`)

// Media binds only inside the configured UDP range and advertises the NAT 1:1
// address, so the fleet manager opens exactly that range and clients are given
// the node's public address (no ICE servers: nothing to reach in a unit test).
func TestVoiceHubAdvertisesTheNAT1To1AddressInsideTheBoundedUDPRange(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.UDPMin, c.UDPMax, c.NAT1To1IP = 43100, 43163, "203.0.113.7"
	})
	_, _, _, offer := clientOffer(t)
	answer, err := r.hub.Open(t.Context(), r.call, offer, newSink().sink())
	if err != nil {
		t.Fatal(err)
	}
	cands := candidateRe.FindAllStringSubmatch(answer, -1)
	if len(cands) == 0 {
		t.Fatalf("the answer carries no candidates:\n%s", answer)
	}
	for _, c := range cands {
		port, _ := strconv.Atoi(c[2])
		if port < 43100 || port > 43163 {
			t.Errorf("candidate %s:%s is outside the bounded UDP range", c[1], c[2])
		}
		if c[3] == "host" && c[1] != "203.0.113.7" {
			t.Errorf("host candidate %s is not the NAT 1:1 address", c[1])
		}
	}
}

func TestVoiceHubConfigIsValidatedAndStaleKeysAreCleared(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "relay-key.9099")
	if err := os.WriteFile(key, []byte("left-by-a-crashed-run"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := helper.VoiceEnvironment{ID: "e", RelayKeyFile: key}
	if _, err := New(Config{Environments: []helper.VoiceEnvironment{env}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := fileContent(key); ok {
		t.Fatal("a previous run's relay key must not survive a restart: the relay would accept a stale attempt")
	}
	if _, err := New(Config{Environments: []helper.VoiceEnvironment{{ID: "e", RelayKeyFile: filepath.Join(dir, "missing", "k")}}}); err == nil {
		t.Fatal("an unwritable key directory must refuse to start")
	}
	if _, err := New(Config{Environments: []helper.VoiceEnvironment{{ID: "e"}}}); err == nil {
		t.Fatal("an environment without a key file must be refused")
	}
}

func TestVoiceHubRefusesABindingTheKeysCannotBeDerivedFrom(t *testing.T) {
	r := newRig(t)
	bad := r.call
	bad.NodeID = "a|b"
	_, _, _, offer := clientOffer(t)
	if _, err := r.hub.Open(context.Background(), bad, offer, newSink().sink()); err == nil {
		t.Fatal("a binding with the separator in a part must be refused")
	}
	if r.hub.Active() != 0 {
		t.Fatal("state left behind")
	}
	if _, err := r.hub.Open(context.Background(), r.call, offer, newSink().sink()); err != nil {
		t.Fatalf("a valid binding: %v", err)
	}
	if _, err := r.hub.Open(context.Background(), r.call, offer, newSink().sink()); err == nil {
		t.Fatal("a second Open of the same call must be refused (use Reoffer)")
	}
}
