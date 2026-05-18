// Command dialer originates a configurable number of outbound SIP calls
// through an SBC at a configurable rate, with Answering Machine Detection
// enabled, and applies a simple post-answer policy per call:
//
//   - If AMD classifies the answerer as "machine" → hang up immediately.
//   - For any other result ("human", "no_speech", "not_sure") → keep the
//     call up for HOLD_AFTER_AMD and then hang up.
//
// From and To phone numbers are randomly generated per call. The To URI is
// built as sip:<random>@SBC_HOST so the SBC sees a freshly-generated dialed
// number every time.
//
// Configuration (env):
//
//	VOICEBLENDER_URL  default http://localhost:8080/v1
//	SBC_HOST          default sbc:5060 — host[:port] of the SBC
//	TOTAL_CALLS       default 1 — total number of calls to dispatch
//	RATE_PER_SECOND   default 1 — max new calls started per second
//	HOLD_AFTER_AMD    default 30s — duration to keep a non-machine call up
//
// Calls run concurrently: the dispatcher emits up to RATE_PER_SECOND new
// calls per second until TOTAL_CALLS have been started, then waits for all
// of them to finish. The summary is logged at exit.
//
// Like the sbc example, this demo uses the WebSocket VSI feed for events,
// so no HTTP webhook server is required.
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	voiceblender "github.com/VoiceBlender/voice-go"
)

const amdResultMachine = "machine"

// stats aggregates per-call outcomes across all goroutines.
type stats struct {
	dispatched    atomic.Int64
	connected     atomic.Int64
	failedConnect atomic.Int64
	machine       atomic.Int64
	nonMachine    atomic.Int64
	noAMD         atomic.Int64
	errors        atomic.Int64
}

func (s *stats) logFields() []any {
	return []any{
		"dispatched", s.dispatched.Load(),
		"connected", s.connected.Load(),
		"failed_connect", s.failedConnect.Load(),
		"machine", s.machine.Load(),
		"non_machine", s.nonMachine.Load(),
		"no_amd", s.noAMD.Load(),
		"errors", s.errors.Load(),
	}
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	baseURL := envOr("VOICEBLENDER_URL", "http://localhost:8080/v1")
	sbcHost := envOr("SBC_HOST", "sbc:5060")
	totalCalls := mustParsePositiveInt(envOr("TOTAL_CALLS", "1"), "TOTAL_CALLS")
	ratePerSecond := mustParsePositiveInt(envOr("RATE_PER_SECOND", "1"), "RATE_PER_SECOND")
	holdAfterAMD := mustParseDuration(envOr("HOLD_AFTER_AMD", "30s"))

	client := voiceblender.New(voiceblender.WithBaseURL(baseURL))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Pump events into the client's hub so Subscribe works.
	go func() {
		if err := client.RunEventStream(ctx); err != nil && ctx.Err() == nil {
			log.Error("event stream", "error", err)
			cancel()
		}
	}()

	log.Info("starting",
		"total_calls", totalCalls,
		"rate_per_second", ratePerSecond,
		"hold_after_amd", holdAfterAMD,
		"sbc_host", sbcHost,
	)

	var s stats
	dispatchCalls(ctx, log, client, sbcHost, holdAfterAMD, totalCalls, ratePerSecond, &s)

	log.Info("done", s.logFields()...)
}

// dispatchCalls launches totalCalls calls at no more than ratePerSecond starts
// per second, waits for them all to finish, and updates s.
func dispatchCalls(ctx context.Context, log *slog.Logger, client *voiceblender.Client, sbcHost string, holdAfterAMD time.Duration, totalCalls, ratePerSecond int, s *stats) {
	tickInterval := time.Second / time.Duration(ratePerSecond)
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	var wg sync.WaitGroup

	for i := 1; i <= totalCalls; i++ {
		select {
		case <-ctx.Done():
			log.Warn("interrupted before dispatch complete",
				"dispatched", i-1,
				"total", totalCalls,
			)
			wg.Wait()
			return
		case <-ticker.C:
			wg.Add(1)
			go func(callID int) {
				defer wg.Done()
				runOneCall(ctx, log, client, sbcHost, holdAfterAMD, callID, s)
			}(i)
		}
	}
	wg.Wait()
}

