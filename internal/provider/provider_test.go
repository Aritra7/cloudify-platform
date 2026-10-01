package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

func TestProtocolSchemaIsValid(t *testing.T) {
	t.Parallel()
	server := providerserver.NewProtocol6(New("test")())()
	response, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("get provider schema: %v", err)
	}
	for _, diagnostic := range response.Diagnostics {
		if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("schema diagnostic: %s: %s", diagnostic.Summary, diagnostic.Detail)
		}
	}
	if response.Provider == nil {
		t.Fatal("provider schema is nil")
	}
	if response.ResourceSchemas["cloudify_migration"] == nil {
		t.Fatal("cloudify_migration resource schema is missing")
	}
	if response.DataSourceSchemas["cloudify_migration"] == nil {
		t.Fatal("cloudify_migration data source schema is missing")
	}
	if response.ResourceSchemas["cloudify_resource"] == nil {
		t.Fatal("cloudify_resource resource schema is missing")
	}
	if response.DataSourceSchemas["cloudify_resource"] == nil {
		t.Fatal("cloudify_resource data source schema is missing")
	}
}
