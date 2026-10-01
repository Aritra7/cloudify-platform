terraform {
  required_providers {
    cloudify = {
      source = "aritra7/cloudify"
    }
  }
}

provider "cloudify" {
  endpoint = var.cloudify_endpoint
}

variable "cloudify_endpoint" {
  type        = string
  description = "Cloudify control-plane URL"
  default     = "http://localhost:8080"
}

variable "managed_resource_id" {
  type        = string
  description = "Managed-resource UUID projected after an applied Cloudify plan"
}

resource "cloudify_migration" "demo" {
  idempotency_key = "terraform-provider-demo-main"
  repository_url  = "https://github.com/example/application"
  revision        = "main"
  project_id      = "example-project"
  region          = "us-central1"
  runtime         = "cloud-run"

  timeouts = {
    create = "45m"
    read   = "30s"
    delete = "10m"
  }
}

data "cloudify_migration" "demo" {
  id = cloudify_migration.demo.id
}

output "migration_status" {
  value = data.cloudify_migration.demo.status
}

resource "cloudify_resource" "demo" {
  id = var.managed_resource_id

  timeouts = {
    create = "30s"
    read   = "30s"
    delete = "30m"
  }
}

output "resource_reconciliation_state" {
  value = cloudify_resource.demo.state
}
