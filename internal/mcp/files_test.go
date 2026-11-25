package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/youyo/focal/internal/result"
)

const filesConfigBody = "operations:\n  files:\n    enabled: true\n    paths:\n      - /var/log\n"

func TestFilesToolIsListedOnlyWhenEnabled(t *testing.T) {
	url, _ := serverFor(t, "", Options{})
	if slices.Contains(listToolNames(t, url), "inspect_files") {
		t.Error("inspect_files is listed on a default configuration")
	}
	url, _ = serverFor(t, filesConfigBody, Options{})
	if !slices.Contains(listToolNames(t, url), "inspect_files") {
		t.Error("inspect_files is not listed with files enabled")
	}
}

// TestFilesToolMatchesTheConfiguredList pins that the configured list reaches
// the registry: a path under it builds the operation, and one outside it is a
// tool error that never reaches the runner.
func TestFilesToolMatchesTheConfiguredList(t *testing.T) {
	url, runner := serverFor(t, filesConfigBody, Options{})

	res := callTool(t, url, "inspect_files", map[string]any{"host": "web", "path": "/var/log/nginx"})
	if res.IsError {
		t.Fatalf("call reported an error: %+v", res)
	}
	if len(runner.calls) != 1 || runner.calls[0].operation != "files" {
		t.Fatalf("runner calls = %+v, want one files call", runner.calls)
	}

	for _, tc := range []struct {
		path string
		code string
	}{
		{"/etc", "path_not_allowed"},
		{"/var/log2", "path_not_allowed"},
		{"/var/log/../../etc", "invalid_path"},
		{"var/log", "invalid_path"},
		{"/var/log/my app", "invalid_path"},
	} {
		got := toolErrorOf(t, callTool(t, url, "inspect_files", map[string]any{"host": "web", "path": tc.path}))
		if got.Kind != result.KindValidation || got.Code != tc.code {
			t.Errorf("path %q: error = %+v, want %s", tc.path, got, tc.code)
		}
	}
	if len(runner.calls) != 1 {
		t.Errorf("a refused path reached the runner: %+v", runner.calls)
	}
}

// TestFilesToolDescriptionWarnsAboutWhatItReturns pins the two things an agent
// has to know before it reads a listing: names are written by whoever can write
// to the directory, and SSM lists with root's view.
func TestFilesToolDescriptionWarnsAboutWhatItReturns(t *testing.T) {
	url, _ := serverFor(t, filesConfigBody, Options{})
	resp := rpc(t, url, "tools/list", "", nil)
	var out struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	var desc string
	for _, tool := range out.Tools {
		if tool.Name == "inspect_files" {
			desc = tool.Description
		}
	}
	for _, want := range []string{"names", "not instructions", "SSH", "SSM", "root", "0700", "complete"} {
		if !strings.Contains(desc, want) {
			t.Errorf("inspect_files description lacks %q:\n%s", want, desc)
		}
	}
}
