package voiceblender

import (
	"context"
	"net/http"
)

// CreateLeg originates a new outbound SIP leg.
func (c *Client) CreateLeg(ctx context.Context, req CreateLegRequest) (*Leg, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out Leg
	return &out, c.do(ctx, http.MethodPost, "/legs", body, &out)
}

// ListLegs returns all active legs.
func (c *Client) ListLegs(ctx context.Context) ([]Leg, error) {
	var out []Leg
	return out, c.do(ctx, http.MethodGet, "/legs", nil, &out)
}

// GetLeg returns a single leg by ID.
func (c *Client) GetLeg(ctx context.Context, id string) (*Leg, error) {
	var out Leg
	return &out, c.do(ctx, http.MethodGet, "/legs/"+id, nil, &out)
}

// DeleteLeg hangs up and removes a leg.
func (c *Client) DeleteLeg(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/legs/"+id, nil, &out)
}

// AnswerLeg answers a ringing inbound SIP leg.
func (c *Client) AnswerLeg(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/answer", nil, &out)
}

// EarlyMediaLeg enables early media on a ringing inbound SIP leg.
func (c *Client) EarlyMediaLeg(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/early-media", nil, &out)
}

// MuteLeg mutes a leg.
func (c *Client) MuteLeg(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/mute", nil, &out)
}

// UnmuteLeg unmutes a leg.
func (c *Client) UnmuteLeg(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/legs/"+id+"/mute", nil, &out)
}

// HoldLeg puts a SIP leg on hold.
func (c *Client) HoldLeg(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/hold", nil, &out)
}

// UnholdLeg resumes a held SIP leg.
func (c *Client) UnholdLeg(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/legs/"+id+"/hold", nil, &out)
}

// SendDTMF sends DTMF digits to a leg.
func (c *Client) SendDTMF(ctx context.Context, id string, req DTMFRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/dtmf", body, &out)
}

// PlayLeg plays audio on a leg.
func (c *Client) PlayLeg(ctx context.Context, id string, req PlaybackRequest) (*PlaybackResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out PlaybackResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/play", body, &out)
}

// StopPlayLeg stops an active playback on a leg.
func (c *Client) StopPlayLeg(ctx context.Context, id, playbackID string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/legs/"+id+"/play/"+playbackID, nil, &out)
}

// VolumePlayLeg adjusts the volume of an active playback on a leg.
func (c *Client) VolumePlayLeg(ctx context.Context, id, playbackID string, req VolumeRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPatch, "/legs/"+id+"/play/"+playbackID, body, &out)
}

// TTSLeg synthesizes speech and plays it on a leg.
func (c *Client) TTSLeg(ctx context.Context, id string, req TTSRequest) (*TTSResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out TTSResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/tts", body, &out)
}

// RecordLeg starts recording a leg.
func (c *Client) RecordLeg(ctx context.Context, id string, req RecordingRequest) (*RecordingResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out RecordingResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/record", body, &out)
}

// StopRecordLeg stops an active recording on a leg.
func (c *Client) StopRecordLeg(ctx context.Context, id string) (*RecordingResponse, error) {
	var out RecordingResponse
	return &out, c.do(ctx, http.MethodDelete, "/legs/"+id+"/record", nil, &out)
}

// STTLeg starts speech-to-text transcription on a leg.
func (c *Client) STTLeg(ctx context.Context, id string, req STTRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/stt", body, &out)
}

// StopSTTLeg stops speech-to-text transcription on a leg.
func (c *Client) StopSTTLeg(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/legs/"+id+"/stt", nil, &out)
}

// DeepgramAgentLeg attaches a Deepgram AI agent to a leg.
func (c *Client) DeepgramAgentLeg(ctx context.Context, id string, req DeepgramAgentRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/agent/deepgram", body, &out)
}

// ElevenLabsAgentLeg attaches an ElevenLabs AI agent to a leg.
func (c *Client) ElevenLabsAgentLeg(ctx context.Context, id string, req ElevenLabsAgentRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/agent/elevenlabs", body, &out)
}

// VAPIAgentLeg attaches a VAPI AI agent to a leg.
func (c *Client) VAPIAgentLeg(ctx context.Context, id string, req VAPIAgentRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/agent/vapi", body, &out)
}

// PipecatAgentLeg attaches a Pipecat AI agent to a leg.
func (c *Client) PipecatAgentLeg(ctx context.Context, id string, req PipecatAgentRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/agent/pipecat", body, &out)
}

// AgentMessageLeg sends a message to the AI agent attached to a leg.
func (c *Client) AgentMessageLeg(ctx context.Context, id string, req AgentMessageRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+id+"/agent/message", body, &out)
}

// StopAgentLeg detaches an AI agent from a leg.
func (c *Client) StopAgentLeg(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/legs/"+id+"/agent", nil, &out)
}

// AddICECandidate submits a WebRTC ICE candidate for a leg.
func (c *Client) AddICECandidate(ctx context.Context, legID string, candidate ICECandidateInit) (*StatusResponse, error) {
	body, err := encodeJSON(candidate)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/legs/"+legID+"/ice-candidates", body, &out)
}

// GetICECandidates returns locally gathered ICE candidates for a WebRTC leg.
func (c *Client) GetICECandidates(ctx context.Context, legID string) (*ICECandidatesResponse, error) {
	var out ICECandidatesResponse
	return &out, c.do(ctx, http.MethodGet, "/legs/"+legID+"/ice-candidates", nil, &out)
}
