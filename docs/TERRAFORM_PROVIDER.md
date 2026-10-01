# Terraform provider

`terraform-provider-cloudify` is built with HashiCorp's Terraform Plugin
Framework. It turns the control plane's asynchronous migration API into a
Terraform lifecycle while preserving API idempotency and cancellation.

## Provider configuration

```hcl
terraform {
  required_providers {
    cloudify = {
      source = "aritra7/cloudify"
    }
  }
}

provider "cloudify" {
  endpoint = "https://cloudify.example.com"
  # Prefer CLOUDIFY_TOKEN instead of committing token configuration.
}
```

`endpoint` defaults to `CLOUDIFY_ENDPOINT` and then
`http://localhost:8080`. Plain HTTP is accepted only for a loopback endpoint.
`token` defaults to `CLOUDIFY_TOKEN`, is marked sensitive, and is sent as a
Bearer token.

## Migration resource

```hcl
resource "cloudify_migration" "application" {
  idempotency_key = "example-application-main"
  repository_url  = "https://github.com/example/application"
  revision        = "main"
  project_id      = "example-project"
  region          = "us-central1"
  runtime         = "cloud-run"
  database        = "cloud-sql-postgres"

  timeouts = {
    create = "45m"
    read   = "30s"
    delete = "10m"
  }
}
```

Create submits the migration with the configured idempotency key and polls
until it succeeds, fails, is cancelled, or reaches the configured timeout.
Terraform cancellation makes a bounded best-effort request to cancel active
Cloudify work. Source and destination changes replace the migration rather
than mutating an immutable historical operation.

Destroy cancels a queued or running migration and waits for terminal state.
For an already-terminal migration it removes the operation from Terraform
state. It does **not** destroy infrastructure produced by a successful
migration; that requires the planned asynchronous managed-resource deletion
API.

Import uses the migration UUID:

```bash
terraform import cloudify_migration.application MIGRATION_ID
```

## Data source

```hcl
data "cloudify_migration" "application" {
  id = cloudify_migration.application.id
}
```

The resource and data source expose status, attempt count, source and target
properties, plus creation and update timestamps.

## Local development

Build the provider:

```bash
go build -o ./bin/terraform-provider-cloudify ./cmd/terraform-provider-cloudify
```

Configure a Terraform CLI `dev_overrides` entry for
`registry.terraform.io/aritra7/cloudify` pointing to the absolute `bin`
directory, then run the example under `examples/terraform-provider` against a
local control plane. Never commit the CLI configuration or bearer token.
