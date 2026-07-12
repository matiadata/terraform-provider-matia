package provider

import "github.com/hashicorp/terraform-plugin-framework/resource"

func NewDestinationResource() resource.Resource {
	return &assetResource{kind: assetKindDestination}
}
