//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Agent-Field/codeaf/internal/store"
)

// TestMissionWindowsHeadlessCLI crosses the process boundary that the in-process
// brain tests do not: the shipped command reads a Windows workspace, talks to a
// provider over loopback, edits the Go source, and leaves a SQLite journal that
// another process context can reopen.
func TestMissionWindowsHeadlessCLI(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the native subprocess proof runs only on Windows")
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "fixture with spaces")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureModule := "module fixture\n\ngo 1.26.5\n"
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte(fixtureModule), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, fixed := "return 41", "return 42"
	answerSource := "package fixture\n\nfunc answer() int {\n\t" + broken + "\n}\n"
	if err := os.WriteFile(filepath.Join(work, "answer.go"), []byte(answerSource), 0o600); err != nil {
		t.Fatal(err)
	}
	testSource := "package fixture\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) {\n\tif got := answer(); got != 42 { t.Fatalf(\"answer() = %d, want 42\", got) }\n}\n"
	if err := os.WriteFile(filepath.Join(work, "answer_test.go"), []byte(testSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := runGo(t, work, "test", "./..."); err == nil || !strings.Contains(string(output), "answer() = 41, want 42") {
		t.Fatalf("fixture did not fail for the expected 41-to-42 assertion: %v\n%s", err, output)
	}

	brain := newScriptedBrain(t)
	oldServer := brain.server
	oldServer.Close()
	brain.gatePasses = true
	brain.editPath = "answer.go"
	var mu sync.Mutex
	var physicalRequests int
	var localCatalogRequests int
	var editSchemas int
	var badRequests []string
	var editResponses int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			if r.Method == http.MethodGet && (r.URL.Path == "/models" || strings.HasPrefix(r.URL.Path, "/models/") && strings.HasSuffix(r.URL.Path, "/endpoints")) {
				mu.Lock()
				localCatalogRequests++
				mu.Unlock()
				http.NotFound(w, r)
				return
			}
			mu.Lock()
			badRequests = append(badRequests, fmt.Sprintf("unexpected endpoint %s %s", r.Method, r.URL.Path))
			mu.Unlock()
			http.NotFound(w, r)
			return
		}
		if ip := net.ParseIP(strings.Split(r.RemoteAddr, ":")[0]); ip == nil || !ip.IsLoopback() {
			mu.Lock()
			badRequests = append(badRequests, "provider connection was not loopback: "+r.RemoteAddr)
			mu.Unlock()
		}
		wire, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, "could not read request", http.StatusBadRequest)
			return
		}
		var request struct {
			Model    string            `json:"model"`
			Messages []json.RawMessage `json:"messages"`
			Tools    []struct {
				Type     string `json:"type"`
				Function struct {
					Name       string         `json:"name"`
					Parameters map[string]any `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
			Stream bool `json:"stream"`
		}
		if err := json.Unmarshal(wire, &request); err != nil {
			mu.Lock()
			badRequests = append(badRequests, "request was not JSON: "+err.Error())
			mu.Unlock()
			http.Error(w, "bad JSON", http.StatusBadRequest)
			return
		}
		if request.Model != "local/test-model" || len(request.Messages) == 0 ||
			r.Header.Get("Authorization") != "Bearer windows-local-test-key" ||
			r.Header.Get("Content-Type") != "application/json" {
			mu.Lock()
			badRequests = append(badRequests, "request missed model, messages, tools, or expected local bearer")
			mu.Unlock()
			http.Error(w, "incomplete completion request", http.StatusBadRequest)
			return
		}
		editAdvertised := false
		for _, tool := range request.Tools {
			if tool.Type != "function" || tool.Function.Name == "" || tool.Function.Parameters["type"] != "object" {
				mu.Lock()
				badRequests = append(badRequests, "request carried a malformed function tool schema")
				mu.Unlock()
				http.Error(w, "malformed tool schema", http.StatusBadRequest)
				return
			}
			if tool.Function.Name == "edit" {
				properties, _ := tool.Function.Parameters["properties"].(map[string]any)
				if !hasStringSchemaProperty(properties, "path") || !hasEditArraySchema(properties, "edits") {
					mu.Lock()
					schema, _ := json.Marshal(tool.Function.Parameters)
					badRequests = append(badRequests, "edit tool schema omitted path or edits[].old/new: "+string(schema))
					mu.Unlock()
					http.Error(w, "incomplete edit schema", http.StatusBadRequest)
					return
				}
				editAdvertised = true
			}
		}
		mu.Lock()
		physicalRequests++
		if editAdvertised {
			editSchemas++
		}
		mu.Unlock()

		text := string(wire)
		var response string
		mu.Lock()
		editNow := editAdvertised && strings.Contains(text, "You complete one piece of work, alone, using tools") && editResponses == 0
		if editNow {
			editResponses++
		}
		mu.Unlock()
		if editNow {
			brain.tally("edited")
			response = brain.tool("edit", fmt.Sprintf(`{"path":%q,"edits":[{"old":%q,"new":%q}]}`, "answer.go", broken, fixed))
		} else {
			response = brain.reply(text)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	brain.server = server
	defer brain.close()

	profile := filepath.Join(t.TempDir(), "profile")
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(t.TempDir(), "journal", "graph.db")
	binary := filepath.Join(t.TempDir(), "codeaf.exe")
	build := exec.Command("go", "build", "-o", binary, "./cmd/codeaf")
	build.Dir = root
	build.Env = isolatedMissionEnv(os.Environ(), map[string]string{
		"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
	})
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build codeaf: %v\n%s", err, output)
	}

	command := exec.CommandContext(context.Background(), binary,
		"do", "--json", "--yes-spend", "--timeout", "90s", "--dir", work, "--db", journal,
		"--model", "local/test-model", "--plan-model", "local/test-model",
		"Update answer.go from 41 to 42 and preserve a passing go test ./... result")
	command.Dir = work
	command.Env = isolatedMissionEnv(os.Environ(), map[string]string{
		"HOME": profile, "CODEAF_HOME": state, "CODEAF_PROFILE_DIR": profile,
		"OPENROUTER_API_KEY": "windows-local-test-key", "CODEAF_BASE_URL": server.URL,
		"CODEAF_MODEL_POOL": "off", "CODEAF_TELEMETRY": "off", "CODEAF_NO_UPDATE_CHECK": "1",
		"CODEAF_PLAN_CONSENT": "0", "CODEAF_DAILY_BUDGET": "0", "CODEAF_PRACTICE_BUDGET": "0",
		"HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "NO_PROXY": "*",
		"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
	})
	stdout, err := command.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		mu.Lock()
		failures := append([]string(nil), badRequests...)
		mu.Unlock()
		t.Fatalf("codeaf do exited unsuccessfully: %v\nprovider failures: %v\nstdout:\n%s\nstderr:\n%s", err, failures, stdout, stderr)
	}
	var result struct {
		OK     bool     `json:"ok"`
		Stop   string   `json:"stop"`
		Answer string   `json:"answer"`
		Files  []string `json:"files"`
		Error  string   `json:"error"`
		Run    string   `json:"run"`
		Calls  int      `json:"calls"`
	}
	if err := json.Unmarshal(stdout, &result); err != nil {
		t.Fatalf("CLI stdout was not one JSON object: %v\n%s", err, stdout)
	}
	if !result.OK || result.Stop != "done" || result.Error != "" || result.Run == "" || result.Calls == 0 {
		t.Fatalf("JSON did not describe completed observed work: %+v\n%s", result, stdout)
	}
	if len(result.Files) == 0 || !strings.Contains(strings.Join(result.Files, "\n"), "answer.go") {
		t.Fatalf("JSON did not include the edited fixture: %+v", result.Files)
	}
	after, err := os.ReadFile(filepath.Join(work, "answer.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), fixed) || strings.Contains(string(after), broken) {
		t.Fatalf("CLI edit did not change 41 to 42:\n%s", after)
	}
	if output, err := runGo(t, work, "test", "./..."); err != nil {
		t.Fatalf("edited fixture still fails go test ./...: %v\n%s", err, output)
	}

	mu.Lock()
	observed, catalog, schemas, failures, edits := physicalRequests, localCatalogRequests, editSchemas, append([]string(nil), badRequests...), editResponses
	mu.Unlock()
	if observed == 0 || schemas == 0 || edits != 1 || len(failures) != 0 {
		t.Fatalf("provider wire proof: requests=%d local catalog requests=%d edit schemas=%d edit responses=%d invalid=%v", observed, catalog, schemas, edits, failures)
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("CLI did not leave its durable SQLite journal: %v", err)
	}
	reopened, err := store.Open(journal)
	if err != nil {
		t.Fatalf("reopen CLI journal: %v", err)
	}
	seq, err := reopened.LatestEventSeq()
	closeErr := reopened.Close()
	if err != nil || closeErr != nil || seq == 0 {
		t.Fatalf("reopened journal has no durable events (seq=%d, read=%v, close=%v)", seq, err, closeErr)
	}
}

func hasStringSchemaProperty(properties map[string]any, name string) bool {
	property, ok := properties[name].(map[string]any)
	return ok && property["type"] == "string"
}

func hasEditArraySchema(properties map[string]any, name string) bool {
	array, ok := properties[name].(map[string]any)
	if !ok || array["type"] != "array" {
		return false
	}
	item, ok := array["items"].(map[string]any)
	if !ok || item["type"] != "object" {
		return false
	}
	nested, ok := item["properties"].(map[string]any)
	if !ok || !hasStringSchemaProperty(nested, "old") || !hasStringSchemaProperty(nested, "new") {
		return false
	}
	required, ok := item["required"].([]any)
	return ok && containsSchemaName(required, "old") && containsSchemaName(required, "new")
}

func containsSchemaName(names []any, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func runGo(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()
	command := exec.Command("go", args...)
	command.Dir = dir
	command.Env = isolatedMissionEnv(os.Environ(), map[string]string{
		"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
	})
	return command.CombinedOutput()
}

func isolatedMissionEnv(inherited []string, overrides map[string]string) []string {
	values := make(map[string]string, len(inherited)+len(overrides))
	for _, entry := range inherited {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[strings.ToUpper(key)] = key + "=" + value
		}
	}
	for key, value := range overrides {
		values[strings.ToUpper(key)] = key + "=" + value
	}
	env := make([]string, 0, len(values))
	for _, value := range values {
		env = append(env, value)
	}
	return env
}
