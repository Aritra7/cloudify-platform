package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type managedResourceDataSource struct{ client *Client }

type managedResourceDataSourceModel struct {
	ID                  types.String `tfsdk:"id"`
	Kind                types.String `tfsdk:"kind"`
	MigrationID         types.String `tfsdk:"migration_id"`
	SourcePlanID        types.String `tfsdk:"source_plan_id"`
	ProjectID           types.String `tfsdk:"project_id"`
	Region              types.String `tfsdk:"region"`
	Name                types.String `tfsdk:"name"`
	Desired             types.String `tfsdk:"desired"`
	Observed            types.String `tfsdk:"observed"`
	State               types.String `tfsdk:"state"`
	RemediationPolicy   types.String `tfsdk:"remediation_policy"`
	Lifecycle           types.String `tfsdk:"lifecycle_status"`
	Generation          types.Int64  `tfsdk:"generation"`
	ObservedGeneration  types.Int64  `tfsdk:"observed_generation"`
	RetryCount          types.Int64  `tfsdk:"retry_count"`
	DeleteFailure       types.String `tfsdk:"delete_failure"`
	DeletionRequestedBy types.String `tfsdk:"deletion_requested_by"`
	DeletionRequestedAt types.String `tfsdk:"deletion_requested_at"`
	DeletedAt           types.String `tfsdk:"deleted_at"`
	CreatedAt           types.String `tfsdk:"created_at"`
	UpdatedAt           types.String `tfsdk:"updated_at"`
}

var (
	_ datasource.DataSource              = &managedResourceDataSource{}
	_ datasource.DataSourceWithConfigure = &managedResourceDataSource{}
)

func NewManagedResourceDataSource() datasource.DataSource { return &managedResourceDataSource{} }

func (managed *managedResourceDataSource) Metadata(
	_ context.Context, request datasource.MetadataRequest, response *datasource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_resource"
}

func (managed *managedResourceDataSource) Schema(
	_ context.Context, _ datasource.SchemaRequest, response *datasource.SchemaResponse,
) {
	computedString := func() schema.StringAttribute { return schema.StringAttribute{Computed: true} }
	response.Schema = schema.Schema{
		MarkdownDescription: "Reads one Cloudify managed resource and its reconciliation lifecycle.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Required: true}, "kind": computedString(),
			"migration_id": computedString(), "source_plan_id": computedString(),
			"project_id": computedString(), "region": computedString(), "name": computedString(),
			"desired": computedString(), "observed": computedString(), "state": computedString(),
			"remediation_policy": computedString(), "lifecycle_status": computedString(),
			"generation": schema.Int64Attribute{Computed: true}, "observed_generation": schema.Int64Attribute{Computed: true},
			"retry_count": schema.Int64Attribute{Computed: true}, "delete_failure": computedString(),
			"deletion_requested_by": computedString(), "deletion_requested_at": computedString(),
			"deleted_at": computedString(), "created_at": computedString(), "updated_at": computedString(),
		},
	}
}

func (managed *managedResourceDataSource) Configure(
	_ context.Context, request datasource.ConfigureRequest, response *datasource.ConfigureResponse,
) {
	if request.ProviderData == nil {
		return
	}
	client, ok := request.ProviderData.(*Client)
	if !ok {
		response.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *Client, got %T", request.ProviderData))
		return
	}
	managed.client = client
}

func (managed *managedResourceDataSource) Read(
	ctx context.Context, request datasource.ReadRequest, response *datasource.ReadResponse,
) {
	var state managedResourceDataSourceModel
	response.Diagnostics.Append(request.Config.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	remote, err := managed.client.GetResource(ctx, state.ID.ValueString())
	if err != nil {
		response.Diagnostics.AddError("Unable to read managed resource", err.Error())
		return
	}
	state = managedResourceDataSourceModel{
		ID: types.StringValue(remote.ID), Kind: types.StringValue(remote.Kind),
		MigrationID: types.StringValue(remote.MigrationID), SourcePlanID: types.StringValue(remote.SourcePlanID),
		ProjectID: types.StringValue(remote.ProjectID), Region: types.StringValue(remote.Region), Name: types.StringValue(remote.Name),
		Desired: nullableString(string(remote.Desired)), Observed: nullableString(string(remote.Observed)),
		State: types.StringValue(remote.State), RemediationPolicy: types.StringValue(remote.RemediationPolicy),
		Lifecycle: types.StringValue(remote.Lifecycle), Generation: types.Int64Value(remote.Generation),
		ObservedGeneration: types.Int64Value(remote.ObservedGeneration), RetryCount: types.Int64Value(remote.RetryCount),
		DeleteFailure: nullableString(remote.DeleteFailure), DeletionRequestedBy: nullableString(remote.DeletionRequestedBy),
		DeletionRequestedAt: nullableTime(remote.DeletionRequestedAt), DeletedAt: nullableTime(remote.DeletedAt),
		CreatedAt: types.StringValue(remote.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")),
		UpdatedAt: types.StringValue(remote.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")),
	}
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}
