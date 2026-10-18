package provider

import (
	"encoding/json"
	"strings"
	"unicode"
)

// parseAPIError accepts the gateway, product, and FastAPI error envelopes. The
// HTTP status remains authoritative: no code can turn a denial into a 404 or a
// successful response. A malformed body still yields an HTTP error.
func parseAPIError(status int, data []byte, requestID, token string) *apiError {
	redact := func(value string) string {
		if token != "" {
			return strings.ReplaceAll(value, token, "[REDACTED]")
		}
		return value
	}
	body := redact(string(data))
	if len(body) > 1024 {
		body = body[:1024]
	}
	e := &apiError{Status: status, Body: body, RequestID: safeErrorContext(redact(requestID), 256)}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil {
		return e
	}
	field := func(object map[string]json.RawMessage, key string) string {
		var value string
		if json.Unmarshal(object[key], &value) != nil {
			return ""
		}
		return safeErrorContext(redact(strings.TrimSpace(value)), 256)
	}
	text := func(object map[string]json.RawMessage, key string) string {
		var value string
		if json.Unmarshal(object[key], &value) != nil {
			return ""
		}
		return serverMessage(redact(value))
	}
	code, reason, requiredScope, message := "", "", "", ""
	var gatewayCode string
	var nested map[string]json.RawMessage
	if json.Unmarshal(envelope["error"], &gatewayCode) == nil && gatewayCode != "" {
		code = field(envelope, "error")
		reason = field(envelope, "billing_reason")
		e.billingReason = errorIdentifier(reason) != ""
		requiredScope = field(envelope, "required_scope")
		message = text(envelope, "message")
	} else if json.Unmarshal(envelope["error"], &nested) == nil && nested != nil {
		code, reason = field(nested, "code"), field(nested, "reason")
		requiredScope = field(nested, "required_scope")
		message = text(nested, "message")
	} else if json.Unmarshal(envelope["detail"], &nested) == nil && nested != nil {
		for _, key := range []string{"code", "error_code", "error"} {
			if code = field(nested, key); code != "" {
				break
			}
		}
		reason, requiredScope = field(nested, "reason"), field(nested, "required_scope")
		message = text(nested, "message")
	} else {
		code, reason = field(envelope, "code"), field(envelope, "reason")
		requiredScope = field(envelope, "required_scope")
		if message = text(envelope, "detail"); message == "" {
			message = text(envelope, "message")
		}
	}
	e.Code = errorIdentifier(code)
	e.Reason = errorIdentifier(reason)
	e.RequiredScope = errorIdentifier(requiredScope)
	e.Message = message
	e.BillingSKUCode = field(envelope, "billing_sku_code")
	e.AdmissionContextID = field(envelope, "admission_context_id")
	e.hasDetails = message != "" && hasExtraFields(envelope)
	return e
}

// billingAnswer reports whether the server answered with a billing or
// admission decision. Such answers are never retried for writes.
func (e *apiError) billingAnswer() bool {
	return e.billingReason || e.BillingSKUCode != "" || e.AdmissionContextID != "" ||
		strings.HasPrefix(e.Code, "billing_") || strings.HasPrefix(e.Code, "organization_") || strings.Contains(e.Code, "admission")
}

// The message is shown as the server wrote it, on one line and bounded.
func serverMessage(value string) string {
	value = strings.Join(strings.FieldsFunc(value, unicode.IsControl), " ")
	value = strings.TrimSpace(value)
	if len(value) > 1024 {
		value = value[:1024]
	}
	return value
}

// hasExtraFields reports whether the body carries data beyond code and message
// (for example a resize precheck's reasons), so the diagnostic includes the body.
func hasExtraFields(object map[string]json.RawMessage) bool {
	for key, raw := range object {
		switch key {
		case "code", "error_code", "message", "reason", "required_scope", "request_id",
			"billing_reason", "billing_sku_code", "billing_evaluated_at", "admission_context_id":
			continue
		case "error", "detail":
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) == nil && nested != nil && hasExtraFields(nested) {
				return true
			}
			continue
		}
		return true
	}
	return false
}

// Context is bounded and rejects controls, including terminal escape sequences.
// It is not used as a trusted authorization or routing input.
func safeErrorContext(value string, max int) string {
	if len(value) > max || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ""
	}
	return value
}

func errorIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == ':') {
			return ""
		}
	}
	return value
}
