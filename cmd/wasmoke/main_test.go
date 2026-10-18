package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw-whatsapp/whatsapp"
)

func TestMainFunc(t *testing.T) {
	oldExit := exit
	oldArgs := os.Args
	t.Cleanup(func() {
		exit = oldExit
		os.Args = oldArgs
	})

	var exitCode int
	exit = func(code int) {
		exitCode = code
	}
	os.Args = []string{"wasmoke", "help"}

	main()

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
}

func TestRun(t *testing.T) {
	t.Run("no_args", func(t *testing.T) {
		if code := run(nil); code != 2 {
			t.Fatalf("expected 2, got %d", code)
		}
	})

	t.Run("help_variants", func(t *testing.T) {
		for _, arg := range []string{"help", "-h", "--help"} {
			if code := run([]string{arg}); code != 0 {
				t.Fatalf("expected 0 for %q, got %d", arg, code)
			}
		}
	})

	t.Run("unknown_command", func(t *testing.T) {
		if code := run([]string{"bogus"}); code != 2 {
			t.Fatalf("expected 2, got %d", code)
		}
	})

	t.Run("serve_dispatch", func(t *testing.T) {
		// With no env vars, cmdServe returns 1
		if code := run([]string{"serve"}); code != 1 {
			t.Fatalf("expected 1, got %d", code)
		}
	})

	t.Run("send_dispatch", func(t *testing.T) {
		// With no env vars, cmdSend returns 1
		if code := run([]string{"send"}); code != 1 {
			t.Fatalf("expected 1, got %d", code)
		}
	})
}

func TestUsage(t *testing.T) {
	usage()
}

func TestRequireEnv(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv("TEST_VAR_A", "val")
		t.Setenv("TEST_VAR_B", "")
		vals, err := requireEnv("TEST_VAR_A", "TEST_VAR_B", "TEST_VAR_C")
		if err == nil {
			t.Fatal("expected error for missing vars")
		}
		if vals != nil {
			t.Fatal("expected nil map on error")
		}
		if !strings.Contains(err.Error(), "TEST_VAR_B") || !strings.Contains(err.Error(), "TEST_VAR_C") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("all_present", func(t *testing.T) {
		t.Setenv("TEST_VAR_1", "v1")
		t.Setenv("TEST_VAR_2", "v2")
		vals, err := requireEnv("TEST_VAR_1", "TEST_VAR_2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if vals["TEST_VAR_1"] != "v1" || vals["TEST_VAR_2"] != "v2" {
			t.Fatalf("unexpected vals: %v", vals)
		}
	})
}

func TestMemoryChatDataStore(t *testing.T) {
	store := newMemoryChatDataStore()
	ctx := context.Background()

	got, err := store.GetChatData(ctx, "bot", "chat1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil data, got %v", got)
	}

	data := &whatsapp.WaChatData{}
	if err := store.SaveChatData(ctx, "bot", "chat1", data); err != nil {
		t.Fatalf("SaveChatData failed: %v", err)
	}

	got, err = store.GetChatData(ctx, "bot", "chat1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != data {
		t.Fatalf("expected %v, got %v", data, got)
	}
}

func TestFlagSet(t *testing.T) {
	t.Run("parse_valid", func(t *testing.T) {
		var s1, s2 string
		var m multiFlag
		fs := newFlagSet([]string{"-s1", "v1", "-param", "p1", "-param", "p2", "-s2", "v2"})
		fs.str("s1", &s1)
		fs.str("s2", &s2)
		fs.multi("param", &m)

		if err := fs.parse(); err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		if s1 != "v1" || s2 != "v2" {
			t.Fatalf("unexpected strings: %q, %q", s1, s2)
		}
		if len(m) != 2 || m[0] != "p1" || m[1] != "p2" {
			t.Fatalf("unexpected multi: %v", m)
		}
	})

	t.Run("not_a_flag", func(t *testing.T) {
		fs := newFlagSet([]string{"bare_argument"})
		err := fs.parse()
		if err == nil || !strings.Contains(err.Error(), "expected a -flag") {
			t.Fatalf("expected -flag error, got %v", err)
		}
	})

	t.Run("missing_value", func(t *testing.T) {
		fs := newFlagSet([]string{"-flag"})
		err := fs.parse()
		if err == nil || !strings.Contains(err.Error(), "needs a value") {
			t.Fatalf("expected missing value error, got %v", err)
		}
	})

	t.Run("unknown_flag", func(t *testing.T) {
		fs := newFlagSet([]string{"-unknown", "val"})
		err := fs.parse()
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("expected unknown flag error, got %v", err)
		}
	})
}

