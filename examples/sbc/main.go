// Command sbc is a tiny Session Border Controller (B2BUA) built on
// VoiceBlender. For each inbound SIP call it routes the dialed number to a
// configured upstream PSTN host and is signalling-transparent: every
// provisional and final response on the outbound leg is mirrored onto the
// inbound leg.
//
//	outbound 180 Ringing             → inbound.Ring()       (180 to caller)
//	outbound 183 Session Progress    → inbound.EarlyMedia() (183 + early media to caller)
//	outbound 200 OK                  → inbound.Answer()     (200 OK to caller)
//	outbound 4xx/5xx / BYE pre-connect
//	                                 → inbound.Hangup()     (default cause)
//
// The inbound leg is added to the mixer the moment it transitions to
// early-media or connected so that whatever audio reaches the outbound leg
// (e.g. remote-side ringback) is bridged to the caller.
//
// Routing: the user part of the inbound To URI is preserved and the host
// is rewritten to PSTN_HOST. For example, an inbound call to
// sip:18005551234@some.upstream becomes an outbound to sip:18005551234@pstn.
//
// Configuration (env):
//
//	VOICEBLENDER_URL  default http://localhost:8080/v1
//	PSTN_HOST         default pstn — host[:port] used to route the dialed
//	                  number upstream (e.g. "pstn", "pstn.example.com:5060")
//
// The demo subscribes to events via the WebSocket VSI endpoint, so the
// VoiceBlender instance does not need any per-leg/per-room webhook config.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	voiceblender "github.com/VoiceBlender/voice-go"
)

// forcedCodec is selected for both inbound (183 + 200 OK SDPs) and outbound
// (INVITE codec preference). PCMA gives a single-codec bridge with no
// transcoding when the remote also offers PCMA.
const forcedCodec = "PCMA"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	baseURL := envOr("VOICEBLENDER_URL", "http://localhost:8080/v1")
	pstnHost := envOr("PSTN_HOST", "pstn")

	client := voiceblender.New(voiceblender.WithBaseURL(baseURL))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// One websocket pumps every event into the client's hub. The Subscribe
	// API on Client / *Leg reads from the hub.
	go func() {
		if err := client.RunEventStream(ctx); err != nil && ctx.Err() == nil {
			log.Error("event stream", "error", err)
			cancel()
		}
	}()

	ringings := client.Subscribe(voiceblender.EventLegRinging)
	defer ringings.Close()

	log.Info("sbc ready", "base_url", baseURL, "pstn_host", pstnHost)

	for {
		ev, err := ringings.Next(ctx)
		if err != nil {
			return
		}
		ring := ev.(*voiceblender.LegRingingEvent)
		// Bridge inbound calls only. Outbound legs we create ourselves also
		// emit leg.ringing (when the remote starts ringing) — skip those.
		if ring.LegType != string(voiceblender.LegTypeSIPInbound) {
			continue
		}
		dialed := sipUser(ring.To)
		if dialed == "" {
			log.Warn("inbound has no dialed number, dropping", "leg_id", ring.LegID, "to", ring.To)
			_, _ = client.Leg(ring.LegID).Hangup(ctx, voiceblender.DeleteLegRequest{Reason: "not_found"})
			continue
		}
		log.Info("inbound call", "leg_id", ring.LegID, "from", ring.From, "dialed", dialed)
		go bridge(ctx, log, client, ring, dialed, pstnHost)
	}
}

