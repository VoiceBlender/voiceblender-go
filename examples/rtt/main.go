// Command rtt is a Real-Time Text (T.140 / RFC 4103) demo for VoiceBlender,
// extended with a WebRTC audio bridge so a browser can join the same room as
// the inbound SIP caller.
//
// Behaviour:
//   - Connects to VoiceBlender via the VSI WebSocket — no webhook config needed.
//   - Hosts a tiny web UI at LISTEN_ADDR. The page can:
//   - "Call" — establishes a WebRTC audio leg via /webrtc/offer and joins
//     the shared room.
//   - Show RTT chunks from the SIP peer letter-by-letter.
//   - Send outgoing RTT, which is delivered to the SIP leg via
//     send_leg_rtt over the VSI socket.
//   - Answers any inbound SIP call. RTT is negotiated automatically when the
//     remote offer includes an m=text section (the VoiceBlender server must
//     run with RTT_ENABLED=true). The answered SIP leg joins the same shared
//     room as the WebRTC leg, so audio mixes both ways.
//   - Single-call demo: while a SIP call is active any other inbound call is
//     rejected with "busy"; while a WebRTC call is active a second /webrtc
//     attempt is rejected with 409. Reload picks up an in-progress session.
//
// Run:
//
//	go run ./examples/rtt
//
// Then open http://localhost:8090 in a browser, click "Call" to join via
// WebRTC, and place an RTT-capable SIP call to the VoiceBlender server.
//
// Environment:
//
//	VOICEBLENDER_URL  default http://localhost:8080/v1
//	LISTEN_ADDR       default :8090
//	TLS_CERT_FILE     PEM certificate path; if set together with
//	                  TLS_KEY_FILE the UI is served over HTTPS/WSS.
//	                  Required by browsers for getUserMedia + WebRTC except
//	                  on localhost.
//	TLS_KEY_FILE      PEM private key path; pairs with TLS_CERT_FILE
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	voiceblender "github.com/VoiceBlender/voice-go"
	"github.com/coder/websocket"
)

//go:embed index.html
var assets embed.FS

const sharedRoomID = "rtt-room"

// uiEvent is what the server pushes to the browser over the WebSocket.
type uiEvent struct {
	Kind string `json:"kind"`           // "call_started", "call_ended", "rtt", "webrtc_*", "error"
	From string `json:"from,omitempty"` // caller URI on call_started
	Dir  string `json:"dir,omitempty"`  // "in" or "out" for kind=="rtt"
	Text string `json:"text,omitempty"` // also error message for kind=="error"
	Loss bool   `json:"loss,omitempty"` // RFC 2198 loss marker on incoming RTT
	Time int64  `json:"time"`           // unix ms
}

// clientMsg is what the browser sends to the server over the UI WebSocket.
type clientMsg struct {
	Type string `json:"type"` // "send"
	Text string `json:"text,omitempty"`
}

