package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Catalog data sources for the SKUs that snapshots, backups and block volumes
// are created with. They list what the API returns; the API decides which SKU,
// price and size a create accepts.

func configureDataSource(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *Client {
	if req.ProviderData == nil {
		return nil
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
	}
	return client
}

// catalogFilterAttributes are the optional query parameters every storage
// catalog list takes.
func catalogFilterAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"site_id":          schema.StringAttribute{Optional: true, Description: "Return only plans offered at this site."},
		"currency":         schema.StringAttribute{Optional: true, Description: "Price currency, such as INR. Omit to use the API's default (INR)."},
		"billing_interval": schema.StringAttribute{Optional: true, Description: "HOURLY or MONTHLY: the interval the listed prices are quoted for. Omit to use the API's default (MONTHLY)."},
	}
}

func catalogQuery(siteID, currency, interval types.String) string {
	query := url.Values{}
	for key, value := range map[string]types.String{"site_id": siteID, "currency": currency, "billing_interval": interval} {
		if !value.IsNull() && !value.IsUnknown() && value.ValueString() != "" {
			query.Set(key, value.ValueString())
		}
	}
	if len(query) == 0 {
		return ""
	}
	return "?" + query.Encode()
}

// catalogJSON re-encodes a catalog object received from the API, keeping
// numbers exactly as sent, or returns null when the API sent none.
func catalogJSON(raw json.RawMessage) (types.String, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return types.StringNull(), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value map[string]any
	if err := dec.Decode(&value); err != nil {
		return types.StringNull(), fmt.Errorf("invalid catalog object in response: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return types.StringNull(), err
	}
	return types.StringValue(string(encoded)), nil
}

// ---------------------------------------------------------------- snapshot and backup storage

type recoveryStoragePlansDataSource struct {
	client  *Client
	product string // snapshot or backup
}

func NewSnapshotStoragePlansDataSource() datasource.DataSource {
	return &recoveryStoragePlansDataSource{product: "snapshot"}
}
func NewBackupStoragePlansDataSource() datasource.DataSource {
	return &recoveryStoragePlansDataSource{product: "backup"}
}

type recoveryStoragePlanModel struct {
	SkuID          types.String `tfsdk:"sku_id"`
	SkuCode        types.String `tfsdk:"sku_code"`
	Name           types.String `tfsdk:"name"`
	Selectable     types.Bool   `tfsdk:"selectable"`
	PricingStatus  types.String `tfsdk:"pricing_status"`
	BillingCatalog types.String `tfsdk:"billing_catalog"`
}

type recoveryStoragePlansModel struct {
	SiteID          types.String               `tfsdk:"site_id"`
	Currency        types.String               `tfsdk:"currency"`
	BillingInterval types.String               `tfsdk:"billing_interval"`
	Plans           []recoveryStoragePlanModel `tfsdk:"plans"`
}

func (d *recoveryStoragePlansDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.product + "_storage_plans"
}

func (d *recoveryStoragePlansDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	use := "billing_catalog of ibee_cloud_vm_snapshot or ibee_gpu_vm_snapshot"
	if d.product == "backup" {
		use = "billing_catalog of ibee_cloud_vm_backup_policy or ibee_gpu_vm_backup_policy"
	}
	attributes := catalogFilterAttributes()
	attributes["plans"] = schema.ListNestedAttribute{
		Computed: true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"sku_id":          schema.StringAttribute{Computed: true},
				"sku_code":        schema.StringAttribute{Computed: true, Description: "SKU code, for example " + map[string]string{"snapshot": "SNAPSHOT-STD", "backup": "BACKUP-STD"}[d.product] + "."},
				"name":            schema.StringAttribute{Computed: true},
				"selectable":      schema.BoolAttribute{Computed: true, Description: "Whether the API has a published price for this SKU."},
				"pricing_status":  schema.StringAttribute{Computed: true, Description: "priced or unpriced, as the API reports."},
				"billing_catalog": schema.StringAttribute{Computed: true, Description: "The SKU's billing_catalog as a JSON object. Pass it unchanged as the " + use + "."},
			},
		},
	}
	resp.Schema = schema.Schema{
		Description: "The " + d.product + " storage SKUs the API lists (GET /compute/plans/" + d.product + "-storage). Pick one and pass its billing_catalog unchanged as the " + use + ". Listing reserves nothing.",
		Attributes:  attributes,
	}
}

func (d *recoveryStoragePlansDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSource(req, resp)
}

