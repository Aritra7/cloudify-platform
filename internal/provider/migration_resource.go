package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const defaultOperationTimeout = 30 * time.Minute

type migrationResource struct {
	client *Client
}

type migrationResourceModel struct {
	ID             types.String   `tfsdk:"id"`
	IdempotencyKey types.String   `tfsdk:"idempotency_key"`
	RepositoryURL  types.String   `tfsdk:"repository_url"`
	Revision       types.String   `tfsdk:"revision"`
	ProjectID      types.String   `tfsdk:"project_id"`
	Region         types.String   `tfsdk:"region"`
	Runtime        types.String   `tfsdk:"runtime"`
	Database       types.String   `tfsdk:"database"`
	Status         types.String   `tfsdk:"status"`
	AttemptCount   types.Int64    `tfsdk:"attempt_count"`
	CreatedAt      types.String   `tfsdk:"created_at"`
	UpdatedAt      types.String   `tfsdk:"updated_at"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

var (
	_ resource.Resource                = &migrationResource{}
	_ resource.ResourceWithConfigure   = &migrationResource{}
	_ resource.ResourceWithImportState = &migrationResource{}
)

func NewMigrationResource() resource.Resource { return &migrationResource{} }

func (migrationResource *migrationResource) Metadata(
	_ context.Context, request resource.MetadataRequest, response *resource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_migration"
}

func (migrationResource *migrationResource) Schema(
	ctx context.Context, _ resource.SchemaRequest, response *resource.SchemaResponse,
) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	response.Schema = schema.Schema{
		MarkdownDescription: "Starts and observes one asynchronous Cloudify application migration.",
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"idempotency_key": schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Stable key used to safely replay creation."},
			"repository_url":  schema.StringAttribute{Required: true, PlanModifiers: replace},
			"revision":        schema.StringAttribute{Required: true, PlanModifiers: replace},
			"project_id":      schema.StringAttribute{Required: true, PlanModifiers: replace},
			"region":          schema.StringAttribute{Required: true, PlanModifiers: replace},
			"runtime":         schema.StringAttribute{Required: true, PlanModifiers: replace},
			"database":        schema.StringAttribute{Optional: true, PlanModifiers: replace},
			"status":          schema.StringAttribute{Computed: true},
			"attempt_count":   schema.Int64Attribute{Computed: true},
			"created_at":      schema.StringAttribute{Computed: true},
			"updated_at":      schema.StringAttribute{Computed: true},
			"timeouts":        timeouts.Attributes(ctx, timeouts.Opts{Create: true, Read: true, Delete: true}),
		},
	}
}

func (migrationResource *migrationResource) Configure(
	_ context.Context, request resource.ConfigureRequest, response *resource.ConfigureResponse,
) {
	if request.ProviderData == nil {
		return
	}
	client, ok := request.ProviderData.(*Client)
	if !ok {
		response.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *Client, got %T", request.ProviderData))
		return
	}
	migrationResource.client = client
}

func (migrationResource *migrationResource) Create(
	ctx context.Context, request resource.CreateRequest, response *resource.CreateResponse,
) {
	var plan migrationResourceModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	duration, diagnostics := plan.Timeouts.Create(ctx, defaultOperationTimeout)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() {
		return
	}
	operationContext, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	migration, err := migrationResource.client.CreateMigration(operationContext, createRequest(plan), plan.IdempotencyKey.ValueString())
	if err != nil {
		response.Diagnostics.AddError("Unable to create migration", err.Error())
		return
	}
	setMigration(&plan, migration)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	migrationID := migration.ID
	migration, err = migrationResource.client.WaitMigration(operationContext, migrationID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			cleanupContext, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, cancelErr := migrationResource.client.CancelMigration(cleanupContext, migrationID)
			cleanupCancel()
			if cancelErr != nil {
				response.Diagnostics.AddWarning("Migration cancellation was not confirmed", cancelErr.Error())
			}
		}
		response.Diagnostics.AddError("Migration did not complete", err.Error())
		return
	}
	setMigration(&plan, migration)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
	if migration.Status != "succeeded" {
		response.Diagnostics.AddError("Migration failed", fmt.Sprintf("Migration %s reached terminal status %q", migration.ID, migration.Status))
	}
}

func (migrationResource *migrationResource) Read(
	ctx context.Context, request resource.ReadRequest, response *resource.ReadResponse,
) {
	var state migrationResourceModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	duration, diagnostics := state.Timeouts.Read(ctx, 30*time.Second)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() {
		return
	}
	readContext, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	migration, err := migrationResource.client.GetMigration(readContext, state.ID.ValueString())
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		response.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		response.Diagnostics.AddError("Unable to read migration", err.Error())
		return
	}
	setMigration(&state, migration)
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (migrationResource *migrationResource) Update(
	ctx context.Context, request resource.UpdateRequest, response *resource.UpdateResponse,
) {
	var plan migrationResourceModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	migration, err := migrationResource.client.GetMigration(ctx, plan.ID.ValueString())
	if err != nil {
		response.Diagnostics.AddError("Unable to refresh migration during update", err.Error())
		return
	}
	setMigration(&plan, migration)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (migrationResource *migrationResource) Delete(
	ctx context.Context, request resource.DeleteRequest, response *resource.DeleteResponse,
) {
	var state migrationResourceModel
	response.Diagnostics.Append(request.State.Get(ctx, &state)...)
	if response.Diagnostics.HasError() {
		return
	}
	duration, diagnostics := state.Timeouts.Delete(ctx, defaultOperationTimeout)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() {
		return
	}
	deleteContext, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	migration, err := migrationResource.client.GetMigration(deleteContext, state.ID.ValueString())
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		response.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		response.Diagnostics.AddError("Unable to read migration before deletion", err.Error())
		return
	}
	switch migration.Status {
	case "queued", "running":
		migration, err = migrationResource.client.CancelMigration(deleteContext, migration.ID)
		if err != nil {
			response.Diagnostics.AddError("Unable to cancel migration", err.Error())
			return
		}
	case "cancelling":
	default:
		response.State.RemoveResource(ctx)
		return
	}
	if migration.Status != "cancelled" {
		migration, err = migrationResource.client.WaitMigration(deleteContext, migration.ID)
		if err != nil {
			response.Diagnostics.AddError("Migration cancellation did not complete", err.Error())
			return
		}
	}
	response.State.RemoveResource(ctx)
}

func (migrationResource *migrationResource) ImportState(
	ctx context.Context, request resource.ImportStateRequest, response *resource.ImportStateResponse,
) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), request, response)
}

func createRequest(plan migrationResourceModel) CreateMigrationRequest {
	return CreateMigrationRequest{
		Source: Source{RepositoryURL: plan.RepositoryURL.ValueString(), Revision: plan.Revision.ValueString()},
		Destination: Destination{
			Provider: "gcp", ProjectID: plan.ProjectID.ValueString(), Region: plan.Region.ValueString(),
			Runtime: plan.Runtime.ValueString(), Database: plan.Database.ValueString(),
		},
	}
}

func setMigration(state *migrationResourceModel, migration Migration) {
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
