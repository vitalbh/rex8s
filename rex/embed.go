// Package rexlua holds rex8s.lua, the Rex side of rex8s, so the binary can
// install it (rex8s setup) and tell when the installed copy is outdated.
package rexlua

import _ "embed"

//go:embed rex8s.lua
var Source string
