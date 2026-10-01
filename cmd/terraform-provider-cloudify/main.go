package main

import (
	"context"
	"flag"
	"log"

	cloudifyprovider "github.com/Aritra7/cloudify-platform/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "run the provider with debugger support")
	flag.Parse()
	err := providerserver.Serve(
		context.Background(),
		cloudifyprovider.New(version),
		providerserver.ServeOpts{
			Address: "registry.terraform.io/aritra7/cloudify",
			Debug:   *debug,
		},
	)
	if err != nil {
		log.Fatal(err)
	}
}
