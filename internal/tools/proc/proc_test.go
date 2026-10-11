package proc

import (
	"context"
	"encoding/json"
	"host-scout/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, name, value string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func systemFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "uptime", "42.5 12")
	writeFixture(t, root, "loadavg", "0.1 0.2 0.3 1/50 100")
	writeFixture(t, root, "meminfo", "MemTotal: 100 kB\nMemAvailable: 50 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	return root
}
func TestSystemFixtureAndMalformedData(t *testing.T) {
	if _, err := (System{root: filepath.Join(t.TempDir(), "unavailable")}).Run(context.Background(), json.RawMessage("{}")); err == nil {
		t.Fatal("unavailable proc accepted")
	}
	root := systemFixture(t)
	starts := 0
	ctx := tools.WithStart(context.Background(), func() { starts++ })
	value, err := (System{root: root}).Run(ctx, json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	r := value
	var data map[string]any
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if r.Status != tools.Succeeded || starts != 1 || data["uptime_seconds"] != 42.5 || data["memtotal_kib"] != float64(100) || !strings.Contains(data["memory_scope"].(string), "container") {
		t.Fatalf("%+v %#v", r, data)
	}
	for _, tc := range []struct{ name, value string }{
		{"uptime", "invalid"}, {"uptime", "NaN"}, {"loadavg", "1"}, {"loadavg", "1 nope 3"},
		{"meminfo", "MemTotal: nope kB"}, {"meminfo", "MemTotal: 0 kB"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			root := systemFixture(t)
			writeFixture(t, root, tc.name, tc.value)
			if _, err := (System{root: root}).Run(context.Background(), json.RawMessage("{}")); err == nil {
				t.Fatal("malformed core data accepted")
			}
		})
	}
	before := starts
	for _, args := range []string{"null", `{"extra":1}`, "{} {}", "[]"} {
		value, err := (System{root: "missing"}).Run(ctx, json.RawMessage(args))
		if err != nil || value.Code != "invalid_arguments" {
			t.Fatalf("%v %v", value, err)
		}
	}
	if starts != before {
		t.Fatal("invalid arguments accessed host")
	}
}
func TestProcessesSortingLimitsAndDisappearedPID(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ pid, rss string }{{"5", "20"}, {"2", "20"}, {"9", "100"}} {
		writeFixture(t, root, tc.pid+"/status", "Name:\ttest process\nState:\tS (sleeping)\nVmRSS:\t"+tc.rss+" kB\n")
		// These are intentionally invalid; the tool must never open either file.
		writeFixture(t, root, tc.pid+"/cmdline", "SECRET-CMDLINE")
		writeFixture(t, root, tc.pid+"/environ", "SECRET-ENV")
	}
	_ = os.Mkdir(filepath.Join(root, "6"), 0700) // vanished before status was read
	writeFixture(t, root, "7/status", "Name:\tbad\nVmRSS:\tnot-a-number kB\n")
	value, err := (Processes{root: root}).Run(context.Background(), json.RawMessage(`{"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	r := value
	var data []process
	_ = json.Unmarshal(r.Data, &data)
	if len(data) != 2 || data[0].PID != 9 || data[1].PID != 2 || data[0].Name != "test process" || strings.Contains(string(r.Data), "SECRET") {
		t.Fatalf("%s", r.Data)
	}
	for _, args := range []string{`{"limit":0}`, `{"limit":51}`, `{"limit":1.2}`, `{"limit":2,"extra":1}`, "{}", "null", `{"limit":2} {}`} {
		starts := 0
		value, err := (Processes{root: "missing"}).Run(tools.WithStart(context.Background(), func() { starts++ }), json.RawMessage(args))
		if err != nil || value.Code != "invalid_arguments" || starts != 0 {
			t.Fatalf("%s: %v %v starts=%d", args, value, err, starts)
		}
	}
}
