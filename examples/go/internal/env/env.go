// Package env holds the shared setup for the Go examples.
//
// Environment: UBAG_TOKEN (app secret / PAT, keep it server-side) and
// optionally UBAG_BASE_URL (default http://127.0.0.1:8080).
package env

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"

	ubag "github.com/ubag/ubag-go"
)

// Client builds a gateway client from the environment or exits.
func Client() *ubag.Client {
	token := os.Getenv("UBAG_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "Set UBAG_TOKEN (and optionally UBAG_BASE_URL).")
		os.Exit(2)
	}
	base := os.Getenv("UBAG_BASE_URL")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	client, err := ubag.NewClient(base, ubag.WithAppSecret(token))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return client
}

// Arg returns os.Args[i] or def.
func Arg(i int, def string) string {
	if len(os.Args) > i && os.Args[i] != "" {
		return os.Args[i]
	}
	return def
}

// BytesOrFallback reads path, or returns the generated placeholder when path is empty.
func BytesOrFallback(path string, fallback func() []byte) []byte {
	if path == "" {
		return fallback()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return data
}

// TinyPNG is a 1x1 transparent PNG.
func TinyPNG() []byte {
	data, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	return data
}

// SilentWAV is half a second of 16 kHz mono 16-bit silence.
func SilentWAV() []byte {
	const rate, samples = 16000, 8000
	buf := make([]byte, 44+samples*2)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+samples*2))
	copy(buf[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], 1)
	binary.LittleEndian.PutUint32(buf[24:], rate)
	binary.LittleEndian.PutUint32(buf[28:], rate*2)
	binary.LittleEndian.PutUint16(buf[32:], 2)
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], samples*2)
	return buf
}
