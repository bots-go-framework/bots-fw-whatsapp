// Command wasmoke is a manual smoke harness for the WhatsApp Cloud API adapter.
//
// It exists to answer the one question the test suite cannot: does the wire behave
// the way Meta's docs say? Every unit test runs against httptest and documented
// payloads. This runs against a real WhatsApp Business number.
//
// It is deliberately NOT a `go test`: it takes real credentials, sends real
// messages, and must never run in CI. `go build ./...` compiles it; `go test ./...`
// finds nothing here. Run it by hand once a number is provisioned.
//
// # The experiment it is built to run
//
// The whole ToGethered design rests on an inference nobody has verified: that a
// template quick-reply tap reopens the 24-hour customer-service window. Both halves
// are documented (a tap "immediately messages you"; the window resets on the
// recipient's last reply) but the conjunction is not. This harness makes it
// observable:
//
//  1. `wasmoke serve`  — run the webhook receiver, log every inbound in full.
//  2. `wasmoke send -to <you> -template <name> -lang en_US`  — send a template.
//  3. Tap a button on your phone.
//  4. Watch `serve`: the tap arrives as an inbound message. If the window is then
//     open (serve prints "window: OPEN"), a plain -text send will now succeed where
//     it failed before the tap. That is the door-opener, proven or refuted.
//
// # Configuration (environment)
//
//	WA_PHONE_NUMBER_ID   the sending business phone number ID (send mode)
//	WA_ACCESS_TOKEN      a Cloud API access token          (send mode)
//	WA_APP_SECRET        the app secret, for signature validation (serve mode)
//	WA_VERIFY_TOKEN      the hub.verify_token you set in the dashboard (serve mode)
//	WA_PORT              serve listen port (default 8080)
//	WA_GRAPH_VERSION     override the Graph API version (optional)
//
// See the README in this directory for the full runbook (ngrok, dashboard setup).
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bots-go-framework/bots-api-whatsapp/wabotapi"
	"github.com/bots-go-framework/bots-fw-whatsapp/whatsapp"
)

const botID = "wasmoke"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(cmdServe(os.Args[2:]))
	case "send":
		os.Exit(cmdSend(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `wasmoke — manual WhatsApp Cloud API smoke harness

USAGE
  wasmoke serve                       run the webhook receiver
  wasmoke send  -to <number> -text <s>          send a free-form text (in-window only)
  wasmoke send  -to <number> -template <name> -lang <code> [-param v -param v ...]

ENVIRONMENT
  send:   WA_PHONE_NUMBER_ID  WA_ACCESS_TOKEN  [WA_GRAPH_VERSION]
  serve:  WA_APP_SECRET  WA_VERIFY_TOKEN  [WA_PORT=8080]

Nothing here touches the network until you run it. See ./README.md for the runbook.
`)
}

// requireEnv returns the named vars, or lists everything missing and returns an
// error naming them all at once — so a first run tells you the whole story.
func requireEnv(names ...string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	var missing []string
	for _, n := range names {
		v := strings.TrimSpace(os.Getenv(n))
		if v == "" {
			missing = append(missing, n)
		}
		out[n] = v
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("not configured — set: %s", strings.Join(missing, " "))
	}
	return out, nil
}

// ---- serve -----------------------------------------------------------------