func TestReportSendError(t *testing.T) {
	t.Run("transport_error", func(t *testing.T) {
		if code := reportSendError(errors.New("connection reset")); code != 1 {
			t.Fatalf("expected 1, got %d", code)
		}
	})

	tests := []struct {
		name string
		err  error
	}{
		{"re-engagement", &wabotapi.APIError{Code: 131047, Message: "re-engagement required"}},
		{"rate-limited", &wabotapi.APIError{Code: 80007, Message: "rate limit exceeded"}},
		{"unreachable", &wabotapi.APIError{Code: 131026, Message: "undeliverable"}},
		{"auth", &wabotapi.APIError{Code: 190, Message: "invalid token"}},
		{"template", &wabotapi.APIError{Code: wabotapi.ErrCodeTemplateValidation, Message: "invalid template"}},
		{"generic", &wabotapi.APIError{Code: 500, Message: "internal error"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := reportSendError(tt.err); code != 1 {
				t.Fatalf("expected 1, got %d", code)
			}
		})
	}
}

func TestDefaultHooks(t *testing.T) {
	c := newClient("tok", "123")
	if c == nil {
		t.Fatal("expected non-nil client")
	}

	srv := &http.Server{Addr: "localhost:-1"}
	if err := listenAndServe(srv); err == nil {
		t.Fatal("expected error from invalid addr")
	}
}

type mockLastInbound struct {
	at  time.Time
	err error
}

func (m mockLastInbound) LastInboundAt(_ context.Context, _ string) (time.Time, error) {
	return m.at, m.err
}

func TestLogInbound(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	t.Run("text_message_window_open", func(t *testing.T) {
		m := whatsapp.InboundMessage{
			From: "12345",
			Type: "text",
			ID:   "wamid.1",
			Text: &whatsapp.InboundText{Body: "hello"},
		}
		logInbound(ctx, m, mockLastInbound{at: now})
	})

	t.Run("button_tap_payload_equals_text_window_closed", func(t *testing.T) {
		m := whatsapp.InboundMessage{
			From: "12345",
			Type: "button",
			ID:   "wamid.2",
			Button: &whatsapp.InboundButton{
				Payload: "Yes",
				Text:    "Yes",
			},
		}
		logInbound(ctx, m, mockLastInbound{at: now.Add(-48 * time.Hour)})
	})

	t.Run("button_tap_payload_different", func(t *testing.T) {
		m := whatsapp.InboundMessage{
			From: "12345",
			Type: "button",
			ID:   "wamid.3",
			Button: &whatsapp.InboundButton{
				Payload: "custom_payload",
				Text:    "Confirm",
			},
		}
		logInbound(ctx, m, mockLastInbound{at: now})
	})

	t.Run("interactive_reply", func(t *testing.T) {
		m := whatsapp.InboundMessage{
			From: "12345",
			Type: "interactive",
			ID:   "wamid.4",
			Interactive: &whatsapp.InboundInteractive{
				Type: "button_reply",
				ButtonReply: &whatsapp.InboundReply{
					ID:    "opt1",
					Title: "Option 1",
				},
			},
		}
		logInbound(ctx, m, mockLastInbound{at: now})
	})

	t.Run("last_inbound_error", func(t *testing.T) {
		m := whatsapp.InboundMessage{
			From: "12345",
			Type: "unknown",
			ID:   "wamid.5",
		}
		logInbound(ctx, m, mockLastInbound{err: errors.New("chat not found")})
	})
}

