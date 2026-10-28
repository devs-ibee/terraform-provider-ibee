package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*s3CredentialResource)(nil)
	_ resource.ResourceWithConfigure   = (*s3CredentialResource)(nil)
	_ resource.ResourceWithImportState = (*s3CredentialResource)(nil)
)

type s3CredentialResource struct{ client *Client }

func NewS3CredentialResource() resource.Resource { return &s3CredentialResource{} }

type s3CredentialResourceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	PermissionType  types.String `tfsdk:"permission_type"`
	BucketScope     types.String `tfsdk:"bucket_scope"`
	AllowedBuckets  types.Set    `tfsdk:"allowed_buckets"`
	SecretAccessKey types.String `tfsdk:"secret_access_key"`
	Status          types.String `tfsdk:"status"`
}

type s3CredentialAPI struct {
	AccessKeyID     string    `json:"access_key_id"`
	SecretAccessKey string    `json:"secret_access_key"`
	Name            string    `json:"name"`
	PermissionType  string    `json:"permission_type"`
	BucketScope     string    `json:"bucket_scope"`
	AllowedBuckets  *[]string `json:"allowed_buckets"`
	Status          string    `json:"status"`
}

func (r *s3CredentialResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_credential"
}
func (r *s3CredentialResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{Description: "An S3 access key pair. The API has no update operation, so any input change replaces the key. Settings that are not configured are not sent and the API's defaults apply. The secret_access_key is returned once, at creation, and kept in Terraform state (sensitive, not encrypted by the provider; use an encrypted state backend); imports leave it null. Destroy revokes the key (POST .../revoke) and then deletes it (DELETE). Import using the access key ID.", Attributes: map[string]schema.Attribute{
		"id":                schema.StringAttribute{Computed: true, Description: "S3 access key ID; also used for import.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"name":              schema.StringAttribute{Optional: true, Computed: true, Description: "Display name of the key pair.", PlanModifiers: replace},
		"permission_type":   schema.StringAttribute{Optional: true, Computed: true, Description: "admin_rw, admin_ro, object_rw or object_ro.", PlanModifiers: replace},
		"bucket_scope":      schema.StringAttribute{Optional: true, Computed: true, Description: "all or specific.", PlanModifiers: replace},
		"allowed_buckets":   schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Description: "Bucket names the key may use when bucket_scope is specific.", PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown(), setplanmodifier.RequiresReplace()}},
		"secret_access_key": schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, Description: "Secret key, returned once at creation and kept in state. Null after import."},
		"status":            schema.StringAttribute{Computed: true, Description: "active or revoked."},
	}}
}

func (r *s3CredentialResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.client, ok = req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
}

func s3CredentialPath(id string) string { return "/object-storage/credentials/" + url.PathEscape(id) }

// apply records the fields the API returned; omitted ones keep their value.
func (out s3CredentialAPI) apply(ctx context.Context, state *s3CredentialResourceModel) {
	for _, field := range []struct {
		target *types.String
		value  string
	}{{&state.ID, out.AccessKeyID}, {&state.Name, out.Name}, {&state.PermissionType, out.PermissionType}, {&state.BucketScope, out.BucketScope}, {&state.Status, out.Status}} {
		if field.value != "" {
			*field.target = types.StringValue(field.value)
		} else if field.target.IsUnknown() {
			*field.target = types.StringNull()
		}
	}
	if out.AllowedBuckets != nil {
		state.AllowedBuckets, _ = types.SetValueFrom(ctx, types.StringType, *out.AllowedBuckets)
	} else if state.AllowedBuckets.IsUnknown() {
		state.AllowedBuckets = types.SetNull(types.StringType)
	}
	if state.SecretAccessKey.IsUnknown() {
		state.SecretAccessKey = types.StringNull()
	}
}

func (r *s3CredentialResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan s3CredentialResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{}
	for key, value := range map[string]types.String{"name": plan.Name, "permission_type": plan.PermissionType, "bucket_scope": plan.BucketScope} {
		if known(value) {
			body[key] = value.ValueString()
		}
	}
	if known(plan.AllowedBuckets) {
		var buckets []string
		resp.Diagnostics.Append(plan.AllowedBuckets.ElementsAs(ctx, &buckets, false)...)
		body["allowed_buckets"] = buckets
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var out s3CredentialAPI
	if err := r.client.do(ctx, http.MethodPost, "/object-storage/credentials", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create S3 credential", err.Error())
		return
	}
	if out.AccessKeyID == "" {
		resp.Diagnostics.AddError("Invalid S3 credential response", "The API returned no access_key_id.")
		return
	}
	plan.SecretAccessKey = types.StringNull()
	if out.SecretAccessKey != "" {
		plan.SecretAccessKey = types.StringValue(out.SecretAccessKey)
	}
	out.apply(ctx, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *s3CredentialResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state s3CredentialResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out s3CredentialAPI
	if err := r.client.do(ctx, http.MethodGet, s3CredentialPath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read S3 credential", err.Error())
		return
	}
	out.apply(ctx, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update has nothing to send: every input replaces the key.
func (r *s3CredentialResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan s3CredentialResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete revokes the key (POST .../revoke) and then deletes its record
// (DELETE), the order the API documents and the portal uses. A key that is
// already revoked is only deleted.
func (r *s3CredentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state s3CredentialResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Status.ValueString() != "revoked" {
		err := r.client.do(ctx, http.MethodPost, s3CredentialPath(state.ID.ValueString())+"/revoke", nil, nil)
		if IsNotFound(err) {
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("Failed to revoke S3 credential", err.Error())
			return
		}
		state.Status = types.StringValue("revoked")
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
	if err := r.client.do(ctx, http.MethodDelete, s3CredentialPath(state.ID.ValueString()), nil, nil); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete revoked S3 credential", err.Error()+" The key is revoked and can no longer authenticate.")
	}
}

func (r *s3CredentialResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("secret_access_key"), types.StringNull())...)
}
