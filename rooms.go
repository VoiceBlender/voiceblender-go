package voiceblender

import (
	"context"
	"net/http"
)

// CreateRoom creates a new conference room.
func (c *Client) CreateRoom(ctx context.Context, req CreateRoomRequest) (*Room, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out Room
	return &out, c.do(ctx, http.MethodPost, "/rooms", body, &out)
}

// ListRooms returns all active rooms.
func (c *Client) ListRooms(ctx context.Context) ([]Room, error) {
	var out []Room
	return out, c.do(ctx, http.MethodGet, "/rooms", nil, &out)
}

// GetRoom returns a single room by ID.
func (c *Client) GetRoom(ctx context.Context, id string) (*Room, error) {
	var out Room
	return &out, c.do(ctx, http.MethodGet, "/rooms/"+id, nil, &out)
}

// DeleteRoom deletes a room.
func (c *Client) DeleteRoom(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/rooms/"+id, nil, &out)
}

// AddLegToRoom adds a leg to a room, or moves it from its current room.
func (c *Client) AddLegToRoom(ctx context.Context, roomID string, req AddLegRequest) (*AddLegResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out AddLegResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+roomID+"/legs", body, &out)
}

// RemoveLegFromRoom removes a leg from a room.
func (c *Client) RemoveLegFromRoom(ctx context.Context, roomID, legID string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/rooms/"+roomID+"/legs/"+legID, nil, &out)
}

// PlayRoom plays audio to all participants in a room.
func (c *Client) PlayRoom(ctx context.Context, id string, req PlaybackRequest) (*PlaybackResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out PlaybackResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+id+"/play", body, &out)
}

// StopPlayRoom stops an active playback in a room.
func (c *Client) StopPlayRoom(ctx context.Context, id, playbackID string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/rooms/"+id+"/play/"+playbackID, nil, &out)
}

// VolumePlayRoom adjusts the volume of an active playback in a room.
func (c *Client) VolumePlayRoom(ctx context.Context, id, playbackID string, req VolumeRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPatch, "/rooms/"+id+"/play/"+playbackID, body, &out)
}

// TTSRoom synthesizes speech and plays it to all participants in a room.
func (c *Client) TTSRoom(ctx context.Context, id string, req TTSRequest) (*TTSResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out TTSResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+id+"/tts", body, &out)
}

// RecordRoom starts recording a room.
func (c *Client) RecordRoom(ctx context.Context, id string, req RecordingRequest) (*RecordingResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out RecordingResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+id+"/record", body, &out)
}

// StopRecordRoom stops an active recording in a room.
func (c *Client) StopRecordRoom(ctx context.Context, id string) (*RecordingResponse, error) {
	var out RecordingResponse
	return &out, c.do(ctx, http.MethodDelete, "/rooms/"+id+"/record", nil, &out)
}

// STTRoom starts speech-to-text transcription for all participants in a room.
func (c *Client) STTRoom(ctx context.Context, id string, req STTRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+id+"/stt", body, &out)
}

// StopSTTRoom stops speech-to-text transcription in a room.
func (c *Client) StopSTTRoom(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/rooms/"+id+"/stt", nil, &out)
}

// DeepgramAgentRoom attaches a Deepgram AI agent to a room.
func (c *Client) DeepgramAgentRoom(ctx context.Context, id string, req DeepgramAgentRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+id+"/agent/deepgram", body, &out)
}

// ElevenLabsAgentRoom attaches an ElevenLabs AI agent to a room.
func (c *Client) ElevenLabsAgentRoom(ctx context.Context, id string, req ElevenLabsAgentRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+id+"/agent/elevenlabs", body, &out)
}

// VAPIAgentRoom attaches a VAPI AI agent to a room.
func (c *Client) VAPIAgentRoom(ctx context.Context, id string, req VAPIAgentRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+id+"/agent/vapi", body, &out)
}

// PipecatAgentRoom attaches a Pipecat AI agent to a room.
func (c *Client) PipecatAgentRoom(ctx context.Context, id string, req PipecatAgentRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+id+"/agent/pipecat", body, &out)
}

// AgentMessageRoom sends a message to the AI agent attached to a room.
func (c *Client) AgentMessageRoom(ctx context.Context, id string, req AgentMessageRequest) (*StatusResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out StatusResponse
	return &out, c.do(ctx, http.MethodPost, "/rooms/"+id+"/agent/message", body, &out)
}

// StopAgentRoom detaches an AI agent from a room.
func (c *Client) StopAgentRoom(ctx context.Context, id string) (*StatusResponse, error) {
	var out StatusResponse
	return &out, c.do(ctx, http.MethodDelete, "/rooms/"+id+"/agent", nil, &out)
}
