// Command pstn is a tiny "fake PSTN" service useful for exercising upstream
// SBCs / dialers. For every inbound SIP call it:
//
//  1. Randomly chooses between sending 180 Ringing or 183 Session Progress
//     as the provisional response. With the default SIP_AUTO_RINGING=false
//     on the VoiceBlender side, VB only emits 100 Trying until something
//     explicitly asks for 180 or 183.
//  2. Holds the call in the resulting state (ringing or early_media) for
//     RING_DURATION (default 3s).
//  3. Answers the call (sends 200 OK).
//  4. Keeps the call up for ANSWERED_DURATION (default 15s).
//  5. Hangs up.
//
// At any point a caller-side disconnect is detected and the goroutine exits
// cleanly without a redundant hangup.
//
// Configuration (env):
//
//	VOICEBLENDER_URL   default http://localhost:8080/v1
//	RING_DURATION      default 3s  — how long to ring before answering
//	ANSWERED_DURATION  default 15s — how long to keep the call up after answer
//
// Like the other examples, pstn uses the WebSocket VSI feed for events;
// no HTTP webhook server is required.
package main

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"time"

	voiceblender "github.com/VoiceBlender/voice-go"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	baseURL := envOr("VOICEBLENDER_URL", "http://localhost:8080/v1")
	ringDuration := mustParseDuration(envOr("RING_DURATION", "3s"))
	answeredDuration := mustParseDuration(envOr("ANSWERED_DURATION", "15s"))

	client := voiceblender.New(voiceblender.WithBaseURL(baseURL))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	go func() {
		if err := client.RunEventStream(ctx); err != nil && ctx.Err() == nil {
			log.Error("event stream", "error", err)
			cancel()
		}
	}()

	ringings := client.Subscribe(voiceblender.EventLegRinging)
	defer ringings.Close()

	log.Info("pstn ready",
		"ring_duration", ringDuration,
		"answered_duration", answeredDuration,
	)

	for {
		ev, err := ringings.Next(ctx)
		if err != nil {
			return
		}
		ring := ev.(*voiceblender.LegRingingEvent)
		log.Info("ringing event",
			"leg_id", ring.LegID,
			"leg_type", ring.LegType,
			"from", ring.From,
			"to", ring.To,
		)
		if ring.LegType != string(voiceblender.LegTypeSIPInbound) {
			log.Warn("skipping non-inbound leg", "leg_id", ring.LegID, "leg_type", ring.LegType)
			continue
		}
		go handle(ctx, log, client, ring, ringDuration, answeredDuration)
	}
}

// handle drives one inbound call through ring → answer → hold → hangup.
// At every step a caller-initiated disconnect is observed and short-circuits
// the rest.
func handle(ctx context.Context, log *slog.Logger, client *voiceblender.Client, ring *voiceblender.LegRingingEvent, ringDur, answerDur time.Duration) {
	leg := client.Leg(ring.LegID)
	callLog := log.With("leg_id", leg.ID)

	// Watch disconnects throughout the call's lifetime.
	sub := leg.Subscribe(voiceblender.EventLegDisconnected)
	defer sub.Close()

	// 1. Randomly send 180 Ringing OR 183 Session Progress. VoiceBlender
	//    does not send a provisional response automatically when
	//    SIP_AUTO_RINGING=false (the default), so without this the upstream
	//    only sees 100 Trying until we answer or hang up.
	useEarlyMedia := rand.IntN(2) == 0
	if useEarlyMedia {
		callLog.Info("sending 183 session progress")
		if _, err := leg.EarlyMedia(ctx, voiceblender.EarlyMediaLegRequest{}); err != nil {
			callLog.Error("early-media", "error", err)
			_, _ = leg.Hangup(context.Background(), voiceblender.DeleteLegRequest{})
			return
		}
	} else {
		callLog.Info("sending 180 ringing")
		if _, err := leg.Ring(ctx); err != nil {
			callLog.Error("ring", "error", err)
			_, _ = leg.Hangup(context.Background(), voiceblender.DeleteLegRequest{})
			return
		}
	}

	// 2. Stay ringing for ringDur. Caller may hang up first.
	if disconnected := waitOrDisconnect(ctx, sub, time.NewTimer(ringDur)); disconnected != nil {
		callLog.Info("caller hung up while ringing", "reason", disconnected.Cdr.Reason)
		return
	}
	if ctx.Err() != nil {
		_, _ = leg.Hangup(context.Background(), voiceblender.DeleteLegRequest{})
		return
	}

	// 3. Answer. Force PCMA on the 200 OK SDP — fails if the offer didn't
	//    include PCMA.
	callLog.Info("answering")
	if _, err := leg.Answer(ctx, voiceblender.AnswerLegRequest{Codec: "PCMA"}); err != nil {
		callLog.Error("answer", "error", err)
		return
	}

	// 4. Hold for answerDur, then 5. hang up. Caller-side disconnect ends early.
	if disconnected := waitOrDisconnect(ctx, sub, time.NewTimer(answerDur)); disconnected != nil {
		callLog.Info("caller hung up", "reason", disconnected.Cdr.Reason)
		return
	}
	callLog.Info("answered duration elapsed — hanging up")
	_, _ = leg.Hangup(context.Background(), voiceblender.DeleteLegRequest{})
}

// waitOrDisconnect blocks until either the timer fires (returns nil), a
// LegDisconnectedEvent arrives on sub (returns the event), or ctx cancels
// (returns nil; caller checks ctx.Err()). The timer is always Stopped.
func waitOrDisconnect(ctx context.Context, sub *voiceblender.Subscription, timer *time.Timer) *voiceblender.LegDisconnectedEvent {
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return nil
		case ev := <-sub.Events():
			if d, ok := ev.(*voiceblender.LegDisconnectedEvent); ok {
				return d
			}
			// Other events (none subscribed today) — keep waiting.
		case <-ctx.Done():
			return nil
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustParseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		panic(errors.New("invalid duration: " + s))
	}
	return d
}
