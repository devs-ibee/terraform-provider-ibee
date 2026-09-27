package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*s3CredentialResource)(nil)
	_ resource.ResourceWithConfigure      = (*s3CredentialResource)(nil)
	_ resource.ResourceWithValidateConfig = (*s3CredentialResource)(nil)
	_ resource.ResourceWithImportState    = (*s3CredentialResource)(nil)
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
	OrganizationID  string    `json:"organization_id"`
	WorkspaceID     string    `json:"workspace_id"`
	Status          string    `json:"status"`
}

func (r *s3CredentialResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_credential"
}
func (r *s3CredentialResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "A scoped S3 credential. Permission level and bucket scope must be chosen explicitly; any input change replaces the key. The generated secret_access_key is returned once and is stored in Terraform state, marked sensitive but NOT encrypted by the provider. Use an encrypted access-controlled state backend. Imports cannot recover the secret and leave secret_access_key null. Destroy revokes the key and confirms it is no longer active; it does not delete buckets or objects.", Attributes: map[string]schema.Attribute{
		"id":                schema.StringAttribute{Computed: true, Description: "S3 access key ID; also used for import.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"name":              schema.StringAttribute{Required: true, Description: "Credential display name, 1–128 characters.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"permission_type":   schema.StringAttribute{Required: true, Description: "Choose object_ro, object_rw, admin_ro, or admin_rw. Admin permissions always cover all buckets; use object_ro/object_rw with specific scope for least privilege.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"bucket_scope":      schema.StringAttribute{Required: true, Description: "Explicitly choose specific or all. No implicit workspace-wide access is granted.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"allowed_buckets":   schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{})), Description: "Nonempty bucket-name set when bucket_scope=specific. Must be empty for all or admin permissions.", PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}},
		"secret_access_key": schema.StringAttribute{Computed: true, Sensitive: true, Description: "Creation-only generated S3 secret. Persisted in Terraform state; unavailable after import. Reads never overwrite the locally saved secret."},
		"status":            schema.StringAttribute{Computed: true},
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

func validateS3Scope(permission, scope string, buckets []string) error {
	if permission != "object_ro" && permission != "object_rw" && permission != "admin_ro" && permission != "admin_rw" {
		return fmt.Errorf("permission_type must be object_ro, object_rw, admin_ro, or admin_rw")
	}
	if scope != "specific" && scope != "all" {
		return fmt.Errorf("bucket_scope must explicitly be specific or all")
	}
	if strings.HasPrefix(permission, "admin_") && (scope != "all" || len(buckets) > 0) {
		return fmt.Errorf("admin permissions grant all-bucket access; explicitly choose bucket_scope=all with no allowed_buckets, or use object permissions for restricted access")
	}
	if scope == "specific" && len(buckets) == 0 {
		return fmt.Errorf("specific scope requires at least one allowed bucket")
	}
	if scope == "all" && len(buckets) > 0 {
		return fmt.Errorf("allowed_buckets must be empty for all-bucket scope")
	}
	for _, bucket := range buckets {
		if strings.TrimSpace(bucket) != bucket || bucket == "" || strings.ContainsAny(bucket, "/\\") {
			return fmt.Errorf("allowed_buckets must contain nonempty logical bucket names without surrounding whitespace or path separators")
		}
	}
	return nil
}

func (r *s3CredentialResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg s3CredentialResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.Name.IsUnknown() && !cfg.Name.IsNull() && (strings.TrimSpace(cfg.Name.ValueString()) == "" || len([]rune(cfg.Name.ValueString())) > 128) {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid credential name", "Use 1–128 characters.")
	}
	if cfg.PermissionType.IsUnknown() || cfg.BucketScope.IsUnknown() || cfg.AllowedBuckets.IsUnknown() {
		return
	}
	buckets := []string{}
	for _, v := range cfg.AllowedBuckets.Elements() {
		if v.IsUnknown() {
			return
		}
		if v.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("allowed_buckets"), "Invalid bucket scope", "Bucket names cannot be null.")
			return
		}
		buckets = append(buckets, v.(types.String).ValueString())
	}
	if err := validateS3Scope(cfg.PermissionType.ValueString(), cfg.BucketScope.ValueString(), buckets); err != nil {
		resp.Diagnostics.AddError("Invalid S3 credential scope", err.Error())
	}
}

func s3CredentialPath(id string) string { return "/object-storage/credentials/" + url.PathEscape(id) }
func s3CredentialSafeError(err error) string {
	return strings.ReplaceAll(secretSafeError(err), "Secret API", "S3 credential API")
}

