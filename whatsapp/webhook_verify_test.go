package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testAppSecret = "test-app-secret"

// signFor produces the header Meta would send for body.
func signFor(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

func TestValidateSignature_valid(t *testing.T) {
	body := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	if err := ValidateSignature(signFor(body, testAppSecret), body, testAppSecret); err != nil {
		t.Errorf("expected a valid signature to pass, got: %v", err)
	}
}

func TestValidateSignature_rejections(t *testing.T) {
	body := []byte(`{"object":"whatsapp_business_account"}`)
	valid := signFor(body, testAppSecret)

	for _, tt := range []struct {
		name   string
		header string
		body   []byte
		secret string
		want   error
	}{
		{"missing header", "", body, testAppSecret, ErrMissingSignature},
		{"no sha256= prefix", hex.EncodeToString([]byte("whatever")), body, testAppSecret, ErrMalformedSignature},
		{"wrong prefix", "sha1=abcdef", body, testAppSecret, ErrMalformedSignature},
		{"digest not hex", signaturePrefix + "nothexatall!!", body, testAppSecret, ErrMalformedSignature},
		{"wrong secret", valid, body, "attacker-secret", ErrInvalidSignature},
		{"tampered body", valid, []byte(`{"object":"tampered"}`), testAppSecret, ErrInvalidSignature},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSignature(tt.header, tt.body, tt.secret)
			if !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
}

// TestValidateSignature_bodyMustBeExactBytes pins the trap that a re-marshalled
// payload will not verify. Meta signs the bytes on the wire, so the handler must
// validate the raw body before decoding it.
func TestValidateSignature_bodyMustBeExactBytes(t *testing.T) {
	original := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	header := signFor(original, testAppSecret)

	// Semantically identical JSON, different bytes — as any re-marshal would produce.
	reMarshalled := []byte(`{"entry":[],"object":"whatsapp_business_account"}`)

	if err := ValidateSignature(header, reMarshalled, testAppSecret); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("re-serialised JSON must NOT verify; got %v", err)
	}
}

// TestValidateSignature_emptyBody pins that an empty body is still verifiable
// rather than crashing or silently passing.
func TestValidateSignature_emptyBody(t *testing.T) {
	var body []byte
	if err := ValidateSignature(signFor(body, testAppSecret), body, testAppSecret); err != nil {
		t.Errorf("an empty body should verify against its own signature, got: %v", err)
	}
	if err := ValidateSignature(signFor([]byte("x"), testAppSecret), body, testAppSecret); !errors.Is(err, ErrInvalidSignature) {
		t.Error("an empty body must not accept another payload's signature")
	}
}

func TestVerificationRequest_Verify(t *testing.T) {
	const token = "meatyhamhock"

	for _, tt := range []struct {
		name    string
		req     VerificationRequest
		want    string
		wantErr error
	}{
		{
			name: "valid handshake echoes the challenge",
			req:  VerificationRequest{Mode: "subscribe", Challenge: "1158201444", VerifyToken: token},
			want: "1158201444",
		},
		{
			name:    "wrong token",
			req:     VerificationRequest{Mode: "subscribe", Challenge: "1158201444", VerifyToken: "guess"},
			wantErr: ErrVerifyTokenMismatch,
		},
		{
			name:    "wrong mode",
			req:     VerificationRequest{Mode: "unsubscribe", Challenge: "1158201444", VerifyToken: token},
			wantErr: ErrUnexpectedHubMode,
		},
		{
			name:    "empty mode",
			req:     VerificationRequest{Challenge: "1158201444", VerifyToken: token},
			wantErr: ErrUnexpectedHubMode,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.req.Verify(token)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("got err %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("challenge = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseVerificationRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet,
		"/wa/hook?hub.mode=subscribe&hub.challenge=1158201444&hub.verify_token=meatyhamhock", nil)
	got := ParseVerificationRequest(r)
	want := VerificationRequest{Mode: "subscribe", Challenge: "1158201444", VerifyToken: "meatyhamhock"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestHandleVerification_success pins the full handshake: 200 with the challenge
// as the bare body, which is what Meta's dashboard checks for.
func TestHandleVerification_success(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet,
		"/wa/hook?hub.mode=subscribe&hub.challenge=1158201444&hub.verify_token=meatyhamhock", nil)
	w := httptest.NewRecorder()

	if err := HandleVerification(w, r, "meatyhamhock"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); body != "1158201444" {
		t.Errorf("body = %q, want the bare challenge", body)
	}
}

// TestHandleVerification_wrongTokenIsForbiddenAndSilent pins that a failed
// handshake returns 403 and leaks nothing — an attacker probing for the verify
// token must learn nothing from the response.
func TestHandleVerification_wrongTokenIsForbiddenAndSilent(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet,
		"/wa/hook?hub.mode=subscribe&hub.challenge=1158201444&hub.verify_token=wrong", nil)
	w := httptest.NewRecorder()

	err := HandleVerification(w, r, "meatyhamhock")
	if !errors.Is(err, ErrVerifyTokenMismatch) {
		t.Errorf("got err %v, want ErrVerifyTokenMismatch", err)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if body := w.Body.String(); body != "" {
		t.Errorf("a rejected handshake must not echo the challenge or explain itself, got %q", body)
	}
}
