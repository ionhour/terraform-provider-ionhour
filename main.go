// Command terraform-provider-ionhour is the Terraform provider for Ionhour
// uptime monitoring: projects, HTTP checks and heartbeat jobs managed through
// the Integration API v1.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/ionhour/terraform-provider-ionhour/internal/provider"
)

//go:generate go tool tfplugindocs generate --provider-name ionhour

// version is set by goreleaser (-X main.version=...).
var version = "dev"

// registryAddress is the provider source address on the Terraform Registry.
const registryAddress = "registry.terraform.io/ionhour/ionhour"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: registryAddress,
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
