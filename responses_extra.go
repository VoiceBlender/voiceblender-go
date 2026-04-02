package voiceblender

// AddLegResponse is returned when a leg is added or moved to a room.
// The server returns either {status:"added"} or {status:"moved", from:..., to:...}.
type AddLegResponse struct {
	InstanceID string `json:"instance_id,omitempty"`
	Status     string `json:"status"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
}

// ICECandidatesResponse contains locally gathered ICE candidates for a WebRTC leg.
type ICECandidatesResponse struct {
	InstanceID string             `json:"instance_id,omitempty"`
	Candidates []ICECandidateInit `json:"candidates"`
	Done       bool               `json:"done"`
}

// WebRTCOfferResponse contains the SDP answer and leg ID returned by /webrtc/offer.
type WebRTCOfferResponse struct {
	InstanceID string `json:"instance_id,omitempty"`
	LegID      string `json:"leg_id"`
	SDP        string `json:"sdp"`
}

// PlaybackResponse is returned when audio playback is started on a leg or room.
type PlaybackResponse struct {
	InstanceID string `json:"instance_id,omitempty"`
	PlaybackID string `json:"playback_id"`
	Status     string `json:"status"`
}

// TTSResponse is returned when TTS playback is started on a leg or room.
type TTSResponse struct {
	InstanceID string `json:"instance_id,omitempty"`
	TTSID      string `json:"tts_id"`
	Status     string `json:"status"`
}

// RecordingResponse is returned when recording is started or stopped.
type RecordingResponse struct {
	InstanceID string `json:"instance_id,omitempty"`
	Status     string `json:"status"`
	File       string `json:"file"`
}