func TestCmdServe(t *testing.T) {
	oldServe := listenAndServe
	t.Cleanup(func() {
		listenAndServe = oldServe
	})

	t.Run("missing_env", func(t *testing.T) {
		t.Setenv("WA_APP_SECRET", "")
		t.Setenv("WA_VERIFY_TOKEN", "")
		if code := cmdServe(nil); code != 1 {
			t.Fatalf("expected 1, got %d", code)
		}
	})

	t.Run("server_listen_error_with_default_port", func(t *testing.T) {
		t.Setenv("WA_APP_SECRET", "test_secret")
		t.Setenv("WA_VERIFY_TOKEN", "test_token")
		t.Setenv("WA_PORT", "")

		listenAndServe = func(srv *http.Server) error {
			if srv.Addr != ":8080" {
				t.Fatalf("expected Addr :8080, got %s", srv.Addr)
			}
			return errors.New("port in use")
		}

		if code := cmdServe(nil); code != 1 {
			t.Fatalf("expected 1, got %d", code)
		}
	})

	t.Run("server_success_with_custom_port_and_handler_callbacks", func(t *testing.T) {
		secret := "my_app_secret"
		verifyToken := "my_verify_token"
		t.Setenv("WA_APP_SECRET", secret)
		t.Setenv("WA_VERIFY_TOKEN", verifyToken)
		t.Setenv("WA_PORT", "9999")

		listenAndServe = func(srv *http.Server) error {
			if srv.Addr != ":9999" {
				t.Fatalf("expected Addr :9999, got %s", srv.Addr)
			}

			// Exercise webhook handler through the server mux
			payload := `{
				"object": "whatsapp_business_account",
				"entry": [{
					"id": "waba123",
					"changes": [{
						"value": {
							"messaging_product": "whatsapp",
							"metadata": {
								"display_phone_number": "123",
								"phone_number_id": "wasmoke"
							},
							"contacts": [{"wa_id": "12345", "profile": {"name": "Alice"}}],
							"messages": [{
								"from": "12345",
								"id": "wamid.msg1",
								"timestamp": "1700000000",
								"type": "text",
								"text": {"body": "hello"}
							}],
							"statuses": [{
								"id": "wamid.out1",
								"recipient_id": "12345",
								"status": "delivered",
								"timestamp": "1700000001",
								"pricing": {"billable": true}
							}]
						},
						"field": "messages"
					}]
				}]
			}`

			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(payload))
			sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

			req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(payload))
			req.Header.Set("X-Hub-Signature-256", sig)
			rec := httptest.NewRecorder()

			srv.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 OK from webhook, got %d: %s", rec.Code, rec.Body.String())
			}

			return nil
		}

		if code := cmdServe(nil); code != 0 {
			t.Fatalf("expected 0, got %d", code)
		}
	})
}

