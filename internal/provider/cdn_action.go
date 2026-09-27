package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type cdnAction struct {
	client *Client
	verify bool
}

func NewCDNPurgeAction() action.Action        { return &cdnAction{} }
func NewCDNVerifyDomainAction() action.Action { return &cdnAction{verify: true} }
func (a *cdnAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cdn_purge"
	if a.verify {
		resp.TypeName = req.ProviderTypeName + "_cdn_verify_domain"
	}
}
func (a *cdnAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	attrs := map[string]schema.Attribute{"distribution_id": schema.StringAttribute{Required: true}}
	description := "Explicitly purge CDN cache. Requires Terraform 1.14 or later. Invoke manually or through an explicit action trigger; ordinary refresh never purges. This action does not delete origin objects."
	if a.verify {
		attrs["domain"] = schema.StringAttribute{Required: true}
		description = "Ask the CDN service to verify a custom domain after configuring DNS. Requires Terraform 1.14 or later. Pending DNS/TLS is reported as pending, not ready."
	} else {
		attrs["mode"] = schema.StringAttribute{Required: true, Description: "url, hostname, tag, prefix, or all. Some modes require service entitlements."}
		for _, k := range []string{"paths", "hostnames", "tags", "prefixes"} {
			attrs[k] = schema.ListAttribute{Optional: true, ElementType: types.StringType}
		}
	}
	resp.Schema = schema.Schema{Description: description, Attributes: attrs}
}
func (a *cdnAction) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	a.client, ok = req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
}
func (a *cdnAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var id types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("distribution_id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if strings.TrimSpace(id.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid distribution ID", "distribution_id must be nonempty.")
		return
	}
	endpoint := "/cdn/distributions/" + url.PathEscape(id.ValueString())
	if a.verify {
		var domain types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("domain"), &domain)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if domain.ValueString() == "" || strings.ContainsAny(domain.ValueString(), "/\\ ?#") {
			resp.Diagnostics.AddError("Invalid domain", "Use the existing DNS hostname without URL components.")
			return
		}
		var result struct {
			Status string `json:"status"`
			Domain string `json:"domain"`
		}
		if err := a.client.do(ctx, http.MethodPost, endpoint+"/custom-domains/"+url.PathEscape(domain.ValueString())+"/verify", nil, &result); err != nil {
			resp.Diagnostics.AddError("Domain verification failed", err.Error())
			return
		}
		if result.Domain != domain.ValueString() {
			resp.Diagnostics.AddError("Invalid verification response", "The API returned a different domain.")
			return
		}
		switch result.Status {
		case "active":
			if resp.SendProgress != nil {
				resp.SendProgress(action.InvokeProgressEvent{Message: "Domain is active."})
			}
		case "pending_validation", "pending_tls":
			resp.Diagnostics.AddWarning("Domain is still pending", "The service reported "+result.Status+". Check DNS and invoke verification again after propagation.")
		case "failed":
			resp.Diagnostics.AddError("Domain verification failed", "The service returned failed status.")
		default:
			resp.Diagnostics.AddError("Invalid verification response", "The service did not return a recognized domain status.")
		}
		return
	}
	var mode types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("mode"), &mode)...)
	if resp.Diagnostics.HasError() {
		return
	}
	field := map[string]string{"url": "paths", "hostname": "hostnames", "tag": "tags", "prefix": "prefixes", "all": ""}
	selected, ok := field[mode.ValueString()]
	if !ok {
		resp.Diagnostics.AddError("Invalid purge mode", "Use url, hostname, tag, prefix, or all.")
		return
	}
	body := map[string]any{"mode": mode.ValueString()}
	for _, k := range []string{"paths", "hostnames", "tags", "prefixes"} {
		var list types.List
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(k), &list)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if list.IsNull() {
			if k == selected {
				resp.Diagnostics.AddError("Missing purge targets", k+" is required for this mode.")
				return
			}
			continue
		}
		if k != selected {
			resp.Diagnostics.AddError("Conflicting purge targets", k+" does not match the selected mode.")
			return
		}
		var values []string
		resp.Diagnostics.Append(list.ElementsAs(ctx, &values, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		max := 100
		if k == "paths" {
			max = 30
		}
		if len(values) < 1 || len(values) > max {
			resp.Diagnostics.AddError("Invalid purge targets", fmt.Sprintf("%s requires 1–%d entries.", k, max))
			return
		}
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				resp.Diagnostics.AddError("Invalid purge target", "Targets must be nonempty.")
				return
			}
		}
		body[k] = values
	}
	var result struct {
		Success *bool  `json:"success"`
		Mode    string `json:"mode"`
	}
	if err := a.client.do(ctx, http.MethodPost, endpoint+"/purge", body, &result); err != nil {
		resp.Diagnostics.AddError("CDN purge failed", err.Error())
		return
	}
	if result.Success == nil || !*result.Success || result.Mode != mode.ValueString() {
		resp.Diagnostics.AddError("CDN purge not confirmed", "The API did not confirm success for the requested purge mode.")
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "CDN service confirmed the purge request."})
	}
}