func cmdServe(_ []string) int {
	env, err := requireEnv("WA_APP_SECRET", "WA_VERIFY_TOKEN")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	port := os.Getenv("WA_PORT")
	if port == "" {
		port = "8080"
	}

	store := newMemoryChatDataStore()
	lastInbound := whatsapp.NewStoreLastInboundProvider(store, botID)

	handler := whatsapp.NewWebhookHandler(
		botID, env["WA_VERIFY_TOKEN"], env["WA_APP_SECRET"], store,
		func(ctx context.Context, messages []whatsapp.InboundMessage) error {
			for _, m := range messages {
				logInbound(ctx, m, lastInbound)
			}
			return nil
		},
	).OnStatuses(func(_ context.Context, statuses []whatsapp.MessageStatus) {
		for _, s := range statuses {
			billed := s.Pricing != nil && s.Pricing.Billable
			fmt.Printf("  status   %-9s recip=%s billed=%v\n", s.Status, s.RecipientID, billed)
		}
	})

	mux := http.NewServeMux()
	mux.Handle("/webhook", handler)

	fmt.Printf("wasmoke serve — listening on :%s/webhook\n", port)
	fmt.Println("  GET  = hub.challenge handshake (point the dashboard here)")
	fmt.Println("  POST = signed event notifications")
	fmt.Println("  waiting for inbound… tap a template button on your phone to test the door-opener")
	fmt.Println()

	// A read timeout the body-size cap complements; the handler itself caps the body.
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "server stopped: %v\n", err)
		return 1
	}
	return 0
}

// logInbound prints the evidence that matters for the door-opener experiment.
func logInbound(ctx context.Context, m whatsapp.InboundMessage, lastInbound whatsapp.LastInboundProvider) {
	fmt.Printf("← inbound  from=%s type=%s id=%s\n", m.From, m.Type, m.ID)

	switch {
	case m.Text != nil:
		fmt.Printf("    text: %q\n", m.Text.Body)

	case m.Button != nil:
		// TEMPLATE quick-reply tap: payload is the LABEL, not developer state.
		fmt.Printf("    TEMPLATE button tap — payload=%q text=%q\n", m.Button.Payload, m.Button.Text)
		fmt.Printf("    context.id=%q  (the ONLY link back to what this was about)\n", m.ContextMessageID())
		if m.Button.Payload == m.Button.Text {
			fmt.Println("    ⇒ payload == text: confirms the payload is the button label, carrying no state")
		}

	case m.Interactive != nil && m.Interactive.Reply() != nil:
		// In-window interactive reply: id IS business-supplied state.
		r := m.Interactive.Reply()
		fmt.Printf("    INTERACTIVE reply (%s) — id=%q title=%q\n", m.Interactive.Type, r.ID, r.Title)
		fmt.Println("    ⇒ id is your own callback data: state round-trips here")
	}

	// The door-opener check: is the window open now that this inbound was recorded?
	if at, err := lastInbound.LastInboundAt(ctx, m.From); err == nil {
		open := whatsapp.IsWithinWindow(at, time.Now())
		state := "CLOSED"
		if open {
			state = "OPEN"
		}
		fmt.Printf("    window: %s (last inbound %s)\n", state, at.Format(time.RFC3339))
		if open {
			fmt.Println("    ⇒ a plain -text send to this number should now SUCCEED")
		}
	}
	fmt.Println()
}

// ---- send ------------------------------------------------------------------

