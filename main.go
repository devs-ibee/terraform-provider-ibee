package main

import (
	"context"
	"flag"
	"log"

	"github.com/devs-ibee/terraform-provider-ibee/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// version is overridden at release time by goreleaser (-X main.version).
var version = "0.5.0"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with debug support")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/devs-ibee/ibee",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