func TestCmdSend(t *testing.T) {
	oldNewClient := newClient
	t.Cleanup(func() {
		newClient = oldNewClient
	})

	t.Run("missing_env", func(t *testing.T) {
		t.Setenv("WA_PHONE_NUMBER_ID", "")
		t.Setenv("WA_ACCESS_TOKEN", "")
		if code := cmdSend(nil); code != 1 {
			t.Fatalf("expected 1, got %d", code)
		}
	})

	t.Run("flag_parse_error", func(t *testing.T) {
		t.Setenv("WA_PHONE_NUMBER_ID", "phone1")
		t.Setenv("WA_ACCESS_TOKEN", "token1")
		if code := cmdSend([]string{"-badflag"}); code != 2 {
			t.Fatalf("expected 2, got %d", code)
		}
	})

	t.Run("missing_to", func(t *testing.T) {
		t.Setenv("WA_PHONE_NUMBER_ID", "phone1")
		t.Setenv("WA_ACCESS_TOKEN", "token1")
		if code := cmdSend([]string{"-text", "hello"}); code != 2 {
			t.Fatalf("expected 2, got %d", code)
		}
	})

	t.Run("both_or_neither_text_template", func(t *testing.T) {
		t.Setenv("WA_PHONE_NUMBER_ID", "phone1")
		t.Setenv("WA_ACCESS_TOKEN", "token1")
		// Neither
		if code := cmdSend([]string{"-to", "123"}); code != 2 {
			t.Fatalf("expected 2, got %d", code)
		}
		// Both
		if code := cmdSend([]string{"-to", "123", "-text", "hi", "-template", "t1"}); code != 2 {
			t.Fatalf("expected 2, got %d", code)
		}
	})

	t.Run("send_text_success", func(t *testing.T) {
		t.Setenv("WA_PHONE_NUMBER_ID", "phone1")
		t.Setenv("WA_ACCESS_TOKEN", "token1")

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messaging_product": "whatsapp",
				"messages": []map[string]string{
					{"id": "wamid.success"},
				},
			})
		}))
		defer ts.Close()

		newClient = func(accessToken, phoneNumberID string) *wabotapi.Client {
			c := wabotapi.NewClient(accessToken, phoneNumberID)
			c.BaseURL = ts.URL
			return c
		}

		if code := cmdSend([]string{"-to", "16505551234", "-text", "Hello World"}); code != 0 {
			t.Fatalf("expected 0, got %d", code)
		}
	})

	t.Run("send_template_success_with_graph_version_and_params", func(t *testing.T) {
		t.Setenv("WA_PHONE_NUMBER_ID", "phone1")
		t.Setenv("WA_ACCESS_TOKEN", "token1")
		t.Setenv("WA_GRAPH_VERSION", "v21.0")

		var interceptedReqBody bytes.Buffer
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = interceptedReqBody.ReadFrom(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messaging_product": "whatsapp",
				"messages": []map[string]string{
					{"id": "wamid.template_success"},
				},
			})
		}))
		defer ts.Close()

		newClient = func(accessToken, phoneNumberID string) *wabotapi.Client {
			c := wabotapi.NewClient(accessToken, phoneNumberID)
			c.BaseURL = ts.URL
			return c
		}

		args := []string{
			"-to", "16505551234",
			"-template", "order_update",
			"-lang", "en_GB",
			"-param", "param1",
			"-param", "param2",
		}
		if code := cmdSend(args); code != 0 {
			t.Fatalf("expected 0, got %d", code)
		}
	})

	t.Run("send_template_default_lang_success", func(t *testing.T) {
		t.Setenv("WA_PHONE_NUMBER_ID", "phone1")
		t.Setenv("WA_ACCESS_TOKEN", "token1")

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messaging_product": "whatsapp",
				"messages": []map[string]string{
					{"id": "wamid.template_default_lang"},
				},
			})
		}))
		defer ts.Close()

		newClient = func(accessToken, phoneNumberID string) *wabotapi.Client {
			c := wabotapi.NewClient(accessToken, phoneNumberID)
			c.BaseURL = ts.URL
			return c
		}

		args := []string{
			"-to", "16505551234",
			"-template", "welcome",
		}
		if code := cmdSend(args); code != 0 {
			t.Fatalf("expected 0, got %d", code)
		}
	})

	t.Run("send_api_error", func(t *testing.T) {
		t.Setenv("WA_PHONE_NUMBER_ID", "phone1")
		t.Setenv("WA_ACCESS_TOKEN", "token1")

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    131047,
					"message": "Re-engagement message",
				},
			})
		}))
		defer ts.Close()

		newClient = func(accessToken, phoneNumberID string) *wabotapi.Client {
			c := wabotapi.NewClient(accessToken, phoneNumberID)
			c.BaseURL = ts.URL
			return c
		}

		if code := cmdSend([]string{"-to", "16505551234", "-text", "Hello"}); code != 1 {
			t.Fatalf("expected 1, got %d", code)
		}
	})
}
