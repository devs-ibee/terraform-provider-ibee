package provider

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"strings"
)

type computeStringValidator struct{ values []string }

func computeOneOf(values ...string) validator.String { return computeStringValidator{values} }
func computeNonEmpty() validator.String              { return computeStringValidator{} }
func (v computeStringValidator) Description(context.Context) string {
	if len(v.values) == 0 {
		return "must not be blank"
	}
	return "must be one of: " + strings.Join(v.values, ", ")
}
func (v computeStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v computeStringValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	if len(v.values) == 0 && strings.TrimSpace(s) != "" {
		return
	}
	for _, allowed := range v.values {
		if s == allowed {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", v.Description(ctx))
}

type computeIntValidator struct{ min, max int64 }

func (v computeIntValidator) Description(context.Context) string {
	return fmt.Sprintf("must be between %d and %d", v.min, v.max)
}
func (v computeIntValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v computeIntValidator) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	n := req.ConfigValue.ValueInt64()
	if n < v.min || n > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", v.Description(ctx))
	}
}

type computeCurrencyValidator struct{}

func (computeCurrencyValidator) Description(context.Context) string {
	return "must be a three-letter uppercase currency code"
}
func (v computeCurrencyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v computeCurrencyValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	valid := len(s) == 3
	for _, ch := range s {
		if ch < 'A' || ch > 'Z' {
			valid = false
		}
	}
	if !valid {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid currency", v.Description(ctx))
	}
}
