# voiceblender-go

Go client library for the [VoiceBlender](https://voiceblender.com) API.

VoiceBlender bridges SIP and WebRTC voice calls with multi-party audio mixing,
real-time speech-to-text, text-to-speech, AI agent integration, recording, and
webhook-based event delivery.

## Installation

```bash
go get github.com/VoiceBlender/voiceblender-go
```

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	voiceblender "github.com/VoiceBlender/voiceblender-go"
)

func main() {
	c := voiceblender.New(voiceblender.WithBaseURL("http://localhost:8080/v1"))

	leg, err := c.CreateLeg(context.Background(), voiceblender.CreateLegRequest{
		Type: voiceblender.LegTypeSIPOutbound,
		URI:  "sip:alice@example.com",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Created leg:", leg.ID)
}
```

## Features

### Legs

Manage individual voice participants (SIP inbound/outbound, WebRTC):

- **Lifecycle** -- create, answer, early media, hang up
- **Audio control** -- mute, hold, send DTMF
- **Playback** -- play audio URLs or tones with volume control
- **Recording** -- record to local storage or S3
- **Text-to-speech** -- synthesize speech with configurable voice/provider
- **Speech-to-text** -- real-time transcription with partial results
- **AI agents** -- connect Deepgram, ElevenLabs, VAPI, or Pipecat agents

### Rooms

Multi-party conference rooms with the same audio, playback, recording, speech,
and agent capabilities as legs, plus participant management (add/remove legs).

### WebRTC

SDP offer/answer exchange and ICE candidate management.

### Events (VSI)

Real-time event streaming over WebSocket with typed Go structs for all 30
event types, automatic ping/pong handling, and a `ParseEvent` dispatcher.

## Client options

```go
// Custom base URL (default: http://localhost:8080/v1)
voiceblender.WithBaseURL("https://api.example.com/v1")

// Custom HTTP client
voiceblender.WithHTTPClient(myClient)

// Custom timeout (default: 30s)
voiceblender.WithTimeout(10 * time.Second)
```

## Error handling

```go
_, err := c.GetLeg(ctx, "leg-123")
if voiceblender.IsNotFound(err) {
	// 404
}
if voiceblender.IsBadRequest(err) {
	// 400
}
if voiceblender.IsConflict(err) {
	// 409
}
```

## Playback

`PlaybackRequest` uses builder functions to ensure mutual exclusion between URL
and tone playback:

```go
// Play an audio file
c.PlayLeg(ctx, legID, voiceblender.PlayURL("https://example.com/audio.wav"))

// Play a tone
c.PlayLeg(ctx, legID, voiceblender.PlayTone("440", 5))
```

## Real-time events (VSI WebSocket)

The VoiceBlender Streaming Interface (VSI) delivers all events over a
WebSocket connection as typed Go structs. This is an alternative to HTTP
webhooks and is useful for long-running processes that need to react to events
in real time.

### Listening for events

```go
stream, err := c.Events(ctx)
if err != nil {
    log.Fatal(err)
}
defer stream.Close()

for {
    ev, err := stream.Next(ctx)
    if err != nil {
        log.Fatal(err)
    }

    switch e := ev.(type) {
    case *voiceblender.LegRingingEvent:
        fmt.Printf("Leg %s is ringing (from=%s)\n", e.LegID, e.From)

    case *voiceblender.LegConnectedEvent:
        fmt.Printf("Leg %s connected\n", e.LegID)

    case *voiceblender.LegDisconnectedEvent:
        fmt.Printf("Leg %s disconnected: %s (%.1fs)\n",
            e.LegID, e.Cdr.Reason, e.Cdr.DurationTotal)

    case *voiceblender.DTMFReceivedEvent:
        fmt.Printf("DTMF digit %s on leg %s\n", e.Digit, e.LegID)

    case *voiceblender.STTTextEvent:
        if e.IsFinal {
            fmt.Printf("[%s] %s\n", e.LegID, e.Text)
        }
    }
}
```

### Filtering events by app ID

By default the VSI stream delivers every event for the instance. Tag your legs
and rooms with an `AppID`, then pass `WithAppFilter` to have the *server* drop
everything else — the events never cross the wire:

```go
// Tag the legs your app owns. Every leg type accepts AppID, including WebRTC.
leg, _ := c.CreateLeg(ctx, voiceblender.CreateLegRequest{
    Type:  "sip",
    To:    "sip:alice@example.com",
    AppID: "my-app",
})

answer, _ := c.WebRTCOffer(ctx, voiceblender.WebRTCOfferRequest{
    SDP:   offerSDP,
    AppID: "my-app",
})

// Subscribe to just this app's events.
stream, err := c.Events(ctx, voiceblender.WithAppFilter("^my-app$"))
```

The filter is an RE2 regular expression matched against each event's `app_id`,
so `"^(my-app|my-other-app)$"` subscribes to several apps at once. It is
unanchored unless you anchor it: `"my-app"` also matches `"my-app-staging"`.

An untagged leg emits events with an empty `app_id`, and a non-empty filter
drops them — if you filter, tag every leg your app creates or it will silently
miss its own events.

For finer-grained logic than a regex can express, the `AppID` field is also
present on the events themselves, so you can still filter client-side:

```go
switch e := ev.(type) {
case *voiceblender.LegConnectedEvent:
    if e.AppID != "my-app" {
        continue
    }
    // handle event
}
```

### Call quality monitoring

The `LegDisconnectedEvent` includes CDR and RTP quality metrics:

```go
case *voiceblender.LegDisconnectedEvent:
    fmt.Printf("Call %s ended: reason=%s total=%.1fs answered=%.1fs\n",
        e.LegID, e.Cdr.Reason, e.Cdr.DurationTotal, e.Cdr.DurationAnswered)

    if e.Quality != nil {
        fmt.Printf("  MOS=%.2f loss=%d jitter=%.1fms\n",
            e.Quality.MosScore, e.Quality.RtpPacketsLost, e.Quality.RtpJitterMs)
    }
```

### Transfer tracking

Track the full lifecycle of a SIP REFER transfer:

```go
case *voiceblender.LegTransferInitiatedEvent:
    fmt.Printf("Transfer started: leg=%s target=%s kind=%s\n",
        e.LegID, e.Target, e.Kind)

case *voiceblender.LegTransferProgressEvent:
    fmt.Printf("Transfer progress: leg=%s status=%d %s\n",
        e.LegID, e.StatusCode, e.Reason)

case *voiceblender.LegTransferCompletedEvent:
    fmt.Printf("Transfer completed: leg=%s\n", e.LegID)

case *voiceblender.LegTransferFailedEvent:
    fmt.Printf("Transfer failed: leg=%s reason=%s error=%s\n",
        e.LegID, e.Reason, e.Error)
```

## Examples

See the [`examples/`](examples/) directory for complete working examples:

- **[IVR](examples/ivr/)** -- Interactive Voice Response system with menu
  navigation, department routing, hold music, TTS prompts, and Deepgram AI
  agent integration.

## Code generation

Models, request types, response types, and event types are generated from
`openapi.yaml`:

```bash
make generate   # regenerate from OpenAPI spec
make build      # build all packages
make vet        # run go vet and build
```

## License

See [LICENSE](LICENSE) for details.
