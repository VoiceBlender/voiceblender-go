package voiceblender

import (
	"context"
	"errors"
	"reflect"
	"sync"
)

// eventHub is an in-process pub/sub fan-out used by the *Sync method family
// (e.g. Leg.PlayTTSSync) and by the per-resource Subscribe APIs.
//
// Subscribers register a matcher predicate; every event delivered to the hub
// is offered to every subscriber whose matcher returns true. Delivery is
// non-blocking — if a subscriber's channel is full the event is dropped for
// that subscriber.
//
// Producers (typically EventStream.PipeTo / Client.DeliverEvent) push events
// into a buffered inbox channel; a dedicated dispatcher goroutine drains the
// inbox and runs the per-sub matchers. This keeps the websocket reader off
// the slow path so VoiceBlender's send buffer doesn't fill while we're
// matching against a large number of subscriptions.
type eventHub struct {
	mu    sync.RWMutex
	subs  map[*subscription]struct{}
	inbox chan interface{}
}

type subscription struct {
	ch    chan interface{}
	match func(ev interface{}) bool
}

const (
	// subBuffer is sized so a per-sub consumer can be in the middle of an
	// HTTP round-trip while a small burst of events queues up without drop.
	subBuffer = 256
	// hubInbox is the producer-side queue between the websocket reader (or
	// webhook handler) and the matcher loop. Sized to absorb spikes without
	// blocking the producer.
	hubInbox = 4096
)

func newEventHub() *eventHub {
	h := &eventHub{
		subs:  make(map[*subscription]struct{}),
		inbox: make(chan interface{}, hubInbox),
	}
	go h.run()
	return h
}

// run drains the inbox and dispatches each event to matching subscribers.
// One goroutine per hub — fan-out to subs is sequential within this goroutine
// but does not block the producer side.
func (h *eventHub) run() {
	for ev := range h.inbox {
		h.dispatch(ev)
	}
}

