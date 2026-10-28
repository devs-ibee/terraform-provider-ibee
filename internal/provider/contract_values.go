package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// contractEnum checks a configured string against the values a contract enum
// lists. It is metadata from the contract, not a rule of the provider.
type contractEnum []string

func (v contractEnum) Description(context.Context) string {
	return "must be one of: " + strings.Join(v, ", ")
}
func (v contractEnum) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v contractEnum) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, allowed := range v {
		if req.ConfigValue.ValueString() == allowed {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", v.Description(ctx))
}
