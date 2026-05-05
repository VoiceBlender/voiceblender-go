// Command ivr is a company IVR (Interactive Voice Response) built on VoiceBlender.
//
// Call flow:
//
//	Inbound call
//	  → Early media: UK ringback tone plays for 3 s
//	  → Answer → Welcome greeting → Main menu
//	    1 → Sales queue (room: sales)
//	    2 → Support queue (room: support)
//	    3 → Billing queue (room: billing)
//	    0 → Deepgram AI agent (room: operator)
//	    9 → Repeat menu
//	    * → Goodbye
//	    invalid/timeout → Re-prompt (up to 3 times then goodbye)
//
// # TTS sequencing
//
// Each call tracks its active TTS ID. Starting a new prompt stops the
// previous one first. tts.finished events for replaced prompts are discarded
// so they cannot accidentally advance the state machine.
//
// # Webhook delivery
//
// Each department room is created with WEBHOOK_URL as its per-room webhook so
// that room-level events (playback, recording, agent, etc.) are routed directly
// here without a global webhook registration.
//
// Inbound leg events (leg.ringing, leg.connected, dtmf.received, …) are
// delivered via VoiceBlender's own webhook routing:
//   - Set the WEBHOOK_URL environment variable on the VoiceBlender process, or
//   - Have the calling SIP system include an X-Webhook-URL header in the INVITE.
//
// Environment variables:
//
//	VOICEBLENDER_URL    VoiceBlender base URL (default: http://localhost:8080/v1)
//	LISTEN_ADDR         Address for the webhook HTTP server (default: :8090)
//	WEBHOOK_URL         URL VoiceBlender POSTs events to (default: http://localhost:8090/webhook)
//	ELEVENLABS_API_KEY  ElevenLabs API key (optional if pre-configured in VoiceBlender)
//	TTS_VOICE           TTS voice name (default: Rachel)
//	TTS_PROVIDER        TTS provider name (default: elevenlabs)
//	DEEPGRAM_API_KEY    Deepgram API key for the AI agent (operator queue)
//	COMPANY_NAME        Name spoken in greeting (default: Acme Corp)
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	voiceblender "github.com/VoiceBlender/voice-go"
)

// ivrState is the current menu position for a call.
type ivrState int

const (
	stateGreeting ivrState = iota // playing the welcome message
	stateMenu                     // main menu prompt is playing or we are waiting for a digit
	stateRouted                   // caller has been sent to a department queue
	stateGoodbye                  // playing goodbye, about to hang up
)

// call tracks per-leg IVR state.
type call struct {
	mu             sync.Mutex
	legID          string
	state          ivrState
	activeTTSID    string // tts_id of the currently playing prompt; "" when idle
	attempts       int    // invalid DTMF attempts on the current menu cycle
	pendingMenu    bool   // re-play the main menu once the current TTS finishes
	roomID         string // set once the leg is placed in a department room
	holdPlaybackID string // playback_id of the looping hold music in the room
	holdMessage    string // TTS text repeated every 15 s while waiting in the room
}

// app holds shared IVR resources.
type app struct {
	client      *voiceblender.Client
	log         *slog.Logger
	webhookURL  string
	ttsVoice    string
	ttsProvider string
	ttsAPIKey   string
	companyName string
	calls       sync.Map // legID → *call
}

