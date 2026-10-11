//go:build linux

package proc

import (
	"context"
	"encoding/json"
	"host-scout/internal/tools"
	"testing"
)

func TestLiveLinuxProc(t *testing.T) {
	for _, tc := range []struct {
		tool tools.Tool
		args string
	}{{System{}, "{}"}, {Processes{}, `{"limit":5}`}} {
		value, err := tc.tool.Run(context.Background(), json.RawMessage(tc.args))
		if err != nil {
			t.Fatal(err)
		}
		r := value
		if r.Status != tools.Succeeded || !json.Valid(r.Data) {
			t.Fatalf("%+v", r)
		}
	}
}
