package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type migrationDataSource struct {
	client *Client
}

type migrationDataSourceModel struct {
	ID            types.String `tfsdk:"id"`
	RepositoryURL types.String `tfsdk:"repository_url"`
	Revision      types.String `tfsdk:"revision"`
	ProjectID     types.String `tfsdk:"project_id"`
	Region        types.String `tfsdk:"region"`
	Runtime       types.String `tfsdk:"runtime"`
	Database      types.String `tfsdk:"database"`
	Status        types.String `tfsdk:"status"`
	AttemptCount  types.Int64  `tfsdk:"attempt_count"`
	CreatedAt     types.String `tfsdk:"created_at"`
	UpdatedAt     types.String `tfsdk:"updated_at"`
}

var (
	_ datasource.DataSource              = &migrationDataSource{}
	_ datasource.DataSourceWithConfigure = &migrationDataSource{}
)

func NewMigrationDataSource() datasource.DataSource { return &migrationDataSource{} }

func (dataSource *migrationDataSource) Metadata(
	_ context.Context, request datasource.MetadataRequest, response *datasource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_migration"
}

func (dataSource *migrationDataSource) Schema(
	_ context.Context, _ datasource.SchemaRequest, response *datasource.SchemaResponse,
) {
	response.Schema = schema.Schema{
		MarkdownDescription: "Reads one Cloudify migration by ID.",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Required: true},
			"repository_url": schema.StringAttribute{Computed: true},
			"revision":       schema.StringAttribute{Computed: true},
			"project_id":     schema.StringAttribute{Computed: true},
			"region":         schema.StringAttribute{Computed: true},
			"runtime":        schema.StringAttribute{Computed: true},
			"database":       schema.StringAttribute{Computed: true},
			"status":         schema.StringAttribute{Computed: true},
			"attempt_count":  schema.Int64Attribute{Computed: true},
			"created_at":     schema.StringAttribute{Computed: true},
			"updated_at":     schema.StringAttribute{Computed: true},
		},
	}
}

func (dataSource *migrationDataSource) Configure(
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
	dataSource.client = client
}

func (dataSource *migrationDataSource) Read(
	ctx context.Context, request datasource.ReadRequest, response *datasource.ReadResponse,
) {
	var state migrationDataSourceModel
	response.Diagnostics.Append(request.Config.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	migration, err := dataSource.client.GetMigration(ctx, state.ID.ValueString())
	if err != nil {
		response.Diagnostics.AddError("Unable to read migration", err.Error())
		return
	}
	setDataSourceMigration(&state, migration)
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func setDataSourceMigration(state *migrationDataSourceModel, migration Migration) {
	state.ID = types.StringValue(migration.ID)
	state.RepositoryURL = types.StringValue(migration.Source.RepositoryURL)
	state.Revision = types.StringValue(migration.Source.Revision)
	state.ProjectID = types.StringValue(migration.Destination.ProjectID)
	state.Region = types.StringValue(migration.Destination.Region)
	state.Runtime = types.StringValue(migration.Destination.Runtime)
	if migration.Destination.Database == "" {
		state.Database = types.StringNull()
	} else {
		state.Database = types.StringValue(migration.Destination.Database)
	}
	state.Status = types.StringValue(migration.Status)
	state.AttemptCount = types.Int64Value(migration.AttemptCount)
	state.CreatedAt = types.StringValue(migration.CreatedAt.UTC().Format(time.RFC3339Nano))
	state.UpdatedAt = types.StringValue(migration.UpdatedAt.UTC().Format(time.RFC3339Nano))
}
