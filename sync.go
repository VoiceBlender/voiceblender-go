package voiceblender

import (
	"context"
	"fmt"
)

// This file provides Sync variants of methods that have a Start/Stop pair.
// A *Sync method issues the Start call, then blocks until the corresponding
// completion event arrives (or ctx cancels, or an error event arrives).
//
// All Sync methods require an event source feeding the client's hub:
//   - WebSocket VSI: go client.RunEventStream(ctx)
//   - Webhook:       client.DeliverEvent(ev) for each parsed event
// Without one of those, every Sync call blocks until ctx cancels.

// waitFor blocks until check reports done=true (returning the accompanying
// err), or ctx cancels (returning ctx.Err()). Events that don't satisfy the
// check are silently dropped and the loop keeps going.
func waitFor(ctx context.Context, sub *subscription, check func(ev interface{}) (done bool, err error)) error {
	for {
		select {
		case ev := <-sub.ch:
			if done, err := check(ev); done {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ── PlayTTS ──────────────────────────────────────────────────────────────────

// PlayTTSSync issues a TTS prompt and blocks until tts.finished (or tts.error)
// arrives for the response's TTSID, or ctx cancels.
func (l *Leg) PlayTTSSync(ctx context.Context, req TTSRequest) (*TTSResponse, error) {
	sub := l.client.events.subscribe(matchLegTTS(l.ID))
	defer l.client.events.unsubscribe(sub)
	resp, err := l.PlayTTS(ctx, req)
	if err != nil {
		return resp, err
	}
	return resp, waitFor(ctx, sub, checkTTS(resp.TTSID))
}

// PlayTTSSync issues a TTS prompt in the room and blocks until completion.
func (r *Room) PlayTTSSync(ctx context.Context, req TTSRequest) (*TTSResponse, error) {
	sub := r.client.events.subscribe(matchRoomTTS(r.ID))
	defer r.client.events.unsubscribe(sub)
	resp, err := r.PlayTTS(ctx, req)
	if err != nil {
		return resp, err
	}
	return resp, waitFor(ctx, sub, checkTTS(resp.TTSID))
}

func matchLegTTS(legID string) func(interface{}) bool {
	return func(ev interface{}) bool {
		switch e := ev.(type) {
		case *TTSFinishedEvent:
			return e.LegID == legID
		case *TTSErrorEvent:
			return e.LegID == legID
		}
		return false
	}
}

func matchRoomTTS(roomID string) func(interface{}) bool {
	return func(ev interface{}) bool {
		switch e := ev.(type) {
		case *TTSFinishedEvent:
			return e.RoomID == roomID
		case *TTSErrorEvent:
			return e.RoomID == roomID
		}
		return false
	}
}

func checkTTS(ttsID string) func(interface{}) (bool, error) {
	return func(ev interface{}) (bool, error) {
		switch e := ev.(type) {
		case *TTSFinishedEvent:
			if e.TTSID == ttsID {
				return true, nil
			}
		case *TTSErrorEvent:
			if e.TTSID == ttsID {
				return true, fmt.Errorf("tts: %s", e.Error)
			}
		}
		return false, nil
	}
}

// ── Play (audio playback) ────────────────────────────────────────────────────

// PlaySync starts a playback on the leg and blocks until playback.finished
// (or playback.error) arrives for the response's PlaybackID, or ctx cancels.
func (l *Leg) PlaySync(ctx context.Context, req PlaybackRequest) (*PlaybackResponse, error) {
	sub := l.client.events.subscribe(matchLegPlayback(l.ID))
	defer l.client.events.unsubscribe(sub)
	resp, err := l.Play(ctx, req)
	if err != nil {
		return resp, err
	}
	return resp, waitFor(ctx, sub, checkPlayback(resp.PlaybackID))
}

// PlaySync starts a playback in the room and blocks until completion.
func (r *Room) PlaySync(ctx context.Context, req PlaybackRequest) (*PlaybackResponse, error) {
	sub := r.client.events.subscribe(matchRoomPlayback(r.ID))
	defer r.client.events.unsubscribe(sub)
	resp, err := r.Play(ctx, req)
	if err != nil {
		return resp, err
	}
	return resp, waitFor(ctx, sub, checkPlayback(resp.PlaybackID))
}

func matchLegPlayback(legID string) func(interface{}) bool {
	return func(ev interface{}) bool {
		switch e := ev.(type) {
		case *PlaybackFinishedEvent:
			return e.LegID == legID
		case *PlaybackErrorEvent:
			return e.LegID == legID
		}
		return false
	}
}

func matchRoomPlayback(roomID string) func(interface{}) bool {
	return func(ev interface{}) bool {
		switch e := ev.(type) {
		case *PlaybackFinishedEvent:
			return e.RoomID == roomID
		case *PlaybackErrorEvent:
			return e.RoomID == roomID
		}
		return false
	}
}

func checkPlayback(playbackID string) func(interface{}) (bool, error) {
	return func(ev interface{}) (bool, error) {
		switch e := ev.(type) {
		case *PlaybackFinishedEvent:
			if e.PlaybackID == playbackID {
				return true, nil
			}
		case *PlaybackErrorEvent:
			if e.PlaybackID == playbackID {
				return true, fmt.Errorf("playback: %s", e.Error)
			}
		}
		return false, nil
	}
}

// ── Record ───────────────────────────────────────────────────────────────────

// RecordSync starts a recording on the leg and blocks until recording.finished
// arrives, or ctx cancels. Recording typically ends when StopRecord is called
// or the leg disconnects, whichever comes first. Note: the spec doesn't carry
// a recording-ID on either the response or the event, so this matches by
// leg-scope only — only one recording per leg can be in flight.
func (l *Leg) RecordSync(ctx context.Context, req RecordingRequest) (*RecordingResponse, error) {
	sub := l.client.events.subscribe(func(ev interface{}) bool {
		e, ok := ev.(*RecordingFinishedEvent)
		return ok && e.LegID == l.ID
	})
	defer l.client.events.unsubscribe(sub)
	resp, err := l.Record(ctx, req)
	if err != nil {
		return resp, err
	}
	return resp, waitFor(ctx, sub, recordingDone)
}

// RecordSync starts a recording in the room and blocks until completion.
func (r *Room) RecordSync(ctx context.Context, req RecordingRequest) (*RecordingResponse, error) {
	sub := r.client.events.subscribe(func(ev interface{}) bool {
		e, ok := ev.(*RecordingFinishedEvent)
		return ok && e.RoomID == r.ID
	})
	defer r.client.events.unsubscribe(sub)
	resp, err := r.Record(ctx, req)
	if err != nil {
		return resp, err
	}
	return resp, waitFor(ctx, sub, recordingDone)
}

func recordingDone(ev interface{}) (bool, error) {
	if _, ok := ev.(*RecordingFinishedEvent); ok {
		return true, nil
	}
	return false, nil
}

// ── Agents ───────────────────────────────────────────────────────────────────
//
// AgentSync variants attach an agent and block until agent.disconnected fires,
// i.e. until the conversation ends. Note that, like RecordSync, the spec
// doesn't carry an agent/session ID on either the response or the event, so
// matching is by leg/room scope — only one agent per leg/room.

func (l *Leg) ElevenLabsAgentSync(ctx context.Context, req ElevenLabsAgentRequest) (*StatusResponse, error) {
	return runLegAgentSync(ctx, l, func() (*StatusResponse, error) { return l.ElevenLabsAgent(ctx, req) })
}

func (l *Leg) VAPIAgentSync(ctx context.Context, req VAPIAgentRequest) (*StatusResponse, error) {
	return runLegAgentSync(ctx, l, func() (*StatusResponse, error) { return l.VAPIAgent(ctx, req) })
}

func (l *Leg) PipecatAgentSync(ctx context.Context, req PipecatAgentRequest) (*StatusResponse, error) {
	return runLegAgentSync(ctx, l, func() (*StatusResponse, error) { return l.PipecatAgent(ctx, req) })
}

func (l *Leg) DeepgramAgentSync(ctx context.Context, req DeepgramAgentRequest) (*StatusResponse, error) {
	return runLegAgentSync(ctx, l, func() (*StatusResponse, error) { return l.DeepgramAgent(ctx, req) })
}

func (r *Room) ElevenLabsAgentSync(ctx context.Context, req ElevenLabsAgentRequest) (*StatusResponse, error) {
	return runRoomAgentSync(ctx, r, func() (*StatusResponse, error) { return r.ElevenLabsAgent(ctx, req) })
}

func (r *Room) VAPIAgentSync(ctx context.Context, req VAPIAgentRequest) (*StatusResponse, error) {
	return runRoomAgentSync(ctx, r, func() (*StatusResponse, error) { return r.VAPIAgent(ctx, req) })
}

func (r *Room) PipecatAgentSync(ctx context.Context, req PipecatAgentRequest) (*StatusResponse, error) {
	return runRoomAgentSync(ctx, r, func() (*StatusResponse, error) { return r.PipecatAgent(ctx, req) })
}

func (r *Room) DeepgramAgentSync(ctx context.Context, req DeepgramAgentRequest) (*StatusResponse, error) {
	return runRoomAgentSync(ctx, r, func() (*StatusResponse, error) { return r.DeepgramAgent(ctx, req) })
}

func runLegAgentSync(ctx context.Context, l *Leg, start func() (*StatusResponse, error)) (*StatusResponse, error) {
	sub := l.client.events.subscribe(func(ev interface{}) bool {
		e, ok := ev.(*AgentDisconnectedEvent)
		return ok && e.LegID == l.ID
	})
	defer l.client.events.unsubscribe(sub)
	resp, err := start()
	if err != nil {
		return resp, err
	}
	return resp, waitFor(ctx, sub, agentDone)
}

func runRoomAgentSync(ctx context.Context, r *Room, start func() (*StatusResponse, error)) (*StatusResponse, error) {
	sub := r.client.events.subscribe(func(ev interface{}) bool {
		e, ok := ev.(*AgentDisconnectedEvent)
		return ok && e.RoomID == r.ID
	})
	defer r.client.events.unsubscribe(sub)
	resp, err := start()
	if err != nil {
		return resp, err
	}
	return resp, waitFor(ctx, sub, agentDone)
}

func agentDone(ev interface{}) (bool, error) {
	if _, ok := ev.(*AgentDisconnectedEvent); ok {
		return true, nil
	}
	return false, nil
}

// Note on STT: STT/StopSTT have no completion event in the spec — stt.text
// fires continuously per transcript and there is no stt.finished. A "Sync"
// variant would block until ctx cancels with no natural unblock, so it's
// intentionally not provided. Use STT (async) and StopSTT instead.
