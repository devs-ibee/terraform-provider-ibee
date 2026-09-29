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
	code, reason, requiredScope := "", "", ""
	var gatewayCode string
	var nested map[string]json.RawMessage
	if json.Unmarshal(envelope["error"], &gatewayCode) == nil && gatewayCode != "" {
		code = field(envelope, "error")
		reason = field(envelope, "billing_reason")
		requiredScope = field(envelope, "required_scope")
	} else if json.Unmarshal(envelope["error"], &nested) == nil && nested != nil {
		code, reason = field(nested, "code"), field(nested, "reason")
		requiredScope = field(nested, "required_scope")
	} else if json.Unmarshal(envelope["detail"], &nested) == nil && nested != nil {
		for _, key := range []string{"code", "error_code", "error"} {
			if code = field(nested, key); code != "" {
				break
			}
		}
		reason, requiredScope = field(nested, "reason"), field(nested, "required_scope")
	} else {
		code, reason = field(envelope, "code"), field(envelope, "reason")
		requiredScope = field(envelope, "required_scope")
	}
	e.Code = errorIdentifier(code)
	e.Reason = errorIdentifier(reason)
	e.RequiredScope = errorIdentifier(requiredScope)
	e.BillingSKUCode = field(envelope, "billing_sku_code")
	e.AdmissionContextID = field(envelope, "admission_context_id")
	return e
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

// Only static, allowlisted messages are suitable for secret-resource diagnostics:
// a server may echo plaintext in any free-form error field, not just its body.
func (e *apiError) guidance() string {
	switch e.Code {
	case "organization_restricted":
		return "The organization is restricted. Resolve its access or billing restriction, then retry; this is not a missing resource."
	case "organization_suspended":
		return "The organization is suspended. Restore access through the authorized administrator or Billing team before retrying writes."
	case "key_revoked", "key_expired", "key_disabled", "key_inactive":
		return "The API token is inactive. Configure a valid token for this environment and workspace."
	case "workspace_not_allowed":
		return "The token does not allow this workspace. Check workspace_id and the token's workspace selection."
	case "workspace_id_required", "invalid_workspace_id":
		return "Configure a valid workspace_id for the API token."
	case "insufficient_scope":
		return "The token lacks the required permission. Use an appropriately scoped token; adding credits does not resolve a missing scope."
	case "organization_lifecycle_unavailable":
		return "Organization eligibility is temporarily unavailable. Check service health and retry later; authorization has not been granted."
	case "billing_denied":
		return "Billing denied this request. Review the organization's billing eligibility before retrying."
	}
	return ""
}