// bridge handles one inbound call from leg.ringing through to either both
// legs connected or one leg disconnected. Provisional and final responses
// on the outbound side are mirrored onto the inbound side so the upstream
// caller sees a transparent signalling stream.
func bridge(ctx context.Context, log *slog.Logger, client *voiceblender.Client, ring *voiceblender.LegRingingEvent, dialed, pstnHost string) {
	inbound := client.Leg(ring.LegID)

	// 1. Create a room scoped to this call pair. Using the inbound leg ID as
	//    a suffix guarantees uniqueness and makes logs trivial to correlate.
	roomID := "sbc-" + ring.LegID
	room, err := client.CreateRoom(ctx, voiceblender.CreateRoomRequest{ID: roomID})
	if err != nil {
		log.Error("create room", "room_id", roomID, "error", err)
		_, _ = inbound.Hangup(ctx, voiceblender.DeleteLegRequest{})
		return
	}
	defer func() {
		if _, err := room.Delete(context.Background()); err != nil {
			log.Warn("delete room", "room_id", room.ID, "error", err)
		}
	}()

	// 2. Originate the outbound leg. With room_id set, VoiceBlender adds it
	//    to the mixer automatically as soon as it has media (early_media or
	//    connected). The inbound leg is added to the room when we mirror its
	//    state to early_media or connected (see below).
	outboundTo := "sip:" + dialed + "@" + pstnHost
	outbound, err := client.CreateLeg(ctx, voiceblender.CreateLegRequest{
		Type:   "sip",
		To:     outboundTo,
		From:   ring.From,
		RoomID: room.ID,
		// Force PCMA in the outbound INVITE so the bridge stays PCMA end-to-end.
		Codecs: []string{forcedCodec},
	})
	if err != nil {
		log.Error("create outbound", "dest", outboundTo, "error", err)
		_, _ = inbound.Hangup(ctx, voiceblender.DeleteLegRequest{})
		return
	}
	log.Info("outbound dialing", "leg_id", outbound.ID, "to", outboundTo)

	// 3. Mirror outbound signalling onto inbound. The events we listen for
	//    correspond to: 180, 183, 200, and BYE/4xx/5xx respectively.
	//    Subscribing after CreateLeg has the same theoretical race as
	//    elsewhere, but SIP timing is many orders of magnitude wider.
	sub := outbound.Subscribe(
		voiceblender.EventLegRinging,      // 180
		voiceblender.EventLegEarlyMedia,   // 183
		voiceblender.EventLegConnected,    // 200
		voiceblender.EventLegDisconnected, // BYE / 4xx / 5xx
	)
	defer sub.Close()

	inboundInRoom := false
	addInboundToRoomOnce := func() {
		if inboundInRoom {
			return
		}
		if _, err := room.AddLeg(ctx, voiceblender.AddLegRequest{LegID: inbound.ID}); err != nil {
			log.Error("add inbound to room", "leg_id", inbound.ID, "room_id", room.ID, "error", err)
			return
		}
		inboundInRoom = true
	}

	for {
		ev, err := sub.Next(ctx)
		if err != nil {
			return
		}
		switch e := ev.(type) {
		case *voiceblender.LegRingingEvent:
			// Outbound got 180; mirror as 180 to the caller. ringLeg is
			// idempotent so re-sends from the upstream are tolerated.
			log.Info("outbound 180 — ringing inbound", "outbound", outbound.ID, "inbound", inbound.ID)
			if _, err := inbound.Ring(ctx); err != nil {
				// Often the leg is past ringing state by the time a duplicate
				// 180 arrives — log at warn, don't tear down.
				log.Warn("ring inbound", "leg_id", inbound.ID, "error", err)
			}

		case *voiceblender.LegEarlyMediaEvent:
			// Outbound got 183 with SDP; mirror as 183 to the caller and put
			// the inbound into the mixer so the upstream's ringback / IVR is
			// bridged through.
			log.Info("outbound 183 — early-media inbound", "outbound", outbound.ID, "inbound", inbound.ID)
			if _, err := inbound.EarlyMedia(ctx, voiceblender.EarlyMediaLegRequest{Codec: forcedCodec}); err != nil {
				log.Error("early-media inbound", "leg_id", inbound.ID, "error", err)
				_, _ = outbound.Hangup(ctx, voiceblender.DeleteLegRequest{})
				_, _ = inbound.Hangup(ctx, voiceblender.DeleteLegRequest{})
				return
			}
			addInboundToRoomOnce()

		case *voiceblender.LegConnectedEvent:
			log.Info("outbound 200 — answering inbound", "outbound", outbound.ID, "inbound", inbound.ID)
			if _, err := inbound.Answer(ctx, voiceblender.AnswerLegRequest{Codec: forcedCodec}); err != nil {
				log.Error("answer inbound", "leg_id", inbound.ID, "error", err)
				_, _ = outbound.Hangup(ctx, voiceblender.DeleteLegRequest{})
				return
			}
			addInboundToRoomOnce()
			// Both legs are now bridged via the mixer; switch to the
			// post-connect teardown handler.
			waitForTeardown(ctx, log, inbound, outbound)
			return

		case *voiceblender.LegDisconnectedEvent:
			// Outbound failed before connecting — could be 4xx, 5xx, BYE,
			// timeout, etc. Map the outbound's cdr.reason to a DeleteLegRequest
			// reason so VoiceBlender returns the matching SIP final response
			// to the upstream caller (busy→486, declined→603, etc.). When we
			// can't translate the reason, send empty body so VoiceBlender
			// picks its default rejection.
			inboundReason := mapDisconnectReason(e.Cdr.Reason)
			log.Warn("outbound failed pre-connect — hanging up inbound",
				"leg_id", outbound.ID,
				"outbound_reason", e.Cdr.Reason,
				"inbound_reason", inboundReason,
			)
			_, _ = inbound.Hangup(ctx, voiceblender.DeleteLegRequest{Reason: inboundReason})
			return
		}
	}
}

