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

## Examples

See the [`examples/`](examples/) directory for complete working examples:

- **[IVR](examples/ivr/)** -- Interactive Voice Response system with menu
  navigation, department routing, hold music, TTS prompts, and Deepgram AI
  agent integration.

## Code generation

Models, request types, and response types are generated from `openapi.yaml`:

```bash
make generate   # regenerate from OpenAPI spec
make build      # build all packages
make vet        # run go vet and build
```

## License

See [LICENSE](LICENSE) for details.