func cmdSend(args []string) int {
	env, err := requireEnv("WA_PHONE_NUMBER_ID", "WA_ACCESS_TOKEN")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	var to, text, template, lang string
	var params multiFlag
	fs := newFlagSet(args)
	fs.str("to", &to)
	fs.str("text", &text)
	fs.str("template", &template)
	fs.str("lang", &lang)
	fs.multi("param", &params)
	if err := fs.parse(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	if to == "" {
		fmt.Fprintln(os.Stderr, "-to <number> is required (E.164 without +, e.g. 16505551234)")
		return 2
	}
	if (text == "") == (template == "") {
		fmt.Fprintln(os.Stderr, "give exactly one of -text or -template")
		return 2
	}

	client := wabotapi.NewClient(env["WA_ACCESS_TOKEN"], env["WA_PHONE_NUMBER_ID"])
	if v := os.Getenv("WA_GRAPH_VERSION"); v != "" {
		client.GraphVersion = v
	}
	ctx := context.Background()

	var resp *wabotapi.SendMessageResponse
	if text != "" {
		fmt.Printf("→ sending text to %s …\n", to)
		resp, err = client.SendText(ctx, to, text)
	} else {
		if lang == "" {
			lang = "en_US"
		}
		fmt.Printf("→ sending template %q (%s) to %s, params=%v …\n", template, lang, to, []string(params))
		resp, err = client.SendTemplate(ctx, to, template, lang, params...)
	}

	if err != nil {
		return reportSendError(err)
	}
	fmt.Printf("✓ sent. wamid=%s\n", resp.MessageID())
	if text != "" {
		fmt.Println("  (a text send succeeding means this number is INSIDE the 24h window)")
	} else {
		fmt.Println("  now tap a button and watch `wasmoke serve` — that tap should reopen the window")
	}
	return 0
}

// reportSendError classifies the failure the way a caller should, so the harness
// output is a legend for the error model, not a raw dump.
func reportSendError(err error) int {
	apiErr := wabotapi.AsAPIError(err)
	if apiErr == nil {
		fmt.Fprintf(os.Stderr, "✗ transport error: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "✗ API error code=%d: %s\n", apiErr.Code, apiErr.Message)
	switch {
	case apiErr.IsReEngagementRequired():
		fmt.Fprintln(os.Stderr, "  ⇒ 131047: outside the 24h window. Send a TEMPLATE instead, or have the user message you first.")
	case apiErr.IsRateLimited():
		fmt.Fprintln(os.Stderr, "  ⇒ throttled (HTTP 400, no Retry-After). Back off and retry.")
	case apiErr.IsUnreachable():
		fmt.Fprintln(os.Stderr, "  ⇒ recipient unreachable (not a WhatsApp number, or blocked). Do not retry.")
	case apiErr.IsAuthError():
		fmt.Fprintln(os.Stderr, "  ⇒ auth: check WA_ACCESS_TOKEN and its permissions.")
	case apiErr.IsTemplateError():
		fmt.Fprintln(os.Stderr, "  ⇒ template problem: name/language wrong, not approved, or still syncing.")
	}
	return 1
}

// ---- in-memory ChatDataStore ----------------------------------------------

// memoryChatDataStore is a process-lifetime ChatDataStore. Enough for a smoke run;
// the real deployment uses whatsapp.NewDalgoChatDataStore.
type memoryChatDataStore struct {
	mu   sync.Mutex
	data map[string]*whatsapp.WaChatData
}

func newMemoryChatDataStore() *memoryChatDataStore {
	return &memoryChatDataStore{data: map[string]*whatsapp.WaChatData{}}
}

func (s *memoryChatDataStore) GetChatData(_ context.Context, botID, chatID string) (*whatsapp.WaChatData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.data[botID+"/"+chatID]; ok {
		return d, nil
	}
	return nil, nil // a missing chat is not an error
}

func (s *memoryChatDataStore) SaveChatData(_ context.Context, botID, chatID string, d *whatsapp.WaChatData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[botID+"/"+chatID] = d
	return nil
}

// ---- a tiny flag layer -----------------------------------------------------
//
// The stdlib flag package is avoided so -param can repeat and the error messages
// stay in this tool's voice.

type multiFlag []string

type flagSet struct {
	args   []string
	strs   map[string]*string
	multis map[string]*multiFlag
}

func newFlagSet(args []string) *flagSet {
	return &flagSet{args: args, strs: map[string]*string{}, multis: map[string]*multiFlag{}}
}
func (f *flagSet) str(name string, p *string)      { f.strs[name] = p }
func (f *flagSet) multi(name string, p *multiFlag) { f.multis[name] = p }

func (f *flagSet) parse() error {
	for i := 0; i < len(f.args); i++ {
		a := f.args[i]
		name := strings.TrimLeft(a, "-")
		if name == a {
			return fmt.Errorf("expected a -flag, got %q", a)
		}
		i++
		if i >= len(f.args) {
			return fmt.Errorf("flag -%s needs a value", name)
		}
		val := f.args[i]
		if p, ok := f.strs[name]; ok {
			*p = val
			continue
		}
		if p, ok := f.multis[name]; ok {
			*p = append(*p, val)
			continue
		}
		return fmt.Errorf("unknown flag -%s", name)
	}
	return nil
}
