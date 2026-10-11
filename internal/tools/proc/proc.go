// Package proc provides bounded, read-only Linux /proc observations.
package proc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tools "host-scout/pkg/tool"
)

const procRoot = "/proc"

type System struct{ root string }

func rootPath(root string) string {
	if root == "" {
		return procRoot
	}
	return root
}

func (System) Definition() tools.Definition {
	return tools.Definition{
		Name:        "inspect_system",
		Description: "Read Linux uptime, load average, and memory totals from /proc. Does not execute a command.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`),
	}
}

func (s System) Run(ctx context.Context, arguments json.RawMessage) (tools.Result, error) {
	if err := requireEmptyObject(arguments); err != nil {
		return tools.Result{Status: tools.Failed, Code: "invalid_arguments"}, nil
	}
	if err := ctx.Err(); err != nil {
		return tools.Result{}, err
	}
	tools.Started(ctx)
	root := rootPath(s.root)
	uptime, err := os.ReadFile(filepath.Join(root, "uptime"))
	if err != nil {
		return tools.Result{}, err
	}
	load, err := os.ReadFile(filepath.Join(root, "loadavg"))
	if err != nil {
		return tools.Result{}, err
	}
	memory, err := os.Open(filepath.Join(root, "meminfo"))
	if err != nil {
		return tools.Result{}, err
	}
	defer memory.Close()
	uptimeValue, err := firstNumber(string(uptime))
	if err != nil {
		return tools.Result{}, err
	}
	loads := strings.Fields(string(load))
	if len(loads) < 3 {
		return tools.Result{}, errors.New("invalid loadavg")
	}
	for _, v := range loads[:3] {
		if _, err := firstNumber(v); err != nil {
			return tools.Result{}, errors.New("invalid loadavg")
		}
	}
	result := map[string]any{
		"uptime_seconds": uptimeValue,
		"load_average":   loads[:3],
		"memory_scope":   "/proc/meminfo may report host memory rather than container cgroup limits",
	}
	found := make(map[string]bool)
	scanner := bufio.NewScanner(memory)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if key != "MemTotal" && key != "MemAvailable" && key != "SwapTotal" && key != "SwapFree" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || len(fields) != 3 || fields[2] != "kB" || found[key] {
			return tools.Result{}, errors.New("invalid meminfo")
		}
		found[key] = true
		result[strings.ToLower(key)+"_kib"] = value
	}
	if err := scanner.Err(); err != nil {
		return tools.Result{}, err
	}
	if len(found) != 4 {
		return tools.Result{}, errors.New("missing core meminfo fields")
	}
	if ctx.Err() != nil {
		return tools.Result{}, ctx.Err()
	}
	return tools.Value(result), nil
}

type Processes struct{ root string }

func (Processes) Definition() tools.Definition {
	return tools.Definition{
		Name:        "list_processes",
		Description: "List up to 50 visible Linux processes sorted by resident memory. Returns PID, name, state and RSS; does not read command-line arguments or environment variables.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":50}},"required":["limit"],"additionalProperties":false}`),
	}
}

type process struct {
	PID    int    `json:"pid"`
	Name   string `json:"name"`
	State  string `json:"state,omitempty"`
	RSSKiB uint64 `json:"rss_kib"`
}

func (p Processes) Run(ctx context.Context, arguments json.RawMessage) (tools.Result, error) {
	var args struct {
		Limit int `json:"limit"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return tools.Result{Status: tools.Failed, Code: "invalid_arguments"}, nil
	}
	if args.Limit < 1 || args.Limit > 50 {
		return tools.Result{Status: tools.Failed, Code: "invalid_arguments"}, nil
	}
	if ctx.Err() != nil {
		return tools.Result{}, ctx.Err()
	}
	tools.Started(ctx)
	root := rootPath(p.root)
	entries, err := os.ReadDir(root)
	if err != nil {
		return tools.Result{}, err
	}
	processes := make([]process, 0, 64)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return tools.Result{}, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		value, err := readProcess(root, pid)
		if err == nil {
			processes = append(processes, value)
		}
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].RSSKiB == processes[j].RSSKiB {
			return processes[i].PID < processes[j].PID
		}
		return processes[i].RSSKiB > processes[j].RSSKiB
	})
	if len(processes) > args.Limit {
		processes = processes[:args.Limit]
	}
	return tools.Value(processes), nil
}

func readProcess(root string, pid int) (process, error) {
	file, err := os.Open(filepath.Join(root, strconv.Itoa(pid), "status"))
	if err != nil {
		return process{}, err
	}
	defer file.Close()
	value := process{PID: pid}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "Name:":
			value.Name = strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "Name:"))
		case "State:":
			value.State = fields[1]
		case "VmRSS:":
			var err error
			value.RSSKiB, err = strconv.ParseUint(fields[1], 10, 64)
			if err != nil || len(fields) != 3 || fields[2] != "kB" {
				return process{}, errors.New("invalid process RSS")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return process{}, fmt.Errorf("read process %d: %w", pid, err)
	}
	if value.Name == "" {
		return process{}, errors.New("process has no name")
	}
	return value, nil
}

func requireEmptyObject(raw json.RawMessage) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if value == nil || len(value) != 0 {
		return errors.New("expected an empty object")
	}
	return nil
}

func decodeStrict(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if err := decoder.Decode(new(any)); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return errors.New("multiple JSON values in arguments")
}

func firstNumber(value string) (float64, error) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, errors.New("missing numeric observation")
	}
	result, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(result) || math.IsInf(result, 0) || result < 0 {
		return 0, errors.New("invalid numeric observation")
	}
	return result, nil
}
