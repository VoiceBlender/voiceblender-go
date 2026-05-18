package voiceblender

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// EventStream receives real-time events from the VoiceBlender Streaming
// Interface (VSI) WebSocket endpoint and (when used as a *EventStream
// receiver) issues client-to-server commands over the same socket. Use
// Client.Events to create one.
type EventStream struct {
	conn *websocket.Conn

	// writeMu serialises Conn.Write calls; coder/websocket allows one
	// concurrent reader and one concurrent writer, but writes must not race.
	writeMu sync.Mutex

	// done guards Close so it's idempotent.
	closeMu sync.Mutex
	done    bool

	// Inflight VSI commands awaiting their `<cmd>.result` / `error` reply,
	// keyed by request_id.
	inflightMu sync.Mutex
	inflight   map[string]chan vsiResp

	// Monotonic request_id counter.
	nextID atomic.Uint64
}

// vsiResp carries one demuxed command response from the read loop to the
// goroutine waiting in EventStream.call.
type vsiResp struct {
	frameType string          // "<cmd>.result" or "error"
	data      json.RawMessage // the frame's `data` field (raw)
}

// vsiInFrame is the wire envelope used by VSI for both events and command
// responses; only the fields we need to demux are decoded.
type vsiInFrame struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// EventStreamOption configures an EventStream.
type EventStreamOption func(*eventStreamConfig)

type eventStreamConfig struct {
	httpClient *http.Client
}

// WithEventHTTPClient overrides the HTTP client used for the WebSocket dial.
func WithEventHTTPClient(hc *http.Client) EventStreamOption {
	return func(cfg *eventStreamConfig) { cfg.httpClient = hc }
}

// Events opens a WebSocket connection to the VSI endpoint and returns an
// EventStream. The caller must call Close when done. The returned stream
// blocks in Next until an event arrives or the context is cancelled.
func (c *Client) Events(ctx context.Context, opts ...EventStreamOption) (*EventStream, error) {
	cfg := eventStreamConfig{httpClient: c.httpClient}
	for _, o := range opts {
		o(&cfg)
	}

	wsURL := strings.Replace(c.baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL += "/vsi"

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: cfg.httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("voiceblender: dial VSI: %w", err)
	}

	// Wait for the server's initial {"type":"connected"} message.
	_, data, err := conn.Read(ctx)
	if err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("voiceblender: read connected message: %w", err)
	}
	var hello struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &hello); err != nil || hello.Type != "connected" {
		conn.CloseNow()
		return nil, fmt.Errorf("voiceblender: unexpected initial message: %s", data)
	}

	return &EventStream{
		conn:     conn,
		inflight: make(map[string]chan vsiResp),
	}, nil
}

// Next reads the next event from the stream. It blocks until an event is
// available, the context is cancelled, or the connection is closed. Ping
// frames from the server are automatically answered with pong, and command
// responses (`<cmd>.result` / `error` for in-flight VSI commands) are
// transparently routed to the waiting caller and not surfaced as events.
func (s *EventStream) Next(ctx context.Context) (interface{}, error) {
	for {
		_, data, err := s.conn.Read(ctx)
		if err != nil {
			return nil, err
		}

		var env vsiInFrame
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, fmt.Errorf("voiceblender: decode event type: %w", err)
		}

		if env.Type == "ping" {
			s.writeMu.Lock()
			_ = s.conn.Write(ctx, websocket.MessageText, []byte(`{"type":"pong"}`))
			s.writeMu.Unlock()
			continue
		}

		// Demux command responses. A frame is a response iff it carries a
		// request_id we issued and its type is `<cmd>.result` or `error`.
		if env.RequestID != "" && (env.Type == "error" || strings.HasSuffix(env.Type, ".result")) {
			s.inflightMu.Lock()
			ch, ok := s.inflight[env.RequestID]
			if ok {
				delete(s.inflight, env.RequestID)
			}
			s.inflightMu.Unlock()
			if ok {
				// Buffered channel of size 1 — never blocks.
				ch <- vsiResp{frameType: env.Type, data: env.Data}
				continue
			}
			// Orphan response (unknown request_id) — drop silently.
			continue
		}

		return ParseEvent(data)
	}
}

