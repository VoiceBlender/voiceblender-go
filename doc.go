// Package voiceblender provides a Go client for the VoiceBlender API.
//
// VoiceBlender bridges SIP and WebRTC voice calls with multi-party audio
// mixing, real-time speech-to-text, text-to-speech, AI agent integration,
// recording, and webhook-based event delivery.
//
// Usage:
//
//	c := voiceblender.New(voiceblender.WithBaseURL("http://localhost:8080/v1"))
//	leg, err := c.CreateLeg(ctx, voiceblender.CreateLegRequest{
//	    Type: "sip",
//	    URI:  "sip:alice@example.com",
//	})
package voiceblender
