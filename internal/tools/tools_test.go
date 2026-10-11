package tools

import (
	"context"
	"encoding/json"
	"host-scout/internal/model"
	"strings"
	"testing"
)

type fakeTool struct{ name string }

func (f *fakeTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{Name: f.name, Description: "Test tool", Parameters: json.RawMessage("{}")}
}

func TestBoundResultIsExplicitAndSerializable(t *testing.T) {
	r := Bound(Value(strings.Repeat("x", 40000)))
	encoded, err := json.Marshal(r)
	if err != nil || !r.Truncated || len(r.Output) > 32<<10 || !json.Valid(encoded) {
		t.Fatalf("%+v %v", r, err)
	}
	if Bound(Result{}).Code != "invalid_result" {
		t.Fatal("missing terminal status accepted")
	}
}
func (*fakeTool) Run(context.Context, json.RawMessage) (Result, error) { return Value(nil), nil }
func TestRegistryNamesAndSnapshots(t *testing.T) {
	var typedNil *fakeTool
	for _, list := range [][]Tool{{nil}, {typedNil}, {&fakeTool{name: ""}}, {&fakeTool{name: "bad name"}}, {&fakeTool{name: "a"}, &fakeTool{name: "a"}}} {
		if _, err := NewRegistry(list...); err == nil {
			t.Fatal("invalid registry accepted")
		}
	}
	registry, err := NewRegistry(&fakeTool{name: "a"}, &fakeTool{name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	definitions[0].Parameters[0] = 'X'
	if registry.Definitions()[0].Name != "a" || string(registry.Definitions()[0].Parameters) != "{}" {
		t.Fatal("snapshot changed registry")
	}
}
