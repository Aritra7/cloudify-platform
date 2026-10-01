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

type managedResourceResource struct{ client *Client }

type managedResourceModel struct {
	ID                  types.String   `tfsdk:"id"`
	Kind                types.String   `tfsdk:"kind"`
	MigrationID         types.String   `tfsdk:"migration_id"`
	SourcePlanID        types.String   `tfsdk:"source_plan_id"`
	ProjectID           types.String   `tfsdk:"project_id"`
	Region              types.String   `tfsdk:"region"`
	Name                types.String   `tfsdk:"name"`
	Desired             types.String   `tfsdk:"desired"`
	Observed            types.String   `tfsdk:"observed"`
	State               types.String   `tfsdk:"state"`
	RemediationPolicy   types.String   `tfsdk:"remediation_policy"`
	Lifecycle           types.String   `tfsdk:"lifecycle_status"`
	Generation          types.Int64    `tfsdk:"generation"`
	ObservedGeneration  types.Int64    `tfsdk:"observed_generation"`
	RetryCount          types.Int64    `tfsdk:"retry_count"`
	DeleteFailure       types.String   `tfsdk:"delete_failure"`
	DeletionRequestedBy types.String   `tfsdk:"deletion_requested_by"`
	DeletionRequestedAt types.String   `tfsdk:"deletion_requested_at"`
	DeletedAt           types.String   `tfsdk:"deleted_at"`
	CreatedAt           types.String   `tfsdk:"created_at"`
	UpdatedAt           types.String   `tfsdk:"updated_at"`
	Timeouts            timeouts.Value `tfsdk:"timeouts"`
}

var (
	_ resource.Resource                = &managedResourceResource{}
	_ resource.ResourceWithConfigure   = &managedResourceResource{}
	_ resource.ResourceWithImportState = &managedResourceResource{}
)

func NewManagedResourceResource() resource.Resource { return &managedResourceResource{} }

func (managed *managedResourceResource) Metadata(
	_ context.Context, request resource.MetadataRequest, response *resource.MetadataResponse,
) {
	response.TypeName = request.ProviderTypeName + "_resource"
}

func (managed *managedResourceResource) Schema(
	ctx context.Context, _ resource.SchemaRequest, response *resource.SchemaResponse,
) {
	response.Schema = schema.Schema{
		MarkdownDescription: "Adopts an existing Cloudify managed resource and controls its asynchronous Terraform-backed deletion.",
		Attributes: map[string]schema.Attribute{
			"id":                    schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"kind":                  schema.StringAttribute{Computed: true},
			"migration_id":          schema.StringAttribute{Computed: true},
			"source_plan_id":        schema.StringAttribute{Computed: true},
			"project_id":            schema.StringAttribute{Computed: true},
			"region":                schema.StringAttribute{Computed: true},
			"name":                  schema.StringAttribute{Computed: true},
			"desired":               schema.StringAttribute{Computed: true},
			"observed":              schema.StringAttribute{Computed: true},
			"state":                 schema.StringAttribute{Computed: true},
			"remediation_policy":    schema.StringAttribute{Computed: true},
			"lifecycle_status":      schema.StringAttribute{Computed: true},
			"generation":            schema.Int64Attribute{Computed: true},
			"observed_generation":   schema.Int64Attribute{Computed: true},
			"retry_count":           schema.Int64Attribute{Computed: true},
			"delete_failure":        schema.StringAttribute{Computed: true},
			"deletion_requested_by": schema.StringAttribute{Computed: true},
			"deletion_requested_at": schema.StringAttribute{Computed: true},
			"deleted_at":            schema.StringAttribute{Computed: true},
			"created_at":            schema.StringAttribute{Computed: true},
			"updated_at":            schema.StringAttribute{Computed: true},
			"timeouts":              timeouts.Attributes(ctx, timeouts.Opts{Create: true, Read: true, Delete: true}),
		},
	}
}

func (managed *managedResourceResource) Configure(
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
	managed.client = client
}