// runOneCall executes one outbound call from CreateLeg through hangup. All
// outcomes are recorded on s; per-call logging is attached via call_id.
func runOneCall(ctx context.Context, log *slog.Logger, client *voiceblender.Client, sbcHost string, holdAfterAMD time.Duration, callID int, s *stats) {
	s.dispatched.Add(1)

	callLog := log.With("call_id", callID)

	from := randomNumber()
	to := randomNumber()
	toURI := "sip:" + to + "@" + sbcHost

	leg, err := client.CreateLeg(ctx, voiceblender.CreateLegRequest{
		Type: "sip",
		From: from,
		To:   toURI,
		// Force PCMA only in the INVITE so the bridge stays PCMA end-to-end.
		Codecs: []string{"PCMA"},
		// Empty AMDParams enables AMD with server-side defaults.
		AMD: &voiceblender.AMDParams{},
	})
	if err != nil {
		s.errors.Add(1)
		callLog.Error("create leg", "error", err)
		return
	}
	callLog = callLog.With("leg_id", leg.ID)
	callLog.Info("dialing", "from", from, "to", toURI)

	// Race note: the leg.connected / amd.result events could in theory fire
	// between CreateLeg returning and Subscribe registering, but SIP connect
	// takes seconds and AMD only runs after answer — the gap is many orders
	// of magnitude wider than the few µs between the two calls here.
	sub := leg.Subscribe(
		voiceblender.EventLegConnected,
		voiceblender.EventLegDisconnected,
		voiceblender.EventAMDResult,
	)
	defer sub.Close()

	if err := waitForConnect(ctx, sub); err != nil {
		s.failedConnect.Add(1)
		callLog.Warn("connect failed", "error", err)
		return
	}
	s.connected.Add(1)
	callLog.Info("connected — waiting for AMD")

	amd, err := waitForAMD(ctx, sub)
	if err != nil {
		s.noAMD.Add(1)
		callLog.Warn("amd wait", "error", err)
		return
	}
	callLog.Info("amd",
		"result", amd.Result,
		"initial_silence_ms", amd.InitialSilenceMs,
		"greeting_ms", amd.GreetingDurationMs,
		"analysis_ms", amd.TotalAnalysisMs,
	)

	if amd.Result == amdResultMachine {
		s.machine.Add(1)
		callLog.Info("machine detected — hanging up")
		_, _ = leg.Hangup(context.Background(), voiceblender.DeleteLegRequest{})
		return
	}

	s.nonMachine.Add(1)
	callLog.Info("non-machine — holding", "duration", holdAfterAMD)
	holdAndHangup(ctx, callLog, leg, sub, holdAfterAMD)
}

// waitForConnect blocks until leg.connected is observed, or returns the
// disconnect reason if the call ends first.
func waitForConnect(ctx context.Context, sub *voiceblender.Subscription) error {
	for {
		ev, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		switch e := ev.(type) {
		case *voiceblender.LegConnectedEvent:
			return nil
		case *voiceblender.LegDisconnectedEvent:
			return fmt.Errorf("disconnected before connect: %s", e.Cdr.Reason)
		}
	}
}

// waitForAMD blocks until amd.result is observed, or returns an error if the
// call disconnects before AMD fires.
func waitForAMD(ctx context.Context, sub *voiceblender.Subscription) (*voiceblender.AMDResultEvent, error) {
	for {
		ev, err := sub.Next(ctx)
		if err != nil {
			return nil, err
		}
		switch e := ev.(type) {
		case *voiceblender.AMDResultEvent:
			return e, nil
		case *voiceblender.LegDisconnectedEvent:
			return nil, fmt.Errorf("disconnected before AMD result: %s", e.Cdr.Reason)
		}
	}
}

// holdAndHangup keeps the call up for d, then hangs it up. If the remote
// disconnects first, it returns immediately. If ctx is cancelled the call is
// hung up via a fresh context so cleanup completes during shutdown.
func holdAndHangup(ctx context.Context, log *slog.Logger, leg *voiceblender.Leg, sub *voiceblender.Subscription, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			log.Info("hold elapsed — hanging up")
			_, _ = leg.Hangup(context.Background(), voiceblender.DeleteLegRequest{})
			return
		case ev := <-sub.Events():
			if e, ok := ev.(*voiceblender.LegDisconnectedEvent); ok {
				log.Info("remote disconnected during hold", "reason", e.Cdr.Reason)
				return
			}
		case <-ctx.Done():
			log.Info("interrupted — hanging up")
			_, _ = leg.Hangup(context.Background(), voiceblender.DeleteLegRequest{})
			return
		}
	}
}

// randomNumber returns a 10-digit E.164-style North-American number with a
// "+1" prefix. Sufficient entropy for a per-run identifier; not intended as
// a real allocation.
func randomNumber() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	n := binary.BigEndian.Uint64(b[:]) % 10_000_000_000
	return fmt.Sprintf("+1%010d", n)
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

func mustParsePositiveInt(s, name string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		panic(errors.New(name + " must be a positive integer, got " + s))
	}
	return n
}
