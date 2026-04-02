package voiceblender

import (
	"context"
	"net/http"
)

// WebRTCOffer submits a WebRTC SDP offer and returns an SDP answer.
func (c *Client) WebRTCOffer(ctx context.Context, req WebRTCOfferRequest) (*WebRTCOfferResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	var out WebRTCOfferResponse
	return &out, c.do(ctx, http.MethodPost, "/webrtc/offer", body, &out)
}
