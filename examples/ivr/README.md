# Example: Company IVR

A multi-department IVR (Interactive Voice Response) that answers inbound calls, greets the caller with a TTS prompt, presents a DTMF menu, and routes the caller to a department room.

## Call flow

```
Inbound call
  └─ leg.ringing      → answer the call
  └─ leg.connected    → "Thank you for calling Acme Corp. Please hold…"
  └─ tts.finished     → main menu prompt
  └─ dtmf.received
       1 → Sales queue
       2 → Support queue
       3 → Billing queue
       0 → Operator queue
       9 → Repeat menu
       * → Goodbye → hang up
       ? → "Invalid option, please try again" (max 3 attempts, then goodbye)
  └─ leg.disconnected → cleanup
```

Once a caller is routed, they are added to the department's persistent room where agents can join to handle the call. Hold music is played in the room while they wait.

## Prerequisites

- A running [VoiceBlender](https://github.com/VoiceBlender/voiceblender) instance
- An [ElevenLabs](https://elevenlabs.io) API key for TTS prompts, unless already configured in VoiceBlender
- A publicly reachable URL for the webhook server (use [ngrok](https://ngrok.com) or similar for local development)

## Configuration

| Environment variable | Required | Default | Description |
|----------------------|----------|---------|-------------|
| `WEBHOOK_URL` | yes | — | Public URL VoiceBlender will POST events to (e.g. `https://abc123.ngrok.io/webhook`) |
| `ELEVENLABS_API_KEY` | no | — | ElevenLabs API key for TTS; omit if already configured in VoiceBlender |
| `VOICEBLENDER_URL` | no | `http://localhost:8080/v1` | VoiceBlender API base URL |
| `LISTEN_ADDR` | no | `:8090` | Address the webhook HTTP server listens on |
| `TTS_VOICE` | no | `Rachel` | ElevenLabs voice name |
| `COMPANY_NAME` | no | `Acme Corp` | Company name spoken in the greeting |

## Running

```bash
export WEBHOOK_URL=https://your-tunnel.example.com/webhook
export ELEVENLABS_API_KEY=your_key_here

cd examples/ivr
go run .
```

With ngrok:

```bash
ngrok http 8090
# copy the https forwarding URL, then:
export WEBHOOK_URL=https://<ngrok-id>.ngrok.io/webhook
export ELEVENLABS_API_KEY=your_key_here
go run .
```

VoiceBlender must be configured to send inbound SIP calls to the same instance. The IVR registers its webhook automatically on startup and creates the four department rooms (`sales`, `support`, `billing`, `operator`) if they do not already exist.

## Architecture

```
SIP carrier
    │  inbound INVITE
    ▼
VoiceBlender
    │  webhook events (leg.ringing, leg.connected, dtmf.received, …)
    ▼
IVR server  (this program)
    │  REST API calls (AnswerLeg, TTSLeg, AddLegToRoom, …)
    ▼
VoiceBlender
```

Each active call is represented by a `call` struct that holds the current IVR state (`greeting → menu → routed/goodbye`). Webhook events are dispatched to goroutines so the HTTP handler returns immediately; all state transitions are protected by a per-call mutex.
