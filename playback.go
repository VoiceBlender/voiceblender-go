package voiceblender

import "encoding/json"

// PlaybackRequest plays audio on a leg or room.
// Use PlayURL or PlayTone constructors — url and tone are mutually exclusive.
type PlaybackRequest struct {
	url      string
	mimeType string
	tone     string
	Repeat   int `json:"repeat,omitempty"`
	Volume   int `json:"volume,omitempty"`
}

// PlayURL returns a PlaybackRequest that streams audio from url.
func PlayURL(url, mimeType string) PlaybackRequest {
	return PlaybackRequest{url: url, mimeType: mimeType}
}

// PlayTone returns a PlaybackRequest that generates a named telephone tone.
// Format: {country}_{type} or bare {type} (defaults to US).
// Examples: us_ringback, gb_busy, dial.
func PlayTone(tone string) PlaybackRequest {
	return PlaybackRequest{tone: tone}
}

// MarshalJSON encodes PlaybackRequest, choosing url or tone as appropriate.
func (p PlaybackRequest) MarshalJSON() ([]byte, error) {
	type wire struct {
		URL      string `json:"url,omitempty"`
		MimeType string `json:"mime_type,omitempty"`
		Tone     string `json:"tone,omitempty"`
		Repeat   int    `json:"repeat,omitempty"`
		Volume   int    `json:"volume,omitempty"`
	}
	return json.Marshal(wire{
		URL:      p.url,
		MimeType: p.mimeType,
		Tone:     p.tone,
		Repeat:   p.Repeat,
		Volume:   p.Volume,
	})
}
