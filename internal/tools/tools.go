// Package tools keeps the existing internal import path while the extension
// contract lives in pkg/tool.
package tools

import "host-scout/pkg/tool"

type Tool = tool.Tool
type Registry = tool.Registry
type Definition = tool.Definition

var NewRegistry = tool.NewRegistry
var WithStart = tool.WithStart
var Started = tool.Started
