package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBatchJSONAndJSONL(t *testing.T) {
	jsonArray := []byte(`[
		{"title":"t1","spec":"s1","verify":"go test ./...","model_hint":"codex"},
		{"title":"t2","spec":"s2","verify":["echo ok","go build ./..."],"model_hint":"codex"}
	]`)
	got, err := parseBatch(jsonArray)
	if err != nil {
		t.Fatalf("parseBatch(json array) error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(got))
	}
	if len(got[0].Verify) != 1 || got[0].Verify[0] != "go test ./..." {
		t.Fatalf("unexpected verify parsing for first task: %#v", got[0].Verify)
	}
	if len(got[1].Verify) != 2 {
		t.Fatalf("expected 2 verify commands for second task, got %d", len(got[1].Verify))
	}

	jsonl := []byte("{\"title\":\"a\",\"spec\":\"b\",\"verify\":\"echo 1\",\"model_hint\":\"codex\"}\n{\"title\":\"c\",\"spec\":\"d\",\"verify\":[\"echo 2\"],\"model_hint\":\"codex\"}\n")
	got, err = parseBatch(jsonl)
	if err != nil {
		t.Fatalf("parseBatch(jsonl) error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 tasks from jsonl, got %d", len(got))
	}
}

func TestNormalizeInputModelHintRequired(t *testing.T) {
	// model_hint present -> succeeds
	_, err := normalizeInput(taskInputRaw{
		Title:     "t",
		Spec:      "s",
		Verify:    []byte(`"echo ok"`),
		ModelHint: "codex",
	})
	if err != nil {
		t.Fatalf("expected success with model_hint set, got: %v", err)
	}

	// model_hint empty -> error
	_, err = normalizeInput(taskInputRaw{
		Title:  "t",
		Spec:   "s",
		Verify: []byte(`"echo ok"`),
	})
	if err == nil || !strings.Contains(err.Error(), "model_hint is required") {
		t.Fatalf("expected error containing 'model_hint is required', got: %v", err)
	}

	// model_hint whitespace-only -> error
	_, err = normalizeInput(taskInputRaw{
		Title:     "t",
		Spec:      "s",
		Verify:    []byte(`"echo ok"`),
		ModelHint: "   ",
	})
	if err == nil || !strings.Contains(err.Error(), "model_hint is required") {
		t.Fatalf("expected error for whitespace-only model_hint, got: %v", err)
	}
}

func TestNextRunnableTaskIndex(t *testing.T) {
	tasks := []Task{
		{ID: "OB-001", Status: statusDone},
		{ID: "OB-002", Status: statusFailed, Attempts: 1},
		{ID: "OB-003", Status: statusTodo},
	}
	idx := nextRunnableTaskIndex(tasks, 2)
	if idx != 2 {
		t.Fatalf("expected todo task index 2 first, got %d", idx)
	}

	tasks = []Task{
		{ID: "OB-001", Status: statusDone},
		{ID: "OB-002", Status: statusFailed, Attempts: 1},
		{ID: "OB-003", Status: statusBlocked, Attempts: 2},
	}
	idx = nextRunnableTaskIndex(tasks, 2)
	if idx != 1 {
		t.Fatalf("expected failed retry task index 1, got %d", idx)
	}
}

func TestNextRunnableTaskIndexCustomMaxAttempts(t *testing.T) {
	tasks := []Task{
		{ID: "OB-001", Status: statusFailed, Attempts: 2},
		{ID: "OB-002", Status: statusFailed, Attempts: 3},
	}
	// With maxAttempts=2, OB-001 is at the limit so not runnable.
	idx := nextRunnableTaskIndex(tasks, 2)
	if idx != -1 {
		t.Fatalf("expected -1 with maxAttempts=2, got %d", idx)
	}
	// With maxAttempts=4, both are runnable; first one wins.
	idx = nextRunnableTaskIndex(tasks, 4)
	if idx != 0 {
		t.Fatalf("expected 0 with maxAttempts=4, got %d", idx)
	}
}

func TestIsTransientFailure(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"rate_limit", true},
		{"provider_unavailable", true},
		{"quota", false},
		{"billing", false},
		{"auth", false},
		{"model_unavailable", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isTransientFailure(tc.reason); got != tc.want {
			t.Fatalf("isTransientFailure(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}

func TestStaleInProgressRecovery(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tasks.jsonl")
	tasks := []Task{
		{ID: "OB-001", Status: statusInProgress, UpdatedAt: "2026-01-01T00:00:00Z"},
		{ID: "OB-002", Status: statusTodo, UpdatedAt: "2026-01-01T00:00:00Z"},
		{ID: "OB-003", Status: statusInProgress, UpdatedAt: "2026-01-01T00:00:00Z", LastError: "old error"},
	}
	if err := saveTasks(p, tasks); err != nil {
		t.Fatalf("saveTasks: %v", err)
	}

	// Simulate the recovery logic from cmdGo.
	loaded, err := loadTasks(p)
	if err != nil {
		t.Fatalf("loadTasks: %v", err)
	}
	recovered := false
	for i := range loaded {
		if loaded[i].Status == statusInProgress {
			loaded[i].Status = statusTodo
			loaded[i].LastError = ""
			loaded[i].UpdatedAt = nowUTC()
			recovered = true
		}
	}
	if !recovered {
		t.Fatalf("expected recovery of stale in_progress tasks")
	}
	if err := saveTasks(p, loaded); err != nil {
		t.Fatalf("saveTasks after recovery: %v", err)
	}

	// Verify results.
	final, err := loadTasks(p)
	if err != nil {
		t.Fatalf("loadTasks after save: %v", err)
	}
	for _, tk := range final {
		if tk.Status == statusInProgress {
			t.Fatalf("task %s still in_progress after recovery", tk.ID)
		}
	}
	if final[0].Status != statusTodo {
		t.Fatalf("OB-001 should be todo, got %s", final[0].Status)
	}
	if final[2].LastError != "" {
		t.Fatalf("OB-003 last_error should be cleared, got %q", final[2].LastError)
	}
}

func TestClassifyProviderFailure(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want string
	}{
		{name: "rate", msg: "429 Too Many Requests", want: "rate_limit"},
		{name: "quota", msg: "usage limit exceeded", want: "quota"},
		{name: "billing", msg: "billing issue", want: "billing"},
		{name: "model", msg: "unknown model", want: "model_unavailable"},
		{name: "provider", msg: "service unavailable", want: "provider_unavailable"},
		{name: "auth", msg: "unauthorized", want: "auth"},
		{name: "other", msg: "syntax error", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyProviderFailure(os.ErrPermission, tc.msg)
			if got != tc.want {
				t.Fatalf("classifyProviderFailure(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

func TestFindTaskIndex(t *testing.T) {
	tasks := []Task{
		{ID: "OB-001"},
		{ID: "OB-002"},
	}
	if got := findTaskIndex(tasks, "OB-002"); got != 1 {
		t.Fatalf("findTaskIndex returned %d, want 1", got)
	}
	if got := findTaskIndex(tasks, "OB-404"); got != -1 {
		t.Fatalf("findTaskIndex returned %d, want -1", got)
	}
}

func TestLoadRuns(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "runs.jsonl")
	content := `{"task_id":"OB-001","status":"done","finished_at":"2026-02-17T00:00:00Z"}
{"task_id":"OB-002","status":"failed","finished_at":"2026-02-17T00:01:00Z"}`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write runs file: %v", err)
	}
	runs, err := loadRuns(p)
	if err != nil {
		t.Fatalf("loadRuns error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[1].TaskID != "OB-002" {
		t.Fatalf("unexpected second run task id: %q", runs[1].TaskID)
	}
}

func TestAppendCycleSummaryLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cycle.log")

	if err := appendCycleSummaryLine(p, "alpha", 3, 2, 1, 0, []string{"OB-001", "OB-002"}, false); err != nil {
		t.Fatalf("appendCycleSummaryLine error: %v", err)
	}

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read cycle log: %v", err)
	}
	line := string(b)
	checks := []string{
		"instance=alpha",
		"processed=3",
		"done=2",
		"failed=1",
		"blocked=0",
		"dry_run=false",
		"task_ids=OB-001,OB-002",
	}
	for _, s := range checks {
		if !strings.Contains(line, s) {
			t.Fatalf("expected cycle log to contain %q, got %q", s, line)
		}
	}
}

func TestResolveInstanceDirFromCWD(t *testing.T) {
	tmp := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(orig)
	}()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}

	instDir := filepath.Join(tmp, ".obliviate", "state", "alpha")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatalf("mkdir inst dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "instance.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write instance metadata: %v", err)
	}

	got, err := resolveInstanceDir("alpha")
	if err != nil {
		t.Fatalf("resolveInstanceDir error: %v", err)
	}
	if got != instDir {
		t.Fatalf("resolveInstanceDir = %q, want %q", got, instDir)
	}
}

func TestBuildExecutionPromptIncludesGlobalPrompt(t *testing.T) {
	home := t.TempDir()
	instance := "test-inst"
	instDir := filepath.Join(home, "state", instance)
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	globalContent := "Use slog not log. Wrap errors with fmt.Errorf."
	if err := os.WriteFile(filepath.Join(home, "global-prompt.md"), []byte(globalContent), 0o644); err != nil {
		t.Fatalf("write global-prompt.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "prompt.md"), []byte("instance rules here"), 0o644); err != nil {
		t.Fatalf("write prompt.md: %v", err)
	}

	task := Task{ID: "OB-001", Title: "test task", Spec: "do stuff", Status: statusTodo}
	prompt, err := buildExecutionPrompt(home, instance, task)
	if err != nil {
		t.Fatalf("buildExecutionPrompt error: %v", err)
	}
	if !strings.Contains(prompt, globalContent) {
		t.Fatalf("prompt should contain global-prompt.md content, got:\n%s", prompt)
	}
	globalIdx := strings.Index(prompt, "## Global Prompt")
	instanceIdx := strings.Index(prompt, "## Instance Prompt")
	if globalIdx == -1 || instanceIdx == -1 {
		t.Fatalf("expected both Global Prompt and Instance Prompt sections")
	}
	if globalIdx >= instanceIdx {
		t.Fatalf("Global Prompt (at %d) should appear before Instance Prompt (at %d)", globalIdx, instanceIdx)
	}
}

func TestBuildExecutionPromptMissingGlobalPrompt(t *testing.T) {
	home := t.TempDir()
	instance := "test-inst"
	instDir := filepath.Join(home, "state", instance)
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	task := Task{ID: "OB-001", Title: "test task", Spec: "do stuff", Status: statusTodo}
	prompt, err := buildExecutionPrompt(home, instance, task)
	if err != nil {
		t.Fatalf("buildExecutionPrompt error: %v", err)
	}
	if !strings.Contains(prompt, "## Global Prompt") {
		t.Fatalf("prompt should contain ## Global Prompt section heading even when file is missing")
	}
}
