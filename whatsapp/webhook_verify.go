package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// SignatureHeader carries the HMAC of the request body on event notifications.
//
// https://developers.facebook.com/docs/graph-api/webhooks/getting-started#validating-payloads
const SignatureHeader = "X-Hub-Signature-256"

// signaturePrefix is what Meta prepends to the hex digest in SignatureHeader.
const signaturePrefix = "sha256="

// Webhook verification errors.
var (
	// ErrMissingSignature means the request carried no SignatureHeader.
	ErrMissingSignature = errors.New("missing " + SignatureHeader + " header")

	// ErrMalformedSignature means the header was present but not "sha256=<hex>".
	ErrMalformedSignature = errors.New("malformed " + SignatureHeader + " header")

	// ErrInvalidSignature means the computed HMAC did not match. Treat the request
	// as forged.
	ErrInvalidSignature = errors.New("invalid payload signature")

	// ErrVerifyTokenMismatch means a subscription request presented the wrong token.
	ErrVerifyTokenMismatch = errors.New("hub.verify_token mismatch")

	// ErrUnexpectedHubMode means a subscription request was not hub.mode=subscribe.
	ErrUnexpectedHubMode = errors.New("unexpected hub.mode")
)

// ValidateSignature reports whether body was signed with appSecret.
//
// Meta signs every event notification with HMAC-SHA256 over the payload, keyed by
// the app secret, and sends it as "X-Hub-Signature-256: sha256=<hex>".
//
// body MUST be the exact bytes received. Re-marshalling parsed JSON will not
// reproduce Meta's byte sequence and the signature will not match, so read the raw
// body first and validate before decoding.
//
// https://developers.facebook.com/docs/graph-api/webhooks/getting-started#validating-payloads
func ValidateSignature(header string, body []byte, appSecret string) error {
	if header == "" {
		return ErrMissingSignature
	}
	if !strings.HasPrefix(header, signaturePrefix) {
		return fmt.Errorf("%w: expected %q prefix", ErrMalformedSignature, signaturePrefix)
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, signaturePrefix))
	if err != nil {
		return fmt.Errorf("%w: digest is not valid hex: %v", ErrMalformedSignature, err)
	}

	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	want := mac.Sum(nil)

	// hmac.Equal, not bytes.Equal: constant-time, so a timing side channel cannot
	// be used to forge a signature byte by byte.
	if !hmac.Equal(got, want) {
		return ErrInvalidSignature
	}
	return nil
}

// VerificationRequest is Meta's subscription handshake, sent as a GET when a
// webhook endpoint is configured.
//
// https://developers.facebook.com/docs/graph-api/webhooks/getting-started#verification-requests
type VerificationRequest struct {
	Mode        string
	Challenge   string
	VerifyToken string
}

// ParseVerificationRequest reads the hub.* query parameters from r.
func ParseVerificationRequest(r *http.Request) VerificationRequest {
	q := r.URL.Query()
	return VerificationRequest{
		Mode:        q.Get("hub.mode"),
		Challenge:   q.Get("hub.challenge"),
		VerifyToken: q.Get("hub.verify_token"),
	}
}

// Verify checks the handshake against the configured token and returns the
// challenge to echo back.
//
// Meta's instruction is exact: verify hub.verify_token matches the string
// configured in the App Dashboard, then respond with the hub.challenge value.
func (v VerificationRequest) Verify(verifyToken string) (challenge string, err error) {
	if v.Mode != "subscribe" {
		return "", fmt.Errorf("%w: %q", ErrUnexpectedHubMode, v.Mode)
	}
	// Constant-time: the verify token is a shared secret, so comparing it with ==
	// would leak it a byte at a time to anyone who can time the endpoint.
	if !hmac.Equal([]byte(v.VerifyToken), []byte(verifyToken)) {
		return "", ErrVerifyTokenMismatch
	}
	if v.Challenge == "" {
		return "", errors.New("hub.challenge is empty")
	}
	return v.Challenge, nil
}

// HandleVerification serves the subscription handshake, echoing hub.challenge on
// success and refusing with 403 otherwise.
//
// Returns the error, if any, so the caller can log it; the response is already
// written either way.
func HandleVerification(w http.ResponseWriter, r *http.Request, verifyToken string) error {
	challenge, err := ParseVerificationRequest(r).Verify(verifyToken)
	if err != nil {
		// 403, and deliberately no detail: an attacker probing for the verify token
		// learns nothing from the response.
		w.WriteHeader(http.StatusForbidden)
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write([]byte(challenge))
	return err
}