func (managed *managedResourceResource) Create(
	ctx context.Context, request resource.CreateRequest, response *resource.CreateResponse,
) {
	var plan managedResourceModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	duration, diagnostics := plan.Timeouts.Create(ctx, 30*time.Second)
	response.Diagnostics.Append(diagnostics...)
	if response.Diagnostics.HasError() {
		return
	}
	readContext, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	remote, err := managed.client.GetResource(readContext, plan.ID.ValueString())
	if err != nil {
		response.Diagnostics.AddError("Unable to adopt managed resource", err.Error())
		return
	}
	setManagedResource(&plan, remote)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (managed *managedResourceResource) Read(
	ctx context.Context, request resource.ReadRequest, response *resource.ReadResponse,
) {
	var state managedResourceModel
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
	remote, err := managed.client.GetResource(readContext, state.ID.ValueString())
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		response.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		response.Diagnostics.AddError("Unable to read managed resource", err.Error())
		return
	}
	setManagedResource(&state, remote)
	response.Diagnostics.Append(response.State.Set(ctx, &state)...)
}

func (managed *managedResourceResource) Update(
	ctx context.Context, request resource.UpdateRequest, response *resource.UpdateResponse,
) {
	var plan managedResourceModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &plan)...)
	if response.Diagnostics.HasError() {
		return
	}
	remote, err := managed.client.GetResource(ctx, plan.ID.ValueString())
	if err != nil {
		response.Diagnostics.AddError("Unable to refresh managed resource", err.Error())
		return
	}
	setManagedResource(&plan, remote)
	response.Diagnostics.Append(response.State.Set(ctx, &plan)...)
}

func (managed *managedResourceResource) Delete(
	ctx context.Context, request resource.DeleteRequest, response *resource.DeleteResponse,
) {
	var state managedResourceModel
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
	remote, err := managed.client.DeleteResource(deleteContext, state.ID.ValueString())
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		response.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		response.Diagnostics.AddError("Unable to request managed resource deletion", err.Error())
		return
	}
	if remote.Lifecycle != "deleted" {
		remote, err = managed.client.WaitResourceDeleted(deleteContext, remote.ID)
		if err != nil {
			response.Diagnostics.AddError("Managed resource deletion did not complete", err.Error())
			return
		}
	}
	response.State.RemoveResource(ctx)
}

func (managed *managedResourceResource) ImportState(
	ctx context.Context, request resource.ImportStateRequest, response *resource.ImportStateResponse,
) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), request, response)
}

func setManagedResource(state *managedResourceModel, remote ManagedResource) {
	state.ID = types.StringValue(remote.ID)
	state.Kind = types.StringValue(remote.Kind)
	state.MigrationID = types.StringValue(remote.MigrationID)
	state.SourcePlanID = types.StringValue(remote.SourcePlanID)
	state.ProjectID = types.StringValue(remote.ProjectID)
	state.Region = types.StringValue(remote.Region)
	state.Name = types.StringValue(remote.Name)
	state.Desired = nullableString(string(remote.Desired))
	state.Observed = nullableString(string(remote.Observed))
	state.State = types.StringValue(remote.State)
	state.RemediationPolicy = types.StringValue(remote.RemediationPolicy)
	state.Lifecycle = types.StringValue(remote.Lifecycle)
	state.Generation = types.Int64Value(remote.Generation)
	state.ObservedGeneration = types.Int64Value(remote.ObservedGeneration)
	state.RetryCount = types.Int64Value(remote.RetryCount)
	state.DeleteFailure = nullableString(remote.DeleteFailure)
	state.DeletionRequestedBy = nullableString(remote.DeletionRequestedBy)
	state.DeletionRequestedAt = nullableTime(remote.DeletionRequestedAt)
	state.DeletedAt = nullableTime(remote.DeletedAt)
	state.CreatedAt = types.StringValue(remote.CreatedAt.UTC().Format(time.RFC3339Nano))
	state.UpdatedAt = types.StringValue(remote.UpdatedAt.UTC().Format(time.RFC3339Nano))
}

func nullableString(value string) types.String {
	if value == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}

func nullableTime(value *time.Time) types.String {
	if value == nil {
		return types.StringNull()
	}
	return types.StringValue(value.UTC().Format(time.RFC3339Nano))
}