// PipeTo runs Next in a loop and dispatches every event into the client's
// internal hub so that *Sync methods (e.g. Leg.PlayTTSSync) can be unblocked.
// It blocks until ctx is cancelled or the stream errors, and returns that
// error. Typical usage is to start it in a goroutine alongside the main flow:
//
//	stream, _ := client.Events(ctx)
//	go stream.PipeTo(ctx, client)
func (s *EventStream) PipeTo(ctx context.Context, c *Client) error {
	for {
		ev, err := s.Next(ctx)
		if err != nil {
			return err
		}
		c.DeliverEvent(ev)
	}
}

// RunEventStream is a convenience that opens a VSI WebSocket EventStream and
// pumps its events into the client's hub. It blocks until ctx is cancelled
// or the stream errors. Equivalent to:
//
//	s, err := c.Events(ctx, opts...)
//	if err != nil { return err }
//	defer s.Close()
//	return s.PipeTo(ctx, c)
func (c *Client) RunEventStream(ctx context.Context, opts ...EventStreamOption) error {
	s, err := c.Events(ctx, opts...)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.PipeTo(ctx, c)
}

// Close gracefully closes the WebSocket connection by sending a stop message.
func (s *EventStream) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.done {
		return nil
	}
	s.done = true
	s.writeMu.Lock()
	_ = s.conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"stop"}`))
	s.writeMu.Unlock()
	return s.conn.Close(websocket.StatusNormalClosure, "client closed")
}

// VSIError is the error returned by VSI command methods when the server
// replies with an `error` frame.
type VSIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *VSIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("voiceblender vsi: %d %s", e.Code, e.Message)
	}
	return "voiceblender vsi: " + e.Message
}

// call performs a VSI request/response round-trip. It writes a command frame
// `{type, request_id, payload}` to the WebSocket, waits for the matching
// `<cmd>.result` or `error` reply, and unmarshals the result's `data` into
// `result` (which may be nil if the caller doesn't need the body).
//
// Used by the generated typed methods in vsi.go.
func (s *EventStream) call(ctx context.Context, cmdType string, payload, result interface{}) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("voiceblender: vsi: stream not initialised")
	}

	rid := fmt.Sprintf("vsi-%d", s.nextID.Add(1))
	ch := make(chan vsiResp, 1)

	s.inflightMu.Lock()
	if s.inflight == nil {
		s.inflight = make(map[string]chan vsiResp)
	}
	s.inflight[rid] = ch
	s.inflightMu.Unlock()

	// Always clean up the inflight entry; if the response arrives after we
	// give up (ctx timeout), Next() drops it as an orphan.
	defer func() {
		s.inflightMu.Lock()
		delete(s.inflight, rid)
		s.inflightMu.Unlock()
	}()

	frame := struct {
		Type      string      `json:"type"`
		RequestID string      `json:"request_id"`
		Payload   interface{} `json:"payload,omitempty"`
	}{cmdType, rid, payload}

	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("voiceblender: vsi marshal %s: %w", cmdType, err)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	s.writeMu.Lock()
	err = s.conn.Write(writeCtx, websocket.MessageText, data)
	s.writeMu.Unlock()
	cancel()
	if err != nil {
		return fmt.Errorf("voiceblender: vsi write %s: %w", cmdType, err)
	}

	select {
	case resp := <-ch:
		if resp.frameType == "error" {
			var e VSIError
			_ = json.Unmarshal(resp.data, &e)
			if e.Message == "" {
				e.Message = "unknown error"
			}
			return &e
		}
		if result != nil && len(resp.data) > 0 {
			if err := json.Unmarshal(resp.data, result); err != nil {
				return fmt.Errorf("voiceblender: vsi decode %s.result: %w", cmdType, err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
