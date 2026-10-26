package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

// Exercise StartSession, not just command construction: both the configured
// executable and its argument prefix must reach the actual app-server process.
func TestAppServerSession_HonorsConfiguredCommandAndArgs(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CC_CODEX_APP_SERVER_TEST_HELPER", "1")
	// A hardcoded "codex" lookup must fail rather than launching a real agent.
	t.Setenv("PATH", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a := &Agent{
		cmd: executable, cliExtraArgs: []string{"-test.run=^TestAppServerCommandHelper$", "--", "custom-prefix"},
		backend: "app_server", appServerURL: "stdio://", workDir: t.TempDir(), activeIdx: -1,
		model: "test-model", reasoningEffort: "medium", mode: "suggest",
	}
	s, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("configured native app-server failed: %v", err)
	}
	defer s.Close()
	if s.CurrentSessionID() != "configured-native-server" {
		t.Fatalf("unexpected server: %q", s.CurrentSessionID())
	}
}

func TestAppServerCommandHelper(t *testing.T) {
	if os.Getenv("CC_CODEX_APP_SERVER_TEST_HELPER") != "1" {
		return
	}
	i := slices.Index(os.Args, "custom-prefix")
	if i < 0 || i+1 >= len(os.Args) || os.Args[i+1] != "app-server" {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "test"}
		case "thread/start":
			result = map[string]any{"thread": map[string]any{"id": "configured-native-server"}, "model": "test-model", "reasoningEffort": "medium"}
		case "account/rateLimits/read":
			result = map[string]any{"rateLimits": map[string]any{"limitId": "test"}}
		}
		response, _ := json.Marshal(map[string]any{"id": *req.ID, "result": result})
		fmt.Println(string(response))
	}
	if marker := os.Getenv("CC_CODEX_GRACEFUL_CLOSE_MARKER"); marker != "" {
		time.Sleep(100 * time.Millisecond)
		if err := os.WriteFile(marker, []byte("graceful"), 0600); err != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func TestAppServerSession_CloseLetsServerCleanUpBeforeCancellation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := t.TempDir() + "/closed"
	t.Setenv("CC_CODEX_APP_SERVER_TEST_HELPER", "1")
	t.Setenv("CC_CODEX_GRACEFUL_CLOSE_MARKER", marker)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a := &Agent{cmd: executable, cliExtraArgs: []string{"-test.run=^TestAppServerCommandHelper$", "--", "custom-prefix"}, backend: "app_server", appServerURL: "stdio://", workDir: t.TempDir(), activeIdx: -1}
	s, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("server was killed before it could clean up its tool processes")
	}
}