func (d *recoveryStoragePlansDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state recoveryStoragePlansModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Items *[]struct {
			SkuID          string          `json:"sku_id"`
			SkuCode        string          `json:"sku_code"`
			Name           *string         `json:"name"`
			Selectable     *bool           `json:"selectable"`
			PricingStatus  *string         `json:"pricing_status"`
			BillingCatalog json.RawMessage `json:"billing_catalog"`
		} `json:"items"`
	}
	if err := d.client.do(ctx, http.MethodGet, "/compute/plans/"+d.product+"-storage"+catalogQuery(state.SiteID, state.Currency, state.BillingInterval), nil, &out); err != nil {
		resp.Diagnostics.AddError("Failed to list "+d.product+" storage plans", err.Error())
		return
	}
	if out.Items == nil {
		resp.Diagnostics.AddError("Invalid "+d.product+" storage plans response", "The response omitted the items array.")
		return
	}
	state.Plans = make([]recoveryStoragePlanModel, 0, len(*out.Items))
	for _, item := range *out.Items {
		catalog, err := catalogJSON(item.BillingCatalog)
		if err != nil {
			resp.Diagnostics.AddError("Invalid "+d.product+" storage plans response", err.Error())
			return
		}
		state.Plans = append(state.Plans, recoveryStoragePlanModel{
			SkuID:          types.StringValue(item.SkuID),
			SkuCode:        types.StringValue(item.SkuCode),
			Name:           types.StringPointerValue(item.Name),
			Selectable:     types.BoolPointerValue(item.Selectable),
			PricingStatus:  types.StringPointerValue(item.PricingStatus),
			BillingCatalog: catalog,
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ---------------------------------------------------------------- block storage

type blockStoragePlansDataSource struct{ client *Client }

func NewBlockStoragePlansDataSource() datasource.DataSource { return &blockStoragePlansDataSource{} }

type blockStoragePlanModel struct {
	ID                   types.String `tfsdk:"id"`
	SkuCode              types.String `tfsdk:"sku_code"`
	PlanCode             types.String `tfsdk:"plan_code"`
	Name                 types.String `tfsdk:"name"`
	Description          types.String `tfsdk:"description"`
	StorageType          types.String `tfsdk:"storage_type"`
	Status               types.String `tfsdk:"status"`
	Selectable           types.Bool   `tfsdk:"selectable"`
	MinSizeGb            types.Int64  `tfsdk:"min_size_gb"`
	MaxSizeGb            types.Int64  `tfsdk:"max_size_gb"`
	AllowedSizesGb       types.List   `tfsdk:"allowed_sizes_gb"`
	PricePerGbMonthMinor types.Int64  `tfsdk:"price_per_gb_month_minor"`
	Iops                 types.String `tfsdk:"iops"`
	Throughput           types.String `tfsdk:"throughput"`
	IncludedUsage        types.String `tfsdk:"included_usage"`
	BillingCatalog       types.String `tfsdk:"billing_catalog"`
}

type blockStoragePlansModel struct {
	SiteID          types.String            `tfsdk:"site_id"`
	Currency        types.String            `tfsdk:"currency"`
	BillingInterval types.String            `tfsdk:"billing_interval"`
	Plans           []blockStoragePlanModel `tfsdk:"plans"`
}

func (d *blockStoragePlansDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_block_storage_plans"
}

func (d *blockStoragePlansDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := catalogFilterAttributes()
	attributes["plans"] = schema.ListNestedAttribute{
		Computed: true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"id":                       schema.StringAttribute{Computed: true},
				"sku_code":                 schema.StringAttribute{Computed: true, Description: "Send as sku_code of ibee_block_volume."},
				"plan_code":                schema.StringAttribute{Computed: true},
				"name":                     schema.StringAttribute{Computed: true},
				"description":              schema.StringAttribute{Computed: true},
				"storage_type":             schema.StringAttribute{Computed: true},
				"status":                   schema.StringAttribute{Computed: true},
				"selectable":               schema.BoolAttribute{Computed: true},
				"min_size_gb":              schema.Int64Attribute{Computed: true, Description: "From included_usage, when the API reports it as a number."},
				"max_size_gb":              schema.Int64Attribute{Computed: true, Description: "From included_usage, when the API reports it as a number."},
				"allowed_sizes_gb":         schema.ListAttribute{Computed: true, ElementType: types.Int64Type, Description: "From included_usage, when the API reports it."},
				"price_per_gb_month_minor": schema.Int64Attribute{Computed: true, Description: "From included_usage, in the currency's minor unit."},
				"iops":                     schema.StringAttribute{Computed: true, Description: "From included_usage: display text, for example Up to 10K."},
				"throughput":               schema.StringAttribute{Computed: true, Description: "From included_usage: display text, for example Up to 400 MiB/s."},
				"included_usage":           schema.StringAttribute{Computed: true, Description: "The plan's included_usage object (sizes, performance, prices) as JSON, as the API returns it. Use jsondecode to read other fields."},
				"billing_catalog":          schema.StringAttribute{Computed: true, Description: "The plan's billing_catalog as a JSON object, when the API returns one."},
			},
		},
	}
	resp.Schema = schema.Schema{
		Description: "The block storage plans the API lists (GET /block-storage/plans), as the portal's Create Volume page shows them. Use a plan's sku_code for ibee_block_volume; the API decides which SKU and size a create accepts.",
		Attributes:  attributes,
	}
}

func (d *blockStoragePlansDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSource(req, resp)
}

// usageString reads one text field of included_usage, or null.
func usageString(usage map[string]any, key string) types.String {
	if text, ok := usage[key].(string); ok {
		return types.StringValue(text)
	}
	return types.StringNull()
}

// usageInt reads one integer field of included_usage, or null when the API
// sent none or sent another type.
func usageInt(usage map[string]any, key string) types.Int64 {
	if n, ok := usage[key].(json.Number); ok {
		if v, err := n.Int64(); err == nil {
			return types.Int64Value(v)
		}
	}
	return types.Int64Null()
}

func (d *blockStoragePlansDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state blockStoragePlansModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Items *[]struct {
			ID             string          `json:"id"`
			SkuCode        string          `json:"sku_code"`
			PlanCode       *string         `json:"plan_code"`
			Name           string          `json:"name"`
			Description    *string         `json:"description"`
			StorageType    *string         `json:"storage_type"`
			Status         *string         `json:"status"`
			Selectable     *bool           `json:"selectable"`
			IncludedUsage  json.RawMessage `json:"included_usage"`
			BillingCatalog json.RawMessage `json:"billing_catalog"`
		} `json:"items"`
	}
	if err := d.client.do(ctx, http.MethodGet, "/block-storage/plans"+catalogQuery(state.SiteID, state.Currency, state.BillingInterval), nil, &out); err != nil {
		resp.Diagnostics.AddError("Failed to list block storage plans", err.Error())
		return
	}
	if out.Items == nil {
		resp.Diagnostics.AddError("Invalid block storage plans response", "The response omitted the items array.")
		return
	}
	state.Plans = make([]blockStoragePlanModel, 0, len(*out.Items))
	for _, item := range *out.Items {
		usageJSON, err := catalogJSON(item.IncludedUsage)
		if err != nil {
			resp.Diagnostics.AddError("Invalid block storage plans response", err.Error())
			return
		}
		catalog, err := catalogJSON(item.BillingCatalog)
		if err != nil {
			resp.Diagnostics.AddError("Invalid block storage plans response", err.Error())
			return
		}
		usage := map[string]any{}
		if !usageJSON.IsNull() {
			dec := json.NewDecoder(bytes.NewReader(item.IncludedUsage))
			dec.UseNumber()
			_ = dec.Decode(&usage)
		}
		sizes := types.ListNull(types.Int64Type)
		if raw, ok := usage["allowed_sizes_gb"].([]any); ok {
			values := make([]int64, 0, len(raw))
			for _, entry := range raw {
				if n, ok := entry.(json.Number); ok {
					if v, err := n.Int64(); err == nil {
						values = append(values, v)
					}
				}
			}
			list, diags := types.ListValueFrom(ctx, types.Int64Type, values)
			resp.Diagnostics.Append(diags...)
			sizes = list
		}
		state.Plans = append(state.Plans, blockStoragePlanModel{
			ID:                   types.StringValue(item.ID),
			SkuCode:              types.StringValue(item.SkuCode),
			PlanCode:             types.StringPointerValue(item.PlanCode),
			Name:                 types.StringValue(item.Name),
			Description:          types.StringPointerValue(item.Description),
			StorageType:          types.StringPointerValue(item.StorageType),
			Status:               types.StringPointerValue(item.Status),
			Selectable:           types.BoolPointerValue(item.Selectable),
			MinSizeGb:            usageInt(usage, "min_size_gb"),
			MaxSizeGb:            usageInt(usage, "max_size_gb"),
			AllowedSizesGb:       sizes,
			PricePerGbMonthMinor: usageInt(usage, "price_per_gb_month_minor"),
			Iops:                 usageString(usage, "iops"),
			Throughput:           usageString(usage, "throughput"),
			IncludedUsage:        usageJSON,
			BillingCatalog:       catalog,
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