// subscribe creates a subscription that will receive every dispatched event
// for which match returns true. The caller must call unsubscribe when done.
func (h *eventHub) subscribe(match func(interface{}) bool) *subscription {
	sub := &subscription{
		ch:    make(chan interface{}, subBuffer),
		match: match,
	}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

func (h *eventHub) unsubscribe(sub *subscription) {
	h.mu.Lock()
	delete(h.subs, sub)
	h.mu.Unlock()
}

// dispatch offers ev to every matching subscriber. Non-blocking: if a
// subscriber's buffer is full the event is dropped for that subscriber only.
func (h *eventHub) dispatch(ev interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for sub := range h.subs {
		if sub.match(ev) {
			select {
			case sub.ch <- ev:
			default:
			}
		}
	}
}

// DeliverEvent feeds an event into the client's internal hub so that *Sync
// methods waiting on it can return. Use this from a webhook handler after
// decoding the request body with ParseEvent. If you instead consume events
// from the WebSocket VSI endpoint, use EventStream.PipeTo or Client.RunEventStream.
//
// Delivery is non-blocking: events are queued in a buffered inbox and
// dispatched on a separate goroutine. If the inbox is full (slow matcher
// loop unable to keep up with a sustained burst) the event is dropped to
// keep the caller — typically the websocket reader — from stalling and
// causing the server to disconnect on its own send-buffer pressure.
func (c *Client) DeliverEvent(ev interface{}) {
	select {
	case c.events.inbox <- ev:
	default:
	}
}

// Subscription is a stream of events delivered to a subscriber. Values are
// typed event pointers (e.g. *LegConnectedEvent, *TTSFinishedEvent); use a
// type switch to handle them. Close the subscription when done — until then,
// the goroutine on the producer side will keep matching events against it.
type Subscription struct {
	sub  *subscription
	hub  *eventHub
	once sync.Once
}

// Events returns a receive-only channel of events. The channel is closed
// when Close is called, so a `for ev := range sub.Events()` loop exits
// cleanly. The channel buffer is sized so well-behaved consumers won't drop;
// if the consumer falls far behind, dispatched events are silently dropped
// for this subscription.
func (s *Subscription) Events() <-chan interface{} { return s.sub.ch }

// Next is a convenience for consumers that prefer a blocking read with
// cancellation over ranging the channel. It returns the next event, or an
// error if ctx is cancelled or the subscription is closed.
func (s *Subscription) Next(ctx context.Context) (interface{}, error) {
	select {
	case ev, ok := <-s.sub.ch:
		if !ok {
			return nil, errors.New("voiceblender: subscription closed")
		}
		return ev, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close ends the subscription and closes the events channel. Idempotent.
func (s *Subscription) Close() {
	s.once.Do(func() {
		s.hub.unsubscribe(s.sub)
		// Safe to close: unsubscribe acquired the hub's write lock, so any
		// in-flight dispatch had already released its read lock and will not
		// try to send to this channel again.
		close(s.sub.ch)
	})
}

// Subscribe returns a Subscription that receives every event scoped to this
// leg (matched by leg_id). If one or more event types are passed, only events
// with those types are delivered. Pass no types to receive all leg-scoped
// events.
//
// Requires an event source feeding the client's hub — start one via
// Client.RunEventStream (WebSocket VSI) or call Client.DeliverEvent for each
// event your webhook handler receives.
//
//	sub := leg.Subscribe(voiceblender.EventDTMFReceived)
//	defer sub.Close()
//	for ev := range sub.Events() {
//	    if d, ok := ev.(*voiceblender.DTMFReceivedEvent); ok { ... }
//	}
func (l *Leg) Subscribe(types ...WebhookEventType) *Subscription {
	return newSubscription(l.client.events, "LegID", l.ID, types)
}

// Subscribe returns a Subscription that receives every event scoped to this
// room (matched by room_id). See Leg.Subscribe.
func (r *Room) Subscribe(types ...WebhookEventType) *Subscription {
	return newSubscription(r.client.events, "RoomID", r.ID, types)
}

// Subscribe returns a Subscription that receives every event delivered to the
// client's hub, optionally filtered by event type. Use this to listen for
// events that are not yet associated with a known *Leg or *Room — e.g. the
// initial leg.ringing for an inbound call.
func (c *Client) Subscribe(types ...WebhookEventType) *Subscription {
	return newSubscription(c.events, "", "", types)
}

// newSubscription creates a Subscription whose matcher checks the named
// resource-ID field on each event and (optionally) restricts to a set of
// event types. idField is "LegID" or "RoomID" — both are present as exported
// fields on every event struct that scopes to that resource.
func newSubscription(hub *eventHub, idField, idValue string, types []WebhookEventType) *Subscription {
	typeSet := makeTypeSet(types)
	match := func(ev interface{}) bool {
		if idField != "" && extractStringField(ev, idField) != idValue {
			return false
		}
		if len(typeSet) == 0 {
			return true
		}
		_, ok := typeSet[extractEventType(ev)]
		return ok
	}
	sub := hub.subscribe(match)
	return &Subscription{sub: sub, hub: hub}
}

func makeTypeSet(types []WebhookEventType) map[WebhookEventType]struct{} {
	if len(types) == 0 {
		return nil
	}
	s := make(map[WebhookEventType]struct{}, len(types))
	for _, t := range types {
		s[t] = struct{}{}
	}
	return s
}

// extractStringField returns the string value of the named exported field on
// ev (which is expected to be a pointer to an event struct), or "" if the
// field doesn't exist or isn't a string. Embedded struct fields (i.e. the
// promoted Type field on the inner Event) are followed.
func extractStringField(ev interface{}, name string) string {
	v := reflect.Indirect(reflect.ValueOf(ev))
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return ""
	}
	f := v.FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.String {
		return ""
	}
	return f.String()
}

func extractEventType(ev interface{}) WebhookEventType {
	return WebhookEventType(extractStringField(ev, "Type"))
}
