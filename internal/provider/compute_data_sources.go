package provider

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ---------------------------------------------------------------- plans

var (
	_ datasource.DataSource              = (*computePlansDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*computePlansDataSource)(nil)
)

type computePlansDataSource struct {
	client *Client
}

func NewComputePlansDataSource() datasource.DataSource { return &computePlansDataSource{} }

type computePlanModel struct {
	PlanID            types.String `tfsdk:"plan_id"`
	Code              types.String `tfsdk:"code"`
	PricingStatus     types.String `tfsdk:"pricing_status"`
	BillingInterval   types.String `tfsdk:"billing_interval"`
	MonthlyPriceMinor types.Int64  `tfsdk:"monthly_price_minor"`
	GpuModel          types.String `tfsdk:"gpu_model"`
	Name              types.String `tfsdk:"name"`
	Cpu               types.Int64  `tfsdk:"cpu"`
	RamMb             types.Int64  `tfsdk:"ram_mb"`
	DiskGb            types.Int64  `tfsdk:"disk_gb"`
	HourlyPriceMinor  types.Int64  `tfsdk:"hourly_price_minor"`
	Currency          types.String `tfsdk:"currency"`
	Selectable        types.Bool   `tfsdk:"selectable"`
	GpuCount          types.Int64  `tfsdk:"gpu_count"`
}

type computePlansModel struct {
	VmType          types.String       `tfsdk:"vm_type"`
	SiteID          types.String       `tfsdk:"site_id"`
	Currency        types.String       `tfsdk:"currency"`
	BillingInterval types.String       `tfsdk:"billing_interval"`
	Plans           []computePlanModel `tfsdk:"plans"`
}

func (d *computePlansDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compute_plans"
}

func (d *computePlansDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Compute plans accepted by VM creation. Billing follows the plan automatically.",
		Attributes: map[string]schema.Attribute{
			"vm_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Validators:  []validator.String{computeOneOf("cloud", "gpu")},
				Description: "cloud (default) or gpu.",
			},
			"site_id": schema.StringAttribute{
				Optional:    true,
				Description: "Restrict plans to one placement site.",
			},
			"currency":         schema.StringAttribute{Optional: true, Computed: true, Validators: []validator.String{computeCurrencyValidator{}}, Description: "Three-letter uppercase catalog currency; defaults to INR. VM creation independently resolves the organization currency from billing."},
			"billing_interval": schema.StringAttribute{Optional: true, Computed: true, Validators: []validator.String{computeOneOf("HOURLY", "MONTHLY")}, Description: "Catalog display interval; defaults to MONTHLY. VM resources explicitly select their own supported billing_interval."},
			"plans": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"plan_id":             schema.StringAttribute{Computed: true},
						"code":                schema.StringAttribute{Computed: true},
						"pricing_status":      schema.StringAttribute{Computed: true},
						"billing_interval":    schema.StringAttribute{Computed: true},
						"monthly_price_minor": schema.Int64Attribute{Computed: true},
						"gpu_model":           schema.StringAttribute{Computed: true},
						"name":                schema.StringAttribute{Computed: true},
						"cpu":                 schema.Int64Attribute{Computed: true},
						"ram_mb":              schema.Int64Attribute{Computed: true},
						"disk_gb":             schema.Int64Attribute{Computed: true},
						"hourly_price_minor":  schema.Int64Attribute{Computed: true},
						"currency":            schema.StringAttribute{Computed: true},
						"selectable":          schema.BoolAttribute{Computed: true},
						"gpu_count":           schema.Int64Attribute{Computed: true},
					},
				},
			},
		},
	}
}

func (d *computePlansDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *computePlansDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg computePlansModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vmType := cfg.VmType.ValueString()
	if vmType == "" {
		vmType = "cloud"
		cfg.VmType = types.StringValue(vmType)
	}

	if cfg.Currency.IsNull() {
		cfg.Currency = types.StringValue("INR")
	}
	if cfg.BillingInterval.IsNull() {
		cfg.BillingInterval = types.StringValue("MONTHLY")
	}
	plans, err := d.client.listComputePlansFiltered(ctx, vmType, cfg.SiteID.ValueString(), cfg.Currency.ValueString(), cfg.BillingInterval.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to list compute plans", err.Error())
		return
	}
	cfg.Plans = make([]computePlanModel, 0, len(plans))
	for _, p := range plans {
		cfg.Plans = append(cfg.Plans, computePlanModel{
			PlanID:            types.StringValue(p.PlanID),
			Code:              types.StringValue(p.Code),
			PricingStatus:     types.StringValue(p.PricingStatus),
			BillingInterval:   types.StringValue(p.BillingInterval),
			MonthlyPriceMinor: types.Int64PointerValue(p.MonthlyPriceMinor),
			GpuModel:          types.StringValue(p.GpuModel),
			Name:              types.StringValue(p.Name),
			Cpu:               types.Int64Value(p.Cpu),
			RamMb:             types.Int64Value(p.RamMb),
			DiskGb:            types.Int64Value(p.DiskGb),
			HourlyPriceMinor:  types.Int64PointerValue(p.HourlyPriceMinor),
			Currency:          types.StringValue(p.Currency),
			Selectable:        types.BoolValue(p.Selectable),
			GpuCount:          types.Int64Value(p.GpuCount),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ---------------------------------------------------------------- images

var (
	_ datasource.DataSource              = (*imagesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*imagesDataSource)(nil)
)

type imagesDataSource struct {
	client *Client
}

func NewImagesDataSource() datasource.DataSource { return &imagesDataSource{} }

type imageModel struct {
	TemplateID types.String `tfsdk:"template_id"`
	Name       types.String `tfsdk:"name"`
	OsDistro   types.String `tfsdk:"os_distro"`
	OsType     types.String `tfsdk:"os_type"`
}

type imagesModel struct {
	VmType types.String `tfsdk:"vm_type"`
	Images []imageModel `tfsdk:"images"`
}

func (d *imagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *imagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "OS images accepted by VM creation.",
		Attributes: map[string]schema.Attribute{
			"vm_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Validators:  []validator.String{computeOneOf("cloud", "gpu")},
				Description: "cloud (default) or gpu.",
			},
			"images": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"template_id": schema.StringAttribute{Computed: true},
						"name":        schema.StringAttribute{Computed: true},
						"os_distro":   schema.StringAttribute{Computed: true},
						"os_type":     schema.StringAttribute{Computed: true},
					},
				},
			},
		},
	}
}

func (d *imagesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *imagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg imagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vmType := cfg.VmType.ValueString()
	if vmType == "" {
		vmType = "cloud"
		cfg.VmType = types.StringValue(vmType)
	}

	var out struct {
		Images *[]struct {
			TemplateID string `json:"template_id"`
			Name       string `json:"name"`
			OsDistro   string `json:"os_distro"`
			OsType     string `json:"os_type"`
		} `json:"images"`
	}
	if err := d.client.do(ctx, http.MethodGet, "/compute/images?vm_type="+url.QueryEscape(vmType), nil, &out); err != nil {
		resp.Diagnostics.AddError("Failed to list images", err.Error())
		return
	}
	if out.Images == nil {
		resp.Diagnostics.AddError("Invalid image catalog", "API omitted images array.")
		return
	}
	cfg.Images = make([]imageModel, 0, len(*out.Images))
	for _, im := range *out.Images {
		cfg.Images = append(cfg.Images, imageModel{
			TemplateID: types.StringValue(im.TemplateID),
			Name:       types.StringValue(im.Name),
			OsDistro:   types.StringValue(im.OsDistro),
			OsType:     types.StringValue(im.OsType),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
