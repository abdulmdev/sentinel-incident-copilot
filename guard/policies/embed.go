// Package policies ships the guard's built-in default policy.
package policies

import _ "embed"

//go:embed default.yaml
var Default []byte
