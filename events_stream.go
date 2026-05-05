package voiceblender

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// EventStream receives real-time events from the VoiceBlender Streaming
// Interface (VSI) WebSocket endpoint. Use Client.Events to create one.
type EventStream struct {
	conn *websocket.Conn
	mu   sync.Mutex
	done bool
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

	return &EventStream{conn: conn}, nil
}

// Next reads the next event from the stream. It blocks until an event is
// available, the context is cancelled, or the connection is closed. Ping
// frames from the server are automatically answered with pong.
func (s *EventStream) Next(ctx context.Context) (interface{}, error) {
	for {
		_, data, err := s.conn.Read(ctx)
		if err != nil {
			return nil, err
		}

		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, fmt.Errorf("voiceblender: decode event type: %w", err)
		}

		if envelope.Type == "ping" {
			s.mu.Lock()
			_ = s.conn.Write(ctx, websocket.MessageText, []byte(`{"type":"pong"}`))
			s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil
	}
	s.done = true
	_ = s.conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"stop"}`))
	return s.conn.Close(websocket.StatusNormalClosure, "client closed")
}