// waitForTeardown propagates a hangup on either side to the other and waits
// until both legs are gone before returning. This matters because the deferred
// room.Delete in bridge() will tear down any leg still in the room — if we
// returned right after issuing the second-side Hangup (which is async), the
// room delete would race the hangup and cause a duplicate BYE.
func waitForTeardown(ctx context.Context, log *slog.Logger, inbound, outbound *voiceblender.Leg) {
	inboundDisc := inbound.Subscribe(voiceblender.EventLegDisconnected)
	defer inboundDisc.Close()
	outboundDisc := outbound.Subscribe(voiceblender.EventLegDisconnected)
	defer outboundDisc.Close()

	hangup := context.Background()
	var waitFor *voiceblender.Subscription

	select {
	case <-inboundDisc.Events():
		log.Info("inbound hung up — hanging up outbound", "leg_id", inbound.ID)
		_, _ = outbound.Hangup(hangup, voiceblender.DeleteLegRequest{})
		waitFor = outboundDisc
	case <-outboundDisc.Events():
		log.Info("outbound hung up — hanging up inbound", "leg_id", outbound.ID)
		_, _ = inbound.Hangup(hangup, voiceblender.DeleteLegRequest{})
		waitFor = inboundDisc
	case <-ctx.Done():
		return
	}

	// Block until the second side's leg.disconnected fires too, so by the
	// time the deferred room.Delete in bridge() runs both legs are gone.
	select {
	case <-waitFor.Events():
	case <-ctx.Done():
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// mapDisconnectReason translates the outbound leg's leg.disconnected cdr.reason
// into a value the DeleteLegRequest API accepts so VoiceBlender sends a matching
// SIP final response on the inbound leg. Unknown / non-mappable reasons return
// an empty string, which falls back to VoiceBlender's default rejection.
//
// VoiceBlender's accepted reasons are: busy, declined, rejected, unavailable,
// not_found, forbidden, server_error.
func mapDisconnectReason(outboundReason string) string {
	switch outboundReason {
	case "busy":
		return "busy"
	case "declined":
		return "declined"
	case "unavailable", "ring_timeout", "timeout":
		return "unavailable"
	case "not_found":
		return "not_found"
	case "forbidden", "unauthorized":
		return "forbidden"
	case "service_unavailable", "not_acceptable",
		"invite_failed", "connect_failed",
		"ice_failure", "rtp_timeout", "session_expired":
		return "server_error"
	}
	// `sip_<code>` for unmapped numeric responses — translate by status class.
	if strings.HasPrefix(outboundReason, "sip_") {
		switch outboundReason {
		case "sip_486":
			return "busy"
		case "sip_603":
			return "declined"
		case "sip_480", "sip_408":
			return "unavailable"
		case "sip_404", "sip_410":
			return "not_found"
		case "sip_401", "sip_403", "sip_407":
			return "forbidden"
		}
		// Fall-through: any other 4xx/5xx → server_error, except 487 which is
		// caller-initiated cancel and has no useful mapping.
		if strings.HasPrefix(outboundReason, "sip_5") {
			return "server_error"
		}
	}
	return ""
}

// sipUser extracts the user/number part from VoiceBlender's `to` value, which
// may arrive as a full SIP URI ("sip:USER@HOST:PORT;params" or "sips:USER@HOST")
// or as a bare user/number ("+14155551234"). Strips uri-parameters/headers
// defensively.
func sipUser(uri string) string {
	s := strings.TrimPrefix(uri, "sip:")
	s = strings.TrimPrefix(s, "sips:")
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[:at]
	}
	for _, sep := range []string{";", "?"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	return s
}