// webhookEvent mirrors the flat VoiceBlender webhook payload.
// Fields from the typed data struct are merged into the top-level object.
type webhookEvent struct {
	Type  voiceblender.WebhookEventType `json:"type"`
	LegID string                        `json:"leg_id"`
	TTSID string                        `json:"tts_id"`
	Digit string                        `json:"digit"`
	Error string                        `json:"error"`
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	baseURL := envOr("VOICEBLENDER_URL", "http://localhost:8080/v1")
	listenAddr := envOr("LISTEN_ADDR", ":8090")
	webhookURL := envOr("WEBHOOK_URL", "http://localhost"+listenAddr+"/webhook")

	a := &app{
		client:      voiceblender.New(voiceblender.WithBaseURL(baseURL)),
		log:         log,
		webhookURL:  webhookURL,
		ttsVoice:    envOr("TTS_VOICE", "Rachel"),
		ttsProvider: envOr("TTS_PROVIDER", "elevenlabs"),
		ttsAPIKey:   os.Getenv("TTS_API_KEY"),
		companyName: envOr("COMPANY_NAME", "Acme Corp"),
	}

	// Start the webhook server first so it can receive room.created events
	// that VoiceBlender fires when we create the department rooms below.
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", a.handleWebhook)

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Error("listen", "addr", listenAddr, "error", err)
		os.Exit(1)
	}
	log.Info("IVR listening", "addr", ln.Addr())
	go func() {
		if err := http.Serve(ln, mux); err != nil {
			log.Error("http server", "error", err)
			os.Exit(1)
		}
	}()

	ctx := context.Background()

	// Ensure the department rooms exist before any calls arrive.
	// Each room is created with a per-room webhook URL so that room-level
	// events are delivered here without a global webhook registration.
	for _, dept := range []string{"sales", "support", "billing", "operator"} {
		_, err := a.client.CreateRoom(ctx, voiceblender.CreateRoomRequest{
			ID:         dept,
			WebhookURL: webhookURL,
		})
		if err != nil && !voiceblender.IsConflict(err) {
			log.Error("create room", "room", dept, "error", err)
			os.Exit(1)
		}
		log.Info("room ready", "room", dept)
	}

	// Block forever — the HTTP server runs in its own goroutine.
	select {}
}

