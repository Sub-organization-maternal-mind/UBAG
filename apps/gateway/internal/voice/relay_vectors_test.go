package voice

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Relay protocol v2 shared vectors: the SAME fixture is asserted by
// deploy/vps/browser/tests/test_relay_vectors.py against the Python relay.

type relayVectors struct {
	Secret    string `json:"secret"`
	Constants struct {
		FrameHeaderBytes int `json:"frame_header_bytes"`
		MaxFrameBytes    int `json:"max_frame_bytes"`
		TypeAudio        int `json:"type_audio"`
		TypeControl      int `json:"type_control"`
		HelloTTLS        int `json:"hello_ttl_s"`
		ReadyTimeoutS    int `json:"ready_timeout_s"`
		DeviceReadyWaitS int `json:"device_ready_wait_s"`
	} `json:"constants"`
	Framing []struct {
		ID         string `json:"id"`
		Length     uint32 `json:"length"`
		Type       int    `json:"type"`
		PayloadHex string `json:"payload_hex"`
		PayloadLen int    `json:"payload_len"`
		Outcome    string `json:"outcome"`
	} `json:"framing"`
	TokenVectors []struct {
		Secret    string `json:"secret"`
		SessionID string `json:"session_id"`
		Exp       int64  `json:"exp"`
		Token     string `json:"token"`
	} `json:"token_vectors"`
	ReplyReasons struct {
		Handshake []string `json:"handshake"`
		Session   []string `json:"session"`
		Busy      string   `json:"gateway_busy_reason"`
	} `json:"reply_reasons"`
}

func loadRelayVectors(t *testing.T) relayVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "conformance", "fixtures", "voice-relay", "v2.json"))
	if err != nil {
		t.Fatalf("read relay vectors: %v", err)
	}
	var v relayVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse relay vectors: %v", err)
	}
	return v
}

func TestRelayVectorsConstants(t *testing.T) {
	c := loadRelayVectors(t).Constants
	if c.FrameHeaderBytes != relayFrameHeaderBytes || c.MaxFrameBytes != maxRelayFrameBytes ||
		c.TypeAudio != int(relayTypeAudio) || c.TypeControl != int(relayTypeControl) {
		t.Fatalf("framing constants drifted from the fixture: %+v", c)
	}
	if time.Duration(c.HelloTTLS)*time.Second != relayHelloTTL || time.Duration(c.ReadyTimeoutS)*time.Second != relayReadyTimeout {
		t.Fatalf("hello ttl / ready timeout drifted from the fixture: %+v", c)
	}
	if c.ReadyTimeoutS <= c.DeviceReadyWaitS {
		t.Fatalf("gateway ready timeout (%ds) must exceed the relay device wait (%ds)", c.ReadyTimeoutS, c.DeviceReadyWaitS)
	}
}

func TestRelayVectorsToken(t *testing.T) {
	for _, tv := range loadRelayVectors(t).TokenVectors {
		if got := RelayToken([]byte(tv.Secret), tv.SessionID, tv.Exp); got != tv.Token {
			t.Errorf("RelayToken(%q,%q,%d) = %s, want %s", tv.Secret, tv.SessionID, tv.Exp, got, tv.Token)
		}
	}
}

func TestRelayVectorsFraming(t *testing.T) {
	for _, c := range loadRelayVectors(t).Framing {
		t.Run(c.ID, func(t *testing.T) {
			var payload []byte
			if c.PayloadHex != "" {
				payload, _ = hex.DecodeString(c.PayloadHex)
			} else {
				payload = make([]byte, c.PayloadLen)
			}
			wire := make([]byte, 4)
			binary.LittleEndian.PutUint32(wire, c.Length)
			if c.Length > 0 {
				wire = append(append(wire, byte(c.Type)), payload...)
			}
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			go func() { _, _ = server.Write(wire) }()
			_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
			kind, got, err := (&TCPFrameConn{conn: client}).readFrame()
			if c.Outcome == "violation" {
				// A timeout means the reader waited for a body it should have refused.
				if err == nil || !strings.Contains(err.Error(), "protocol violation") {
					t.Fatalf("want protocol violation, got kind=%d err=%v", kind, err)
				}
				return
			}
			if err != nil || int(kind) != c.Type || hex.EncodeToString(got) != hex.EncodeToString(payload) {
				t.Fatalf("frame = (%d, %d bytes, %v), want (%d, %d bytes)", kind, len(got), err, c.Type, len(payload))
			}
		})
	}
}

// fakeHandshake accepts one connection, hands the hello to check, then runs
// reply on the connection.
func fakeHandshake(t *testing.T, check func(hello map[string]any), reply func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		kind, payload, err := readRelayFrame(conn)
		var hello map[string]any
		if err != nil || kind != relayTypeControl || json.Unmarshal(payload, &hello) != nil {
			return
		}
		check(hello)
		reply(conn)
	}()
	return ln.Addr().String()
}

// The hello the gateway sends must be exactly the schema's hello shape with a
// token the shared vectors' algorithm reproduces.
func TestRelayVectorsDialHelloAndReplyReasons(t *testing.T) {
	v := loadRelayVectors(t)
	secret := []byte(v.Secret)
	for _, reason := range v.ReplyReasons.Handshake {
		t.Run("handshake_"+reason, func(t *testing.T) {
			helloOK := make(chan error, 1)
			addr := fakeHandshake(t, func(h map[string]any) {
				sid, _ := h["session_id"].(string)
				exp, _ := h["exp"].(float64)
				token, _ := h["token"].(string)
				switch {
				case len(h) != 4 || h["op"] != "hello" || sid != "s1":
					helloOK <- errors.New("hello keys/op/session_id wrong")
				case token != RelayToken(secret, sid, int64(exp)) || len(token) != 64:
					helloOK <- errors.New("hello token wrong")
				case int64(exp) < time.Now().Unix() || int64(exp) > time.Now().Add(relayHelloTTL+time.Second).Unix():
					helloOK <- errors.New("hello exp outside ttl")
				default:
					helloOK <- nil
				}
			}, func(c net.Conn) {
				_ = writeRelayFrame(c, relayTypeControl, []byte(`{"op":"error","reason":"`+reason+`"}`))
			})
			_, err := relayDialer(addr, secret).Dial(t.Context(), Session{ID: "s1", InstanceRef: "browser-1"})
			select {
			case herr := <-helloOK:
				if herr != nil {
					t.Fatal(herr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("relay never received a hello")
			}
			want := ErrRelayUnavailable
			if reason == v.ReplyReasons.Busy {
				want = ErrRelayBusy
			}
			if !errors.Is(err, want) {
				t.Fatalf("Dial err = %v, want %v", err, want)
			}
		})
	}
	for _, reason := range v.ReplyReasons.Session {
		t.Run("session_"+reason, func(t *testing.T) {
			addr := fakeHandshake(t, func(map[string]any) {}, func(c net.Conn) {
				_ = writeRelayFrame(c, relayTypeControl, []byte(`{"op":"ready"}`))
				// An empty audio frame (length 1) must be dropped, not an error.
				_, _ = c.Write([]byte{1, 0, 0, 0, byte(relayTypeAudio)})
				_ = writeRelayFrame(c, relayTypeControl, []byte(`{"op":"error","reason":"`+reason+`"}`))
				time.Sleep(200 * time.Millisecond)
			})
			conn, err := relayDialer(addr, secret).Dial(t.Context(), Session{ID: "s1", InstanceRef: "browser-1"})
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer conn.Close()
			if _, err := conn.Recv(t.Context()); err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("Recv err = %v, want it to carry %q", err, reason)
			}
		})
	}
}
