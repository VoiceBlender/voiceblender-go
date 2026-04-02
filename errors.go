package voiceblender

import "fmt"

// APIError is returned when the server responds with a 4xx or 5xx status code.
type APIError struct {
	StatusCode int    `json:"-"`
	InstanceID string `json:"instance_id"`
	Message    string `json:"error"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("voiceblender: HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("voiceblender: HTTP %d", e.StatusCode)
}

// IsNotFound reports whether err is a 404 API error.
func IsNotFound(err error) bool {
	e, ok := err.(*APIError)
	return ok && e.StatusCode == 404
}

// IsConflict reports whether err is a 409 API error.
func IsConflict(err error) bool {
	e, ok := err.(*APIError)
	return ok && e.StatusCode == 409
}

// IsBadRequest reports whether err is a 400 API error.
func IsBadRequest(err error) bool {
	e, ok := err.(*APIError)
	return ok && e.StatusCode == 400
}
