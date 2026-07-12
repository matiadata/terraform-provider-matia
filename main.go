package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/matiadata/terraform-provider-matia/internal/provider"
)

var version string = "dev"

func main() {
	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/matiadata/matia",
	}

	err := providerserver.Serve(context.Background(), provider.New(version), opts)
	if err != nil {
		slog.Error("failed to serve provider", "error", err)
		os.Exit(1)
	}
}
