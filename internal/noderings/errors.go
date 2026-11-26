package noderings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// gRPC status codes the API puts in error bodies.
const (
	codeNotFound         = 5
	codeAlreadyExists    = 6
	codePermissionDenied = 7
	codeUnauthenticated  = 16
)

// Error is an API error: {"code": <grpc code>, "message": "...", "details": [...]}.
type Error struct {
	HTTPStatus int
	Code       int
	Message    string
	Details    []ErrorDetail
}

// ErrorDetail is one ErrorInfo entry of an error's details.
type ErrorDetail struct {
	Type     string            `json:"@type"`
	Reason   string            `json:"reason"`
	Metadata map[string]string `json:"metadata"`
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.HTTPStatus)
	}
	return fmt.Sprintf("NodeRings API: %s (HTTP %d)", msg, e.HTTPStatus)
}

// IsQuota reports whether the error is an exhausted plan quota rather than a rate limit.
func (e *Error) IsQuota() bool {
	for _, d := range e.Details {
		if strings.HasSuffix(d.Type, "google.rpc.ErrorInfo") && strings.HasSuffix(d.Reason, "LIMIT_REACHED") {
			return true
		}
	}
	return false
}

func decodeError(status int, body []byte) *Error {
	e := &Error{HTTPStatus: status}
	var raw struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Details []json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(body, &raw); err == nil {
		e.Code, e.Message = raw.Code, raw.Message
		for _, d := range raw.Details {
			var detail ErrorDetail
			if json.Unmarshal(d, &detail) == nil {
				e.Details = append(e.Details, detail)
			}
		}
	} else {
		e.Message = strings.TrimSpace(string(body))
		if len(e.Message) > 300 {
			e.Message = e.Message[:300]
		}
	}
	if e.Code == 0 {
		switch status {
		case http.StatusNotFound:
			e.Code = codeNotFound
		case http.StatusConflict:
			e.Code = codeAlreadyExists
		case http.StatusForbidden:
			e.Code = codePermissionDenied
		case http.StatusUnauthorized:
			e.Code = codeUnauthenticated
		}
	}
	return e
}

func hasCode(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// IsNotFound reports whether err is a NotFound API error.
func IsNotFound(err error) bool { return hasCode(err, codeNotFound) }

// IsUnauthenticated reports whether the token was rejected.
func IsUnauthenticated(err error) bool { return hasCode(err, codeUnauthenticated) }