func (r *s3CredentialResource) applyMetadata(ctx context.Context, out s3CredentialAPI, state *s3CredentialResourceModel) error {
	if out.AccessKeyID == "" || out.Name == "" || out.AllowedBuckets == nil || out.OrganizationID == "" || out.WorkspaceID == "" {
		return fmt.Errorf("credential response omitted identity, tenant or scoped metadata; state was preserved")
	}
	if !state.ID.IsNull() && !state.ID.IsUnknown() && out.AccessKeyID != state.ID.ValueString() {
		return fmt.Errorf("credential response identity does not match the managed access key")
	}
	if out.WorkspaceID != r.client.workspaceID || (r.client.organizationID != "" && out.OrganizationID != r.client.organizationID) {
		return fmt.Errorf("credential response tenant does not match the configured workspace or organization")
	}
	if out.Status != "active" && out.Status != "revoked" {
		return fmt.Errorf("credential response has an unrecognized status")
	}
	if err := validateS3Scope(out.PermissionType, out.BucketScope, *out.AllowedBuckets); err != nil {
		return fmt.Errorf("invalid credential scope returned by service: %w", err)
	}
	buckets, diagnostics := types.SetValueFrom(ctx, types.StringType, *out.AllowedBuckets)
	if diagnostics.HasError() {
		return fmt.Errorf("invalid allowed_buckets response")
	}
	state.ID, state.Name = types.StringValue(out.AccessKeyID), types.StringValue(out.Name)
	state.PermissionType, state.BucketScope = types.StringValue(out.PermissionType), types.StringValue(out.BucketScope)
	state.AllowedBuckets, state.Status = buckets, types.StringValue(out.Status)
	if state.SecretAccessKey.IsUnknown() {
		state.SecretAccessKey = types.StringNull()
	}
	return nil
}

func (r *s3CredentialResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan s3CredentialResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var buckets []string
	resp.Diagnostics.Append(plan.AllowedBuckets.ElementsAs(ctx, &buckets, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if buckets == nil {
		buckets = []string{}
	}
	if err := validateS3Scope(plan.PermissionType.ValueString(), plan.BucketScope.ValueString(), buckets); err != nil {
		resp.Diagnostics.AddError("Invalid S3 credential scope", err.Error())
		return
	}
	if err := r.client.requireBillingEligibility(ctx, "", nil); err != nil {
		resp.Diagnostics.AddError("Credential billing eligibility failed", err.Error())
		return
	}
	var out s3CredentialAPI
	body := map[string]any{"name": plan.Name.ValueString(), "permission_type": plan.PermissionType.ValueString(), "bucket_scope": plan.BucketScope.ValueString(), "allowed_buckets": buckets}
	if err := r.client.do(ctx, http.MethodPost, "/object-storage/credentials", body, &out); err != nil {
		resp.Diagnostics.AddError("Failed to create S3 credential", s3CredentialSafeError(err))
		return
	}
	if out.AccessKeyID != "" {
		plan.ID, plan.Status, plan.SecretAccessKey = types.StringValue(out.AccessKeyID), types.StringValue(out.Status), types.StringNull()
		if out.SecretAccessKey != "" {
			plan.SecretAccessKey = types.StringValue(out.SecretAccessKey)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err := r.applyMetadata(ctx, out, &plan); err != nil {
		resp.Diagnostics.AddError("Invalid created credential metadata", err.Error())
		return
	}
	if out.SecretAccessKey == "" {
		resp.Diagnostics.AddError("Created credential secret is missing", "The API created a key but did not return its one-time secret. State retains the access key ID for revocation; the secret cannot be recovered by reading or importing it.")
		return
	}
	if out.Status != "active" {
		resp.Diagnostics.AddError("Created credential is not active", "The API returned an inactive key. Its identity is retained for cleanup.")
		return
	}
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
		resp.Diagnostics.AddError("Failed to read S3 credential", s3CredentialSafeError(err))
		return
	}
	if err := r.applyMetadata(ctx, out, &state); err != nil {
		resp.Diagnostics.AddError("Invalid credential metadata", err.Error())
		return
	}
	if out.Status == "revoked" {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *s3CredentialResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("S3 credential changes require replacement", "Name, permission and scope changes replace the credential; the API does not support updating them in place.")
}

func (r *s3CredentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state s3CredentialResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Use the explicit revoke operation. The native DELETE currently erases
	// metadata, whereas earlier published schemas described DELETE as revoke.
	var result struct {
		Success *bool `json:"success"`
	}
	err := r.client.do(ctx, http.MethodPost, s3CredentialPath(state.ID.ValueString())+"/revoke", nil, &result)
	if err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to revoke S3 credential", s3CredentialSafeError(err))
		return
	}
	if err == nil && (result.Success == nil || !*result.Success) {
		resp.Diagnostics.AddError("Credential revocation not confirmed", "The API did not confirm successful revocation; state is preserved.")
		return
	}
	var out s3CredentialAPI
	if err := r.client.do(ctx, http.MethodGet, s3CredentialPath(state.ID.ValueString()), nil, &out); err != nil {
		if IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to confirm credential revocation", s3CredentialSafeError(err))
		return
	}
	if err := r.applyMetadata(ctx, out, &state); err != nil {
		resp.Diagnostics.AddError("Cannot verify revoked credential", err.Error())
		return
	}
	if out.Status != "revoked" {
		resp.Diagnostics.AddError("S3 credential is still active", "The service still reports this key as active after revocation. State is preserved; retry after checking the credential status.")
	}
}

func (r *s3CredentialResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) == "" || strings.ContainsAny(req.ID, "/\\") {
		resp.Diagnostics.AddError("Invalid credential import ID", "Import using the S3 access key ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("secret_access_key"), types.StringNull())...)
}
