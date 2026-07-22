package linkedin

import "fmt"

// VoyagerStatusError is returned when the Voyager API responds with a non-2xx
// HTTP status (including 401/403 auth failures). Consumers classify via
// errors.As and read .Status instead of regex-matching the error string.
type VoyagerStatusError struct {
	Endpoint string
	Status   int
}

func (e *VoyagerStatusError) Error() string {
	if e.Status == 401 || e.Status == 403 {
		return fmt.Sprintf("voyager auth failed: status %d (cookies may be expired)", e.Status)
	}
	return fmt.Sprintf("voyager %s: status %d", e.Endpoint, e.Status)
}

// VoyagerHTMLResponseError is returned when the Voyager API responds with 200
// but the body is HTML (an authwall/challenge page), indicating the session is
// expired or the IP is blocked. Distinct from VoyagerStatusError so consumers
// can branch on the failure mode without inspecting the message.
type VoyagerHTMLResponseError struct {
	Endpoint string
}

func (e *VoyagerHTMLResponseError) Error() string {
	return "voyager auth failed: HTML response (session expired or IP blocked)"
}
