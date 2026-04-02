# Examples

Runnable examples for the [voice-go](../) client library.

Each example is a self-contained `main` package in its own directory. Unless noted otherwise, every example expects a running [VoiceBlender](https://github.com/VoiceBlender/voiceblender) instance reachable at `http://localhost:8080`.

## Examples

| Directory | Description |
|-----------|-------------|
| [ivr/](ivr/) | Company IVR — answers inbound calls, plays a TTS menu, and routes callers to department rooms via DTMF |

## Running an example

```bash
cd examples/<name>
go run .
```

Each example documents its own environment variables in its README and in the package comment at the top of `main.go`.
