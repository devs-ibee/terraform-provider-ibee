package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

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
	description := "Purges CDN cache (POST /cdn/distributions/{id}/purge). Requires Terraform 1.14 or later. Invoke manually or through an explicit action trigger; refresh never purges. Sends mode and the target lists that are set; the API decides what each mode needs. This action does not delete origin objects."
	if a.verify {
		attrs["domain"] = schema.StringAttribute{Required: true}
		description = "Asks the CDN service to verify a custom domain after its DNS is configured (POST .../custom-domains/{domain}/verify) and reports the status the API returns. Requires Terraform 1.14 or later."
	} else {
		attrs["mode"] = schema.StringAttribute{Required: true, Description: "url, hostname, tag, prefix or all."}
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
	endpoint := "/cdn/distributions/" + url.PathEscape(id.ValueString())
	if a.verify {
		var domain types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("domain"), &domain)...)
		if resp.Diagnostics.HasError() {
			return
		}
		var result struct {
			Status string `json:"status"`
		}
		if err := a.client.do(ctx, http.MethodPost, endpoint+"/custom-domains/"+url.PathEscape(domain.ValueString())+"/verify", nil, &result); err != nil {
			resp.Diagnostics.AddError("Domain verification failed", err.Error())
			return
		}
		if resp.SendProgress != nil {
			resp.SendProgress(action.InvokeProgressEvent{Message: "The API reports the domain as " + result.Status + "."})
		}
		return
	}
	var mode types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("mode"), &mode)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"mode": mode.ValueString()}
	for _, k := range []string{"paths", "hostnames", "tags", "prefixes"} {
		var list types.List
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(k), &list)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if list.IsNull() || list.IsUnknown() {
			continue
		}
		var values []string
		resp.Diagnostics.Append(list.ElementsAs(ctx, &values, false)...)
		body[k] = values
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if err := a.client.do(ctx, http.MethodPost, endpoint+"/purge", body, nil); err != nil {
		resp.Diagnostics.AddError("CDN purge failed", err.Error())
		return
	}
	if resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: "The API accepted the purge request."})
	}
}
