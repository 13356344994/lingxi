package historydownload

import "fmt"

type SafeFetchErrorKind string

const (
	SafeTransportFailed SafeFetchErrorKind = "transport_failed"
	SafeHTTPRejected    SafeFetchErrorKind = "http_rejected"
	SafeInvalidJSON     SafeFetchErrorKind = "invalid_json_envelope"
	SafeAPIRejected     SafeFetchErrorKind = "api_rejected"
	SafeBodyReadFailed  SafeFetchErrorKind = "body_read_failed"
)

type safeFetchError struct {
	kind       SafeFetchErrorKind
	httpStatus int
	apiCode    string
}

func NewSafeFetchError(kind SafeFetchErrorKind, httpStatus int, apiCode string) error {
	switch kind {
	case SafeTransportFailed, SafeHTTPRejected, SafeInvalidJSON, SafeAPIRejected, SafeBodyReadFailed:
	default:
		kind = SafeTransportFailed
	}
	if httpStatus < 100 || httpStatus > 599 {
		httpStatus = 0
	}
	apiCode = numericAPICode(apiCode)
	return safeFetchError{kind: kind, httpStatus: httpStatus, apiCode: apiCode}
}

func (e safeFetchError) Error() string {
	switch {
	case e.httpStatus != 0 && e.apiCode != "":
		return fmt.Sprintf("okxreadonly: %s (http_status=%d api_code=%s)", e.kind, e.httpStatus, e.apiCode)
	case e.httpStatus != 0:
		return fmt.Sprintf("okxreadonly: %s (http_status=%d)", e.kind, e.httpStatus)
	case e.apiCode != "":
		return fmt.Sprintf("okxreadonly: %s (api_code=%s)", e.kind, e.apiCode)
	default:
		return "okxreadonly: " + string(e.kind)
	}
}

func numericAPICode(value string) string {
	if len(value) < 1 || len(value) > 8 {
		return ""
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return ""
		}
	}
	return value
}
