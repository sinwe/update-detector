// Package openapi embeds the OpenAPI specs served by each service's
// GET /openapi.yaml endpoint, so the committed YAML files are the single
// source of truth rather than a copy baked in separately.
package openapi

import _ "embed"

//go:embed update-detector.yaml
var UpdateDetectorSpec []byte

//go:embed update-aggregator.yaml
var UpdateAggregatorSpec []byte
