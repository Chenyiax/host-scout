package tool_test

import (
	"context"
	"encoding/json"
	"fmt"

	"host-scout/pkg/tool"
)

// A program can register its own tool at startup and pass the registry to the
// host application. The tool needs only this public package.
func ExampleNewRegistry() {
	registry, err := tool.NewRegistry(&fake{def: tool.Definition{
		Name: "probe", Description: "Return a sample value",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}})
	if err != nil {
		panic(err)
	}
	result, err := registry.Run(context.Background(), "probe", json.RawMessage(`{}`))
	fmt.Println(registry.Definitions()[0].Name, result.Status, err)
	// Output: probe succeeded <nil>
}
