package tool_test

import (
	"context"
	"encoding/json"
	"testing"

	"host-scout/pkg/tool"
)

type fake struct {
	def    tool.Definition
	called bool
}

func (f *fake) Definition() tool.Definition { return f.def }
func (f *fake) Run(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	f.called = true
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	tool.Started(ctx)
	return tool.Value(map[string]string{"arguments": string(args)}), nil
}

func TestRegistryRejectsInvalidDefinitions(t *testing.T) {
	valid := tool.Definition{Name: "probe", Description: "Probe", Parameters: json.RawMessage(`{"type":"object"}`)}
	for _, items := range [][]tool.Tool{
		{nil}, {(*fake)(nil)},
		{&fake{def: tool.Definition{}}},
		{&fake{def: tool.Definition{Name: "bad name", Description: "x", Parameters: valid.Parameters}}},
		{&fake{def: tool.Definition{Name: "x", Parameters: valid.Parameters}}},
		{&fake{def: tool.Definition{Name: "x", Description: "x", Parameters: json.RawMessage("[")}}},
		{&fake{def: valid}, &fake{def: valid}},
	} {
		if _, err := tool.NewRegistry(items...); err == nil {
			t.Fatal("accepted invalid tool registry")
		}
	}
}

func TestRegistryDispatchAndSnapshots(t *testing.T) {
	f := &fake{def: tool.Definition{Name: "probe", Description: "Probe", Parameters: json.RawMessage(`{"type":"object"}`)}}
	r, err := tool.NewRegistry(f)
	if err != nil {
		t.Fatal(err)
	}
	f.def.Parameters[0] = 'X'
	defs := r.Definitions()
	if string(defs[0].Parameters) != `{"type":"object"}` {
		t.Fatal("definition changed after registration")
	}
	defs[0].Parameters[0] = 'X'
	if string(r.Definitions()[0].Parameters) != `{"type":"object"}` {
		t.Fatal("definition snapshot leaked")
	}
	started := 0
	ctx := tool.WithStart(context.Background(), func() { started++ })
	result, err := r.Run(ctx, "probe", json.RawMessage(`{}`))
	if err != nil || result.Status != tool.Succeeded || started != 1 || !f.called {
		t.Fatalf("dispatch: %+v, %v", result, err)
	}
	result, err = r.Run(ctx, "missing", nil)
	if err != nil || result.Code != "unknown_tool" || started != 1 {
		t.Fatalf("unknown: %+v, %v", result, err)
	}
}