// app holds the single active session state and the set of UI subscribers.
type app struct {
	client *voiceblender.Client
	stream *voiceblender.EventStream
	log    *slog.Logger

	mu        sync.Mutex
	room      *voiceblender.Room
	sipLeg    string
	sipFrom   string
	webrtcLeg string
	clients   map[chan uiEvent]struct{}
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	baseURL := envOr("VOICEBLENDER_URL", "http://localhost:8080/v1")
	listenAddr := envOr("LISTEN_ADDR", ":8090")
	certFile := os.Getenv("TLS_CERT_FILE")
	keyFile := os.Getenv("TLS_KEY_FILE")
	if (certFile == "") != (keyFile == "") {
		log.Error("TLS_CERT_FILE and TLS_KEY_FILE must both be set or both unset")
		return
	}
	tlsEnabled := certFile != ""

	a := &app{
		client:  voiceblender.New(voiceblender.WithBaseURL(baseURL)),
		log:     log,
		clients: make(map[chan uiEvent]struct{}),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// One websocket pumps every event into the client's hub (Subscribe API
	// reads from there) AND carries our outgoing VSI commands — including
	// send_leg_rtt for outbound text. Holding the *EventStream lets us call
	// typed VSI methods directly instead of going via HTTP.
	stream, err := a.client.Events(ctx)
	if err != nil {
		log.Error("event stream open", "error", err)
		return
	}
	defer stream.Close()
	a.stream = stream
	go func() {
		if err := stream.PipeTo(ctx, a.client); err != nil && ctx.Err() == nil {
			log.Error("event stream", "error", err)
			cancel()
		}
	}()

	// Pre-create the shared room. Both the WebRTC leg and the SIP leg join
	// this room when their respective leg.connected fires.
	room, err := a.client.CreateRoom(ctx, voiceblender.CreateRoomRequest{ID: sharedRoomID})
	if err != nil && !voiceblender.IsConflict(err) {
		log.Error("create shared room", "error", err)
		return
	}
	if room == nil {
		room = a.client.Room(sharedRoomID)
	}
	a.room = room
	defer func() {
		if _, err := room.Delete(context.Background()); err != nil {
			a.log.Warn("delete shared room", "error", err)
		}
	}()

	go a.handleRingings(ctx)

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(assets)))
	mux.HandleFunc("/ws", a.wsHandler)
	mux.HandleFunc("/api/webrtc/offer", a.handleWebRTCOffer)
	mux.HandleFunc("/api/webrtc/", a.handleWebRTCLeg)

	srv := &http.Server{Addr: listenAddr, Handler: mux}
	log.Info("rtt demo ready", "base_url", baseURL, "listen", listenAddr, "tls", tlsEnabled)
	go func() {
		var err error
		if tlsEnabled {
			err = srv.ListenAndServeTLS(certFile, keyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

// handleRingings answers inbound SIP calls and starts a per-call RTT pump.
func (a *app) handleRingings(ctx context.Context) {
	sub := a.client.Subscribe(voiceblender.EventLegRinging)
	defer sub.Close()

	for {
		ev, err := sub.Next(ctx)
		if err != nil {
			return
		}
		ring := ev.(*voiceblender.LegRingingEvent)
		if ring.LegType != string(voiceblender.LegTypeSIPInbound) {
			continue
		}

		a.mu.Lock()
		busy := a.sipLeg != ""
		if !busy {
			a.sipLeg = ring.LegID
			a.sipFrom = ring.From
		}
		a.mu.Unlock()

		if busy {
			a.log.Info("busy — rejecting", "leg_id", ring.LegID, "from", ring.From)
			_, _ = a.client.Leg(ring.LegID).Hangup(ctx, voiceblender.DeleteLegRequest{Reason: "busy"})
			continue
		}

		a.log.Info("inbound", "leg_id", ring.LegID, "from", ring.From)
		go a.runSIPCall(ctx, ring)
	}
}

// runSIPCall answers the leg, places it in the shared mixer room, and fans
// rtt.received events out to web clients. The leg is added to the room on
// leg.connected because RTT egress flows via the mixer — receive arrives
// direct from the SIP peer, but transmit needs the leg in a room. Audio
// mixes with the WebRTC participant (if any) in the same shared room.
//
// RTT negotiation itself is SDP-driven: if the remote offer included m=text
// and the server has RTT_ENABLED, the answer generated by Answer() will
// include it too.
func (a *app) runSIPCall(ctx context.Context, ring *voiceblender.LegRingingEvent) {
	leg := a.client.Leg(ring.LegID)

	// Subscribe before answering so we don't miss early events.
	sub := leg.Subscribe(
		voiceblender.EventLegConnected,
		voiceblender.EventRTTReceived,
		voiceblender.EventLegDisconnected,
	)
	defer sub.Close()

	if _, err := leg.Answer(ctx, voiceblender.AnswerLegRequest{}); err != nil {
		a.log.Error("answer", "leg_id", ring.LegID, "error", err)
		a.endSIPCall(ring.LegID)
		return
	}

	defer func() {
		a.broadcast(uiEvent{Kind: "call_ended", Time: nowMs()})
		a.endSIPCall(ring.LegID)
	}()

	for {
		ev, err := sub.Next(ctx)
		if err != nil {
			return
		}
		switch e := ev.(type) {
		case *voiceblender.LegConnectedEvent:
			a.log.Info("sip leg connected", "leg_id", e.LegID)
			if _, err := a.room.AddLeg(ctx, voiceblender.AddLegRequest{LegID: ring.LegID}); err != nil {
				a.log.Error("add sip leg to room", "leg_id", ring.LegID, "room_id", sharedRoomID, "error", err)
			}
			a.broadcast(uiEvent{Kind: "call_started", From: ring.From, Time: nowMs()})
		case *voiceblender.RTTReceivedEvent:
			a.log.Info("rtt in", "leg_id", e.LegID, "seq", e.Seq, "loss", e.LossMarker, "text", e.Text)
			a.broadcast(uiEvent{Kind: "rtt", Dir: "in", Text: e.Text, Loss: e.LossMarker, Time: nowMs()})
		case *voiceblender.LegDisconnectedEvent:
			a.log.Info("sip leg disconnected", "leg_id", e.LegID)
			return
		}
	}
}

func (a *app) endSIPCall(legID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sipLeg == legID {
		a.sipLeg = ""
		a.sipFrom = ""
	}
}

// handleWebRTCOffer accepts an SDP offer from the browser and forwards it to
// VoiceBlender via the VSI WebSocket (webrtc_offer command). Returns the SDP
// answer + leg ID. The new WebRTC leg is then watched in a goroutine — on
// leg.connected it joins the shared room.
func (a *app) handleWebRTCOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req voiceblender.WebRTCOfferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.SDP == "" {
		http.Error(w, "missing sdp", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	if a.webrtcLeg != "" {
		a.mu.Unlock()
		http.Error(w, "webrtc leg already active", http.StatusConflict)
		return
	}
	a.mu.Unlock()

	resp, err := a.stream.WebRTCOffer(r.Context(), req)
	if err != nil {
		a.log.Error("webrtc offer (vsi)", "error", err)
		http.Error(w, "webrtc offer failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	a.mu.Lock()
	a.webrtcLeg = resp.LegID
	a.mu.Unlock()

	a.log.Info("webrtc leg created", "leg_id", resp.LegID)
	go a.runWebRTCLeg(context.Background(), resp.LegID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleWebRTCLeg dispatches /api/webrtc/{legID}/{action} requests. All
// VoiceBlender-side calls go over the VSI WebSocket:
//   - GET  /api/webrtc/{legID}/candidates  → webrtc_get_candidates
//   - POST /api/webrtc/{legID}/candidates  → webrtc_add_candidate
//   - POST /api/webrtc/{legID}/hangup      → delete_leg
func (a *app) handleWebRTCLeg(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/webrtc/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 || parts[0] == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	legID, action := parts[0], parts[1]

	switch action {
	case "candidates":
		switch r.Method {
		case http.MethodGet:
			cands, err := a.stream.WebRTCGetCandidates(r.Context(), voiceblender.IDPayload{ID: legID})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cands)
		case http.MethodPost:
			var c voiceblender.ICECandidateInit
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if _, err := a.stream.WebRTCAddCandidate(r.Context(), voiceblender.VSIWebRTCAddCandidatePayload{
				ID:        legID,
				Candidate: c,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "hangup":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := a.stream.DeleteLeg(r.Context(), voiceblender.DeleteLegPayload{ID: legID}); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "not found: "+action, http.StatusNotFound)
	}
}

// runWebRTCLeg watches the WebRTC leg's connect/disconnect events and joins
// the shared room on connect. Uses its own context so it survives request
// cancellation; tied to the SIGINT context via the goroutine started in main.
func (a *app) runWebRTCLeg(ctx context.Context, legID string) {
	leg := a.client.Leg(legID)
	sub := leg.Subscribe(
		voiceblender.EventLegConnected,
		voiceblender.EventLegDisconnected,
	)
	defer sub.Close()
	defer a.endWebRTCLeg(legID)
	defer a.broadcast(uiEvent{Kind: "webrtc_disconnected", Time: nowMs()})

	for {
		ev, err := sub.Next(ctx)
		if err != nil {
			return
		}
		switch e := ev.(type) {
		case *voiceblender.LegConnectedEvent:
			a.log.Info("webrtc leg connected", "leg_id", e.LegID)
			addCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if _, err := a.room.AddLeg(addCtx, voiceblender.AddLegRequest{LegID: legID}); err != nil {
				a.log.Error("add webrtc leg to room", "leg_id", legID, "room_id", sharedRoomID, "error", err)
			}
			cancel()
			a.broadcast(uiEvent{Kind: "webrtc_connected", Time: nowMs()})
		case *voiceblender.LegDisconnectedEvent:
			a.log.Info("webrtc leg disconnected", "leg_id", e.LegID)
			return
		}
	}
}

func (a *app) endWebRTCLeg(legID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.webrtcLeg == legID {
		a.webrtcLeg = ""
	}
}

// wsHandler serves one browser tab. Inbound RTT chunks are pushed in real
// time; outgoing {type:"send",text:"..."} frames are forwarded to SendRTT.
func (a *app) wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Localhost-only demo; loosen as needed for cross-origin embedding.
		InsecureSkipVerify: true,
	})
	if err != nil {
		a.log.Warn("ws accept", "error", err)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "closing")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	ch := make(chan uiEvent, 64)
	a.mu.Lock()
	a.clients[ch] = struct{}{}
	curSIP := a.sipLeg
	curFrom := a.sipFrom
	curWebRTC := a.webrtcLeg
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.clients, ch)
		a.mu.Unlock()
	}()

	// Re-emit current state so a page reload during an active session
	// resumes in the right state. Past RTT chunks are not replayed.
	if curWebRTC != "" {
		_ = writeJSON(ctx, conn, uiEvent{Kind: "webrtc_connected", Time: nowMs()})
	}
	if curSIP != "" {
		_ = writeJSON(ctx, conn, uiEvent{Kind: "call_started", From: curFrom, Time: nowMs()})
	}

	// Reader goroutine: drains client → server frames (outgoing RTT and
	// peer-initiated close). Cancels ctx on exit so the writer loop quits.
	go func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				a.log.Info("ws read end", "error", err)
				return
			}
			var msg clientMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				a.log.Warn("ws bad json", "raw", string(data), "error", err)
				continue
			}
			a.log.Info("ws recv", "type", msg.Type, "text", msg.Text)
			if msg.Type == "send" && msg.Text != "" {
				a.sendRTT(msg.Text)
			}
		}
	}()

	// Writer loop: server → client frames.
	for {
		select {
		case ev := <-ch:
			if err := writeJSON(ctx, conn, ev); err != nil {
				return
			}
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}

// sendRTT pushes outgoing text on the active SIP leg via the VSI WebSocket
// (send_leg_rtt) and echoes it to all UI clients so the local sender sees
// their own messages. Uses a fresh context so a flaky/closing browser WS
// doesn't tear down the VSI call mid-flight.
func (a *app) sendRTT(text string) {
	a.mu.Lock()
	legID := a.sipLeg
	a.mu.Unlock()
	if legID == "" {
		a.broadcast(uiEvent{Kind: "error", Text: "no active sip call", Time: nowMs()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.stream.SendLegRTT(ctx, voiceblender.RTTPayload{ID: legID, Text: text}); err != nil {
		a.log.Error("send rtt", "leg_id", legID, "error", err)
		a.broadcast(uiEvent{Kind: "error", Text: "send failed: " + err.Error(), Time: nowMs()})
		return
	}
	a.log.Info("rtt out", "leg_id", legID, "text", text)
	a.broadcast(uiEvent{Kind: "rtt", Dir: "out", Text: text, Time: nowMs()})
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, b)
}

func (a *app) broadcast(ev uiEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for ch := range a.clients {
		select {
		case ch <- ev:
		default:
			// slow consumer — drop rather than block the call goroutine.
		}
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