// handleWebhook receives all VoiceBlender events.
func (a *app) handleWebhook(w http.ResponseWriter, r *http.Request) {
	var ev webhookEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Log every incoming event; include event-specific fields where useful.
	switch ev.Type {
	case voiceblender.EventLegRinging:
		a.log.Info("event", "type", ev.Type, "leg_id", ev.LegID)
		go a.onRinging(ev.LegID)

	case voiceblender.EventLegConnected:
		a.log.Info("event", "type", ev.Type, "leg_id", ev.LegID)
		go a.onConnected(ev.LegID)

	case voiceblender.EventLegDisconnected:
		a.log.Info("event", "type", ev.Type, "leg_id", ev.LegID)
		a.calls.Delete(ev.LegID)

	case voiceblender.EventLegLeftRoom:
		// Caller left the room (e.g. agent picked up and moved them). Stop hold-message repeats.
		a.log.Info("event", "type", ev.Type, "leg_id", ev.LegID)
		if c := a.getCall(ev.LegID); c != nil {
			c.mu.Lock()
			c.holdMessage = ""
			c.mu.Unlock()
		}

	case voiceblender.EventDTMFReceived:
		a.log.Info("event", "type", ev.Type, "leg_id", ev.LegID, "digit", ev.Digit)
		go a.onDTMF(ev.LegID, ev.Digit)

	case voiceblender.EventTTSFinished:
		a.log.Info("event", "type", ev.Type, "leg_id", ev.LegID, "tts_id", ev.TTSID)
		go a.onTTSFinished(ev.LegID, ev.TTSID)

	case voiceblender.EventTTSError:
		a.log.Error("tts error", "leg_id", ev.LegID, "tts_id", ev.TTSID, "error", ev.Error)

	default:
		a.log.Info("event", "type", ev.Type, "leg_id", ev.LegID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// onRinging plays UK ringback via early media for 3 seconds, then answers.
func (a *app) onRinging(legID string) {
	ctx := context.Background()

	c := &call{legID: legID, state: stateGreeting}
	a.calls.Store(legID, c)

	leg := a.client.Leg(legID)

	// Enable early media so we can play audio before answering.
	a.log.Info("cmd", "action", "early_media", "leg_id", legID)
	if _, err := leg.EarlyMedia(ctx, voiceblender.EarlyMediaLegRequest{}); err != nil {
		a.log.Warn("early media not available, answering immediately", "leg_id", legID, "error", err)
	} else {
		// Play UK ringback for 3 seconds then stop it before answering.
		a.log.Info("cmd", "action", "play_leg", "leg_id", legID, "tone", "gb_ringback")
		pb, err := leg.Play(ctx, voiceblender.PlayTone("gb_ringback"))
		if err != nil {
			a.log.Warn("play ringback", "leg_id", legID, "error", err)
		} else {
			time.Sleep(3 * time.Second)
			a.log.Info("cmd", "action", "stop_play_leg", "leg_id", legID, "playback_id", pb.PlaybackID)
			if _, err := leg.StopPlay(ctx, pb.PlaybackID); err != nil && !voiceblender.IsNotFound(err) {
				a.log.Warn("stop ringback", "leg_id", legID, "error", err)
			}
		}
	}

	a.log.Info("cmd", "action", "answer_leg", "leg_id", legID)
	if _, err := leg.Answer(ctx, voiceblender.AnswerLegRequest{}); err != nil {
		a.log.Error("answer leg", "leg_id", legID, "error", err)
		a.calls.Delete(legID)
	}
}

// onConnected plays the welcome greeting once the call is answered.
func (a *app) onConnected(legID string) {
	c := a.getCall(legID)
	if c == nil {
		return
	}

	c.mu.Lock()
	c.state = stateGreeting
	c.mu.Unlock()

	a.speak(legID, "Thank you for calling "+a.companyName+". Please hold while we connect your call.")
}

// onTTSFinished advances the IVR state machine when a prompt finishes playing.
// All sequencing of back-to-back TTS is driven from here to prevent overlap.
//
// ttsID is matched against the call's activeTTSID so that tts.finished events
// fired for prompts that were stopped early (replaced by a newer prompt) are
// silently discarded and do not incorrectly advance the state machine.
func (a *app) onTTSFinished(legID, ttsID string) {
	c := a.getCall(legID)
	if c == nil {
		return
	}

	c.mu.Lock()
	if ttsID != c.activeTTSID {
		// This event is for a prompt that was replaced; ignore it.
		c.mu.Unlock()
		return
	}
	c.activeTTSID = ""
	state := c.state
	pending := c.pendingMenu
	c.pendingMenu = false
	roomID := c.roomID
	holdPlaybackID := c.holdPlaybackID
	c.mu.Unlock()

	switch state {
	case stateGreeting:
		// Greeting done — play the main menu.
		c.mu.Lock()
		c.state = stateMenu
		c.attempts = 0
		c.mu.Unlock()
		a.playMenu(legID)

	case stateMenu:
		// An error or retry prompt just finished — re-play the menu.
		if pending {
			a.playMenu(legID)
		}

	case stateRouted:
		// Hold message done — restore music volume, then repeat after 15 s.
		if roomID != "" && holdPlaybackID != "" {
			ctx := context.Background()
			a.log.Info("cmd", "action", "volume_play_room", "room", roomID, "playback_id", holdPlaybackID, "volume", 0)
			if _, err := a.client.Room(roomID).VolumePlay(ctx, holdPlaybackID, voiceblender.VolumeRequest{Volume: 0}); err != nil && !voiceblender.IsNotFound(err) {
				a.log.Warn("restore hold music volume", "room", roomID, "error", err)
			}
		}
		c.mu.Lock()
		holdMsg := c.holdMessage
		c.mu.Unlock()
		if holdMsg != "" {
			go func() {
				time.Sleep(15 * time.Second)
				c.mu.Lock()
				stillWaiting := c.state == stateRouted
				c.mu.Unlock()
				if stillWaiting {
					a.speak(legID, holdMsg)
				}
			}()
		}

	case stateGoodbye:
		// Goodbye done — hang up.
		ctx := context.Background()
		a.log.Info("cmd", "action", "delete_leg", "leg_id", legID)
		if _, err := a.client.Leg(legID).Hangup(ctx, voiceblender.DeleteLegRequest{}); err != nil && !voiceblender.IsNotFound(err) {
			a.log.Error("delete leg", "leg_id", legID, "error", err)
		}
	}
}

// onDTMF handles a DTMF digit press from the caller.
func (a *app) onDTMF(legID, digit string) {
	c := a.getCall(legID)
	if c == nil {
		return
	}

	c.mu.Lock()
	state := c.state
	c.mu.Unlock()

	if state != stateMenu {
		return // ignore stray digits during greeting/routing/goodbye
	}

	ctx := context.Background()

	switch digit {
	case "1":
		a.routeToDepartment(ctx, legID, "sales", "Sales")
	case "2":
		a.routeToDepartment(ctx, legID, "support", "Support")
	case "3":
		a.routeToDepartment(ctx, legID, "billing", "Billing")
	case "0":
		a.routeToAgent(ctx, legID)
	case "9":
		a.playMenu(legID)
	case "*":
		a.goodbye(legID)
	default:
		c.mu.Lock()
		c.attempts++
		attempts := c.attempts
		c.pendingMenu = true // re-play menu once the error prompt finishes
		c.mu.Unlock()

		if attempts >= 3 {
			a.log.Info("too many invalid inputs, hanging up", "leg_id", legID)
			a.goodbye(legID)
			return
		}
		a.speak(legID, "I'm sorry, that's not a valid option. Please try again.")
		// onTTSFinished sees pendingMenu=true and plays the menu when this ends.
	}
}

// routeToDepartment moves the caller into a department room.
func (a *app) routeToDepartment(ctx context.Context, legID, roomID, displayName string) {
	c := a.getCall(legID)
	if c == nil {
		return
	}

	c.mu.Lock()
	c.state = stateRouted
	c.mu.Unlock()

	room := a.client.Room(roomID)

	a.log.Info("cmd", "action", "add_leg_to_room", "leg_id", legID, "room", roomID)
	resp, err := room.AddLeg(ctx, voiceblender.AddLegRequest{LegID: legID})
	if err != nil {
		a.log.Error("add leg to room", "leg_id", legID, "room", roomID, "error", err)
		c.mu.Lock()
		c.state = stateMenu
		c.pendingMenu = true // re-play menu once the error prompt finishes
		c.mu.Unlock()
		a.speak(legID, "I'm sorry, that queue is not available right now. Please try another option.")
		return
	}

	a.log.Info("caller routed", "leg_id", legID, "room", roomID, "status", resp.Status)

	c.mu.Lock()
	c.roomID = roomID
	c.holdMessage = "Please hold while I connect you to " + displayName + "."
	c.mu.Unlock()

	// Start hold music before speaking so speak() can duck its volume.
	const holdMusicURL = "http://localhost/moh/new_music.mp3"
	a.log.Info("cmd", "action", "play_room", "room", roomID, "url", holdMusicURL)
	holdReq := voiceblender.PlayURL(holdMusicURL, "audio/mpeg")
	holdReq.Repeat = -1 // loop indefinitely
	holdPB, err := room.Play(ctx, holdReq)
	if err != nil {
		a.log.Warn("play hold music", "room", roomID, "error", err)
	} else {
		c.mu.Lock()
		c.holdPlaybackID = holdPB.PlaybackID
		c.mu.Unlock()
	}

	// Routing message plays after hold music starts so speak() can duck it.
	a.speak(legID, "Please hold while I connect you to "+displayName+".")
}

// routeToAgent places the caller in the operator room and attaches a Deepgram
// AI agent to handle the conversation.
func (a *app) routeToAgent(ctx context.Context, legID string) {
	c := a.getCall(legID)
	if c == nil {
		return
	}

	c.mu.Lock()
	c.state = stateRouted
	c.mu.Unlock()

	const roomID = "operator"
	room := a.client.Room(roomID)

	a.log.Info("cmd", "action", "add_leg_to_room", "leg_id", legID, "room", roomID)
	resp, err := room.AddLeg(ctx, voiceblender.AddLegRequest{LegID: legID})
	if err != nil {
		a.log.Error("add leg to room", "leg_id", legID, "room", roomID, "error", err)
		c.mu.Lock()
		c.state = stateMenu
		c.pendingMenu = true
		c.mu.Unlock()
		a.speak(legID, "I'm sorry, the operator is not available right now. Please try another option.")
		return
	}

	a.log.Info("caller routed to agent", "leg_id", legID, "room", roomID, "status", resp.Status)

	a.speak(legID, "Please hold while I connect you to an operator.")

	// Attach a Deepgram voice agent to the room.
	//
	// The settings field is the full Deepgram agent configuration object
	// (agent.listen, agent.think, agent.speak, audio, etc.).
	// See https://developers.deepgram.com/docs/voice-agent for details.
	//
	// TODO: paste your Deepgram agent settings JSON below.
	agentSettings := json.RawMessage(`{
  "type": "Settings",
  "audio": {
    "input":  { "encoding": "linear16", "sample_rate": 48000 },
    "output": { "encoding": "linear16", "sample_rate": 24000, "container": "none" }
  },
  "agent": {
    "language": "en",
    "speak":  { "provider": { "type": "deepgram", "model": "aura-2-odysseus-en" } },
    "listen": { "provider": { "type": "deepgram", "version": "v2", "model": "flux-general-en" } },
    "think":  {
      "provider": { "type": "google", "model": "gemini-2.5-flash" },
      "prompt": "You are a helpful virtual assistant speaking to callers on the phone for Acme Corp. Be warm, concise, and professional. Keep responses to 1-2 sentences."
    },
    "greeting": "Hello! How may I help you?"
  }
}`)

	agentReq := voiceblender.DeepgramAgentRequest{
		Settings: agentSettings,
		APIKey:   os.Getenv("DEEPGRAM_API_KEY"),
	}

	a.log.Info("cmd", "action", "deepgram_agent_room", "room", roomID)
	if _, err := room.DeepgramAgent(ctx, agentReq); err != nil {
		a.log.Error("attach agent", "room", roomID, "error", err)
	}
}

// playMenu speaks the main menu options.
func (a *app) playMenu(legID string) {
	text := "For Sales, press 1. " +
		"For Support, press 2. " +
		"For Billing, press 3. " +
		"For an operator, press 0. " +
		"To repeat this menu, press 9. " +
		"To end the call, press star."
	a.speak(legID, text)
}

// goodbye plays a farewell and transitions to goodbye state.
func (a *app) goodbye(legID string) {
	c := a.getCall(legID)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.state = stateGoodbye
	c.pendingMenu = false
	c.mu.Unlock()

	a.speak(legID, "Thank you for calling "+a.companyName+". Goodbye.")
	// onTTSFinished will hang up once this completes.
}

// speak stops any active TTS prompt, then starts a new one.
// The new tts_id is stored on the call so that onTTSFinished can
// discard events for prompts that were replaced.
func (a *app) speak(legID, text string) {
	c := a.getCall(legID)
	if c == nil {
		return
	}

	ctx := context.Background()

	// Stop the prompt currently playing, if any.
	c.mu.Lock()
	prev := c.activeTTSID
	c.activeTTSID = ""
	roomID := c.roomID
	holdPlaybackID := c.holdPlaybackID
	c.mu.Unlock()

	leg := a.client.Leg(legID)

	if prev != "" {
		a.log.Info("cmd", "action", "stop_play_leg", "leg_id", legID, "tts_id", prev)
		if _, err := leg.StopPlay(ctx, prev); err != nil && !voiceblender.IsNotFound(err) {
			a.log.Warn("stop previous tts", "leg_id", legID, "tts_id", prev, "error", err)
		}
	}

	// Duck hold music while TTS plays (-3 steps ≈ -9 dB).
	if roomID != "" && holdPlaybackID != "" {
		a.log.Info("cmd", "action", "volume_play_room", "room", roomID, "playback_id", holdPlaybackID, "volume", -6)
		if _, err := a.client.Room(roomID).VolumePlay(ctx, holdPlaybackID, voiceblender.VolumeRequest{Volume: -6}); err != nil && !voiceblender.IsNotFound(err) {
			a.log.Warn("duck hold music", "room", roomID, "error", err)
		}
	}

	a.log.Info("cmd", "action", "tts_leg", "leg_id", legID, "text", text)
	resp, err := leg.PlayTTS(ctx, voiceblender.TTSRequest{
		Text:     text,
		Voice:    a.ttsVoice,
		Provider: a.ttsProvider,
		APIKey:   a.ttsAPIKey,
	})
	if err != nil {
		a.log.Error("tts", "leg_id", legID, "error", err)
		return
	}

	c.mu.Lock()
	c.activeTTSID = resp.TTSID
	c.mu.Unlock()
}

// getCall retrieves the call state for a leg, or nil if not found.
func (a *app) getCall(legID string) *call {
	v, ok := a.calls.Load(legID)
	if !ok {
		return nil
	}
	return v.(*call)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
