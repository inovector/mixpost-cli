package mixpost

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// APIError is a non-2xx response from the Mixpost API.
type APIError struct {
	Status  int                 `json:"status"`
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors,omitempty"`
	Method  string              `json:"method,omitempty"`
	URL     string              `json:"url,omitempty"`
	Body    []byte              `json:"-"`

	// retryAfter carries the Retry-After header, when the server sent one.
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %d", e.Status)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	for _, line := range e.ValidationLines() {
		fmt.Fprintf(&b, "\n  - %s", line)
	}
	return b.String()
}

// ValidationLines renders the Laravel validation bag as "field: message" lines,
// in a stable order so output can be diffed.
func (e *APIError) ValidationLines() []string {
	if len(e.Errors) == 0 {
		return nil
	}
	fields := make([]string, 0, len(e.Errors))
	for f := range e.Errors {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	lines := make([]string, 0, len(fields))
	for _, f := range fields {
		for _, msg := range e.Errors[f] {
			lines = append(lines, f+": "+msg)
		}
	}
	return lines
}

// parseAPIError builds an APIError from a response body, falling back to the
// raw payload when it is not the JSON envelope Mixpost normally returns.
func parseAPIError(status int, method, url string, body []byte) *APIError {
	e := &APIError{Status: status, Method: method, URL: url, Body: body}

	var payload struct {
		Message string          `json:"message"`
		Error   string          `json:"error"`
		Errors  json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		e.Message = payload.Message
		if e.Message == "" {
			e.Message = payload.Error
		}
		if len(payload.Errors) > 0 {
			e.Errors = decodeValidationBag(payload.Errors)
		}
	}
	if e.Message == "" {
		if trimmed := strings.TrimSpace(string(body)); trimmed != "" && len(trimmed) <= 400 {
			e.Message = trimmed
		} else {
			e.Message = statusText(status)
		}
	}
	return e
}

// decodeValidationBag accepts both {"field":["msg"]} and {"field":"msg"}.
func decodeValidationBag(raw json.RawMessage) map[string][]string {
	var multi map[string][]string
	if err := json.Unmarshal(raw, &multi); err == nil {
		return multi
	}
	var single map[string]string
	if err := json.Unmarshal(raw, &single); err == nil {
		out := make(map[string][]string, len(single))
		for k, v := range single {
			out[k] = []string{v}
		}
		return out
	}
	return nil
}

func statusText(status int) string {
	switch status {
	case 401:
		return "Unauthenticated."
	case 403:
		return "Access forbidden."
	case 404:
		return "Not found."
	case 422:
		return "Validation errors"
	case 429:
		return "Too many requests."
	}
	if status >= 500 {
		return "Server error."
	}
	return "Request failed."
}
