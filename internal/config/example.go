package config

import _ "embed"

// ExampleYAML is the default config the binary writes to ~/.cax/config.yaml
// on first run. It mirrors the spec's documented shape with placeholder
// credentials/model IDs the user fills in.
//
//go:embed example.yaml
var ExampleYAML []byte
