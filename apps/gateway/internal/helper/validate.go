package helper

import (
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

func invalid(msg string) error { return status.Error(codes.InvalidArgument, msg) }

// isAntigravity reports a target the helper must never run: antigravity_* holds
// primary-local credentials, OAuth sockets and stores (ADR-0002 Safe Mode), so
// it is not helper-eligible whatever the primary asks.
func isAntigravity(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "antigravity_")
}

// validateRun checks every field of a RunAttempt request that the runner will
// act on, and returns the spec the runner gets. Everything here is untrusted
// input as far as the helper is concerned: bounded, shaped, never logged.
func validateRun(req *helperv1.RunAttemptRequest) (AttemptSpec, time.Duration, error) {
	f := req.GetFence()
	switch {
	case !nameRe.MatchString(req.GetProvider()), !nameRe.MatchString(req.GetTarget()), !nameRe.MatchString(req.GetCommandType()):
		return AttemptSpec{}, 0, invalid("provider, target and command_type must be short identifiers")
	case isAntigravity(req.GetTarget()) || isAntigravity(req.GetProvider()):
		return AttemptSpec{}, 0, invalid("target is not eligible for helper execution")
	case !idTokenRe.MatchString(req.GetIdentityRef()):
		return AttemptSpec{}, 0, invalid("identity_ref must be an opaque token")
	case len(req.GetTraceId()) > 128:
		return AttemptSpec{}, 0, invalid("trace_id is too long")
	case !jsonObject(req.GetInputJson(), maxInputBytes):
		return AttemptSpec{}, 0, invalid("input_json must be a JSON object within the size limit")
	case req.GetOptionsJson() != "" && !jsonObject(req.GetOptionsJson(), maxOptionsBytes):
		return AttemptSpec{}, 0, invalid("options_json must be a JSON object within the size limit")
	case len(req.GetAssets()) > maxAssets:
		return AttemptSpec{}, 0, invalid("too many assets")
	}
	for _, a := range req.GetAssets() {
		if !sha256Re.MatchString(a.GetSha256()) || a.GetSizeBytes() < 0 || a.GetSizeBytes() > maxAssetBytes ||
			len(a.GetName()) > 255 || len(a.GetMediaType()) > 128 {
			return AttemptSpec{}, 0, invalid("asset needs a lowercase sha256, a size within the limit and short name and media_type")
		}
	}
	var deadline time.Duration
	switch d := req.GetDeadlineSeconds(); {
	case d < 0 || time.Duration(d)*time.Second > MaxDeadline:
		return AttemptSpec{}, 0, invalid("deadline_seconds is out of range")
	case d == 0:
		deadline = DefaultDeadline
	default:
		deadline = time.Duration(d) * time.Second
	}
	return AttemptSpec{
		JobID: f.GetJobId(), AttemptID: f.GetAttemptId(), Generation: f.GetLeaseGeneration(),
		Provider: req.GetProvider(), Target: req.GetTarget(), CommandType: req.GetCommandType(),
		IdentityRef: req.GetIdentityRef(), InputJSON: req.GetInputJson(), OptionsJSON: req.GetOptionsJson(),
		Assets: req.GetAssets(), TraceID: req.GetTraceId(), Deadline: deadline,
	}, deadline, nil
}

// jsonObject reports whether s is a syntactically valid JSON object within max bytes.
func jsonObject(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && strings.HasPrefix(strings.TrimLeft(s, " \t\r\n"), "{") && json.Valid([]byte(s))
}
