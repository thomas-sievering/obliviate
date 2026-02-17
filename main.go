package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	statusTodo       = "todo"
	statusInProgress = "in_progress"
	statusDone       = "done"
	statusFailed     = "failed"
	statusBlocked    = "blocked"
	maxAttempts      = 2
)

const (
	exitOK         = 0
	exitUsage      = 2
	exitValidation = 3
	exitNotFound   = 4
	exitRuntime    = 10
	lockWaitMax    = 15 * time.Second
	lockWaitStep   = 150 * time.Millisecond
	agentTimeout   = 15 * time.Minute
	verifyTimeout  = 2 * time.Minute
)

type Task struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Spec      string   `json:"spec"`
	Verify    []string `json:"verify"`
	Status    string   `json:"status"`
	ModelHint string   `json:"model_hint,omitempty"`
	Priority  string   `json:"priority,omitempty"`
	Attempts  int      `json:"attempts"`
	LastError string   `json:"last_error,omitempty"`
	Source    string   `json:"source,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type InstanceMeta struct {
	Name      string `json:"name"`
	Workdir   string `json:"workdir"`
	CreatedAt string `json:"created_at"`
}

type RunLog struct {
	TaskID           string `json:"task_id"`
	Status           string `json:"status"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	PrimaryProvider  string `json:"primary_provider,omitempty"`
	PrimaryModel     string `json:"primary_model,omitempty"`
	FallbackProvider string `json:"fallback_provider,omitempty"`
	FallbackModel    string `json:"fallback_model,omitempty"`
	FallbackReason   string `json:"fallback_reason,omitempty"`
	StartedAt        string `json:"started_at"`
	FinishedAt       string `json:"finished_at"`
	Error            string `json:"error,omitempty"`
	OutputTail       string `json:"output_tail,omitempty"`
	VerifyFailed     string `json:"verify_failed,omitempty"`
}

type fallbackAttempt struct {
	PrimaryProvider  string
	PrimaryModel     string
	FallbackProvider string
	FallbackModel    string
	Reason           string
}

type goResult struct {
	Instance  string   `json:"instance"`
	Processed int      `json:"processed"`
	Done      int      `json:"done"`
	Failed    int      `json:"failed"`
	Blocked   int      `json:"blocked"`
	TaskIDs   []string `json:"task_ids,omitempty"`
}

type runsResult struct {
	Instance string   `json:"instance"`
	Count    int      `json:"count"`
	Runs     []RunLog `json:"runs"`
}

type taskInputRaw struct {
	Title     string          `json:"title"`
	Spec      string          `json:"spec"`
	Verify    json.RawMessage `json:"verify"`
	ModelHint string          `json:"model_hint"`
	Priority  string          `json:"priority"`
	Source    string          `json:"source"`
}

type taskInput struct {
	Title     string
	Spec      string
	Verify    []string
	ModelHint string
	Priority  string
	Source    string
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("empty value")
	}
	*s = append(*s, v)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]
	var err error

	switch cmd {
	case "init":
		err = cmdInit(args)
	case "add":
		err = cmdAdd(args)
	case "add-batch":
		err = cmdAddBatch(args)
	case "status":
		err = cmdStatus(args)
	case "show":
		err = cmdShow(args)
	case "reset":
		err = cmdReset(args)
	case "skip":
		err = cmdSkip(args)
	case "runs":
		err = cmdRuns(args)
	case "go":
		err = cmdGo(args)
	case "help", "-h", "--help":
		printUsage()
		return
	default:
		err = fmt.Errorf("usage: unknown command: %s", cmd)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(classifyExitCode(err))
	}
	os.Exit(exitOK)
}

func printUsage() {
	fmt.Println(`obliviate - fresh-context task loop runner

Usage:
  obliviate init <instance> [--workdir .]
  obliviate add <instance> --title "..." --spec "..." --verify "cmd" --model "hint" [--json]
  obliviate add-batch <instance> [--file tasks.json|tasks.jsonl] [--stdin] [--json]
  obliviate status [instance] [--json]
  obliviate show <instance> <task-id> [--json]
  obliviate reset <instance> <task-id> [--json]
  obliviate skip <instance> <task-id> [--reason "..." ] [--json]
  obliviate runs <instance> [--limit N] [--task-id OB-001] [--json]
  obliviate go <instance> [--limit N] [--dry-run] [--require-commit] [--json]`)
	fmt.Println(`
Exit codes:
  0  success
  2  usage error
  3  validation error
  4  not found / not initialized
  10 runtime error`)
}

func cmdInit(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: obliviate init <instance> [--workdir .]")
	}
	instance := args[0]

	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	workdir := fs.String("workdir", ".", "repo-relative workdir for this instance")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	projectRoot, err := resolveProjectRootFromWorkdir(*workdir)
	if err != nil {
		return err
	}
	home := projectObliviateHome(projectRoot)
	if err := ensureDir(home); err != nil {
		return err
	}

	stateDir := projectStateDir(projectRoot)
	instDir := filepath.Join(stateDir, instance)
	if err := ensureDir(instDir); err != nil {
		return err
	}

	now := nowUTC()
	meta := InstanceMeta{Name: instance, Workdir: ".", CreatedAt: now}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")

	if err := writeIfMissing(filepath.Join(instDir, "instance.json"), string(metaBytes)+"\n"); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(instDir, "prompt.md"), defaultPrompt(instance)); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(instDir, "spec.md"), "# Feature Spec\n\nDescribe the target feature here.\n"); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(instDir, "learnings.md"), "# Learnings\n"); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(instDir, "tasks.jsonl"), ""); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(instDir, "runs.jsonl"), ""); err != nil {
		return err
	}
	if err := writeIfMissing(filepath.Join(home, "global-learnings.md"), "# Global Learnings\n"); err != nil {
		return err
	}

	fmt.Printf("initialized instance %q at %s\n", instance, instDir)
	return nil
}

func cmdAdd(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: obliviate add <instance> --title ... --spec ... --verify ...")
	}
	instance := args[0]

	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	title := fs.String("title", "", "task title")
	spec := fs.String("spec", "", "task spec")
	modelHint := fs.String("model", "", "model hint (codex, claude-sonnet, claude-opus, ...) ")
	priority := fs.String("priority", "med", "priority")
	source := fs.String("source", "agent", "source")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	var verify stringList
	fs.Var(&verify, "verify", "verification command (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*title) == "" || strings.TrimSpace(*spec) == "" || len(verify) == 0 {
		return errors.New("title, spec, and at least one --verify are required")
	}
	if strings.TrimSpace(*modelHint) == "" {
		return errors.New("model_hint is required (use --model to specify)")
	}

	task := taskInput{
		Title:     *title,
		Spec:      *spec,
		Verify:    verify,
		ModelHint: *modelHint,
		Priority:  *priority,
		Source:    *source,
	}
	added, err := addTasks(instance, []taskInput{task})
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(added[0])
	}
	fmt.Printf("added %d task: %s\n", len(added), added[0].ID)
	return nil
}

func cmdAddBatch(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: obliviate add-batch <instance> [--file ...] [--stdin]")
	}
	instance := args[0]

	fs := flag.NewFlagSet("add-batch", flag.ContinueOnError)
	filePath := fs.String("file", "", "input file (json array or jsonl)")
	readStdin := fs.Bool("stdin", false, "read batch input from stdin")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if (*filePath == "" && !*readStdin) || (*filePath != "" && *readStdin) {
		return errors.New("choose exactly one input source: --file or --stdin")
	}
	var payload []byte
	var err error
	if *readStdin {
		payload, err = io.ReadAll(os.Stdin)
	} else {
		payload, err = os.ReadFile(*filePath)
	}
	if err != nil {
		return err
	}

	inputs, err := parseBatch(payload)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return errors.New("no valid tasks in input")
	}

	added, err := addTasks(instance, inputs)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(added)
	}
	fmt.Printf("added %d tasks to %s\n", len(added), instance)
	return nil
}

func cmdStatus(args []string) error {
	var instance string
	flagArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		instance = args[0]
		flagArgs = args[1:]
	}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: obliviate status [instance] [--json]")
	}

	if instance != "" {
		instDir, err := resolveInstanceDir(instance)
		if err != nil {
			return err
		}
		tasks, err := loadTasks(filepath.Join(instDir, "tasks.jsonl"))
		if err != nil {
			return err
		}
		summary := summarizeStatus(instance, tasks)
		if *jsonOut {
			return printJSON(summary)
		}
		printStatusSummary(summary)
		return nil
	}

	stateDir, err := resolveStateDirFromCWD()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if *jsonOut {
				return printJSON([]statusSummary{})
			}
			fmt.Println("no instances found")
			return nil
		}
		return err
	}
	instances := make([]string, 0)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		instances = append(instances, e.Name())
	}
	sort.Strings(instances)
	if len(instances) == 0 {
		if *jsonOut {
			return printJSON([]statusSummary{})
		}
		fmt.Println("no instances found")
		return nil
	}
	all := make([]statusSummary, 0, len(instances))
	for _, instance := range instances {
		tasks, err := loadTasks(filepath.Join(stateDir, instance, "tasks.jsonl"))
		if err != nil {
			return err
		}
		all = append(all, summarizeStatus(instance, tasks))
	}
	if *jsonOut {
		return printJSON(all)
	}
	for _, s := range all {
		printStatusSummary(s)
	}
	return nil
}

func cmdShow(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: obliviate show <instance> <task-id> [--json]")
	}
	instance := args[0]
	taskID := strings.TrimSpace(args[1])
	if taskID == "" {
		return errors.New("task-id is required")
	}

	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: obliviate show <instance> <task-id> [--json]")
	}

	instDir, err := resolveInstanceDir(instance)
	if err != nil {
		return err
	}
	tasksPath := filepath.Join(instDir, "tasks.jsonl")
	tasks, err := loadTasks(tasksPath)
	if err != nil {
		return err
	}
	idx := findTaskIndex(tasks, taskID)
	if idx < 0 {
		return fmt.Errorf("task %q not found in instance %q", taskID, instance)
	}

	if *jsonOut {
		return printJSON(tasks[idx])
	}
	return printJSON(tasks[idx])
}

func cmdReset(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: obliviate reset <instance> <task-id> [--json]")
	}
	instance := args[0]
	taskID := strings.TrimSpace(args[1])
	if taskID == "" {
		return errors.New("task-id is required")
	}

	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: obliviate reset <instance> <task-id> [--json]")
	}

	instDir, err := resolveInstanceDir(instance)
	if err != nil {
		return err
	}
	lockRelease, err := acquireInstanceLock(instDir)
	if err != nil {
		return err
	}
	defer lockRelease()

	tasksPath := filepath.Join(instDir, "tasks.jsonl")
	tasks, err := loadTasks(tasksPath)
	if err != nil {
		return err
	}
	idx := findTaskIndex(tasks, taskID)
	if idx < 0 {
		return fmt.Errorf("task %q not found in instance %q", taskID, instance)
	}

	tasks[idx].Status = statusTodo
	tasks[idx].Attempts = 0
	tasks[idx].LastError = ""
	tasks[idx].UpdatedAt = nowUTC()
	if err := saveTasks(tasksPath, tasks); err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(tasks[idx])
	}
	fmt.Printf("reset %s -> todo\n", tasks[idx].ID)
	return nil
}

func cmdSkip(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: obliviate skip <instance> <task-id> [--reason \"...\"] [--json]")
	}
	instance := args[0]
	taskID := strings.TrimSpace(args[1])
	if taskID == "" {
		return errors.New("task-id is required")
	}

	fs := flag.NewFlagSet("skip", flag.ContinueOnError)
	reason := fs.String("reason", "", "human-readable skip reason")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: obliviate skip <instance> <task-id> [--reason \"...\"] [--json]")
	}

	instDir, err := resolveInstanceDir(instance)
	if err != nil {
		return err
	}
	lockRelease, err := acquireInstanceLock(instDir)
	if err != nil {
		return err
	}
	defer lockRelease()

	tasksPath := filepath.Join(instDir, "tasks.jsonl")
	tasks, err := loadTasks(tasksPath)
	if err != nil {
		return err
	}
	idx := findTaskIndex(tasks, taskID)
	if idx < 0 {
		return fmt.Errorf("task %q not found in instance %q", taskID, instance)
	}

	reasonText := strings.TrimSpace(*reason)
	if reasonText == "" {
		reasonText = "manually skipped"
	}
	tasks[idx].Status = statusBlocked
	tasks[idx].LastError = "skipped: " + reasonText
	tasks[idx].UpdatedAt = nowUTC()
	if err := saveTasks(tasksPath, tasks); err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(tasks[idx])
	}
	fmt.Printf("skipped %s -> blocked (%s)\n", tasks[idx].ID, reasonText)
	return nil
}

func cmdRuns(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: obliviate runs <instance> [--limit N] [--task-id OB-001] [--json]")
	}
	instance := args[0]

	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "number of most recent runs to return (0 = all)")
	taskID := fs.String("task-id", "", "filter by task id")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: obliviate runs <instance> [--limit N] [--task-id OB-001] [--json]")
	}
	if *limit < 0 {
		return errors.New("limit must be >= 0")
	}

	instDir, err := resolveInstanceDir(instance)
	if err != nil {
		return err
	}
	p := filepath.Join(instDir, "runs.jsonl")
	runs, err := loadRuns(p)
	if err != nil {
		return err
	}
	filter := strings.TrimSpace(*taskID)
	if filter != "" {
		filtered := make([]RunLog, 0, len(runs))
		for _, r := range runs {
			if r.TaskID == filter {
				filtered = append(filtered, r)
			}
		}
		runs = filtered
	}
	if *limit > 0 && len(runs) > *limit {
		runs = runs[len(runs)-*limit:]
	}
	if *jsonOut {
		return printJSON(runsResult{
			Instance: instance,
			Count:    len(runs),
			Runs:     runs,
		})
	}
	if len(runs) == 0 {
		fmt.Printf("[%s] no runs found\n", instance)
		return nil
	}
	for _, r := range runs {
		fmt.Printf("%s %s %s %s/%s\n", r.FinishedAt, r.TaskID, r.Status, r.Provider, r.Model)
	}
	return nil
}

func cmdGo(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: obliviate go <instance> [--limit N] [--dry-run] [--require-commit]")
	}
	instance := args[0]

	fs := flag.NewFlagSet("go", flag.ContinueOnError)
	limit := fs.Int("limit", 0, "max tasks to process (0 = all)")
	dryRun := fs.Bool("dry-run", false, "show what would run")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	requireCommit := fs.Bool("require-commit", false, "require each successful task to create a new git commit")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	instDir, err := resolveInstanceDir(instance)
	if err != nil {
		return err
	}
	meta, err := loadInstanceMeta(filepath.Join(instDir, "instance.json"))
	if err != nil {
		return err
	}

	home := filepath.Dir(filepath.Dir(instDir))
	projectRoot := filepath.Dir(home)
	workdir := resolveWorkdir(projectRoot, meta.Workdir)
	tasksPath := filepath.Join(instDir, "tasks.jsonl")
	runsPath := filepath.Join(instDir, "runs.jsonl")
	lockRelease, err := acquireInstanceLock(instDir)
	if err != nil {
		return err
	}
	defer lockRelease()

	tasks, err := loadTasks(tasksPath)
	if err != nil {
		return err
	}

	processed := 0
	doneCount := 0
	failedCount := 0
	blockedCount := 0
	taskIDs := make([]string, 0)
	for {
		if *limit > 0 && processed >= *limit {
			break
		}
		idx := nextRunnableTaskIndex(tasks)
		if idx < 0 {
			break
		}
		t := tasks[idx]

		if *dryRun {
			if !*jsonOut {
				fmt.Printf("would run %s (%s)\n", t.ID, t.Title)
			}
			processed++
			tasks[idx].Status = statusDone
			taskIDs = append(taskIDs, t.ID)
			continue
		}

		start := nowUTC()
		tasks[idx].Status = statusInProgress
		tasks[idx].UpdatedAt = start
		if err := saveTasks(tasksPath, tasks); err != nil {
			return err
		}

		primaryProvider, primaryModel := routeModel(t.ModelHint)
		prompt, err := buildExecutionPrompt(home, instance, t)
		if err != nil {
			return err
		}

		headBefore := ""
		headBeforeErr := error(nil)
		if *requireCommit {
			headBefore, headBeforeErr = gitHead(workdir)
		}

		provider, model, agentOut, execErr, fb := runAgentWithFallback(primaryProvider, primaryModel, workdir, prompt, agentTimeout)
		run := RunLog{
			TaskID:          t.ID,
			Provider:        provider,
			Model:           model,
			PrimaryProvider: primaryProvider,
			PrimaryModel:    primaryModel,
			StartedAt:       start,
			FinishedAt:      nowUTC(),
			OutputTail:      tail(agentOut, 1000),
		}
		if fb != nil {
			run.FallbackProvider = fb.FallbackProvider
			run.FallbackModel = fb.FallbackModel
			run.FallbackReason = fb.Reason
		}

		if execErr == nil {
			var failedCmd string
			failedOutput := ""
			for _, v := range t.Verify {
				out, verifyErr := runVerify(workdir, v, verifyTimeout)
				if verifyErr != nil {
					failedCmd = v
					failedOutput = out + "\n" + verifyErr.Error()
					break
				}
			}
			if failedCmd != "" {
				execErr = fmt.Errorf("verify failed: %s", failedCmd)
				run.VerifyFailed = failedCmd
				run.OutputTail = tail(run.OutputTail+"\n"+failedOutput, 1000)
			}
		}

		if execErr == nil && *requireCommit {
			if headBeforeErr != nil {
				execErr = fmt.Errorf("require-commit: resolve pre-task git head: %w", headBeforeErr)
			} else {
				headAfter, headAfterErr := gitHead(workdir)
				if headAfterErr != nil {
					execErr = fmt.Errorf("require-commit: resolve post-task git head: %w", headAfterErr)
				} else if headAfter == headBefore {
					execErr = errors.New("require-commit enabled: no new commit created")
				}
			}
		}

		if execErr != nil {
			tasks[idx].Attempts++
			tasks[idx].LastError = execErr.Error()
			tasks[idx].UpdatedAt = nowUTC()
			if tasks[idx].Attempts >= maxAttempts {
				tasks[idx].Status = statusBlocked
				blockedCount++
			} else {
				tasks[idx].Status = statusFailed
				failedCount++
			}
			run.Status = tasks[idx].Status
			run.Error = execErr.Error()
			if !*jsonOut {
				fmt.Printf("%s %s -> %s: %s\n", t.ID, t.Title, tasks[idx].Status, execErr.Error())
			}
		} else {
			tasks[idx].Status = statusDone
			tasks[idx].UpdatedAt = nowUTC()
			tasks[idx].LastError = ""
			run.Status = statusDone
			_ = appendLine(filepath.Join(instDir, "learnings.md"), fmt.Sprintf("- [%s] %s completed (%s)\n", nowUTC(), t.ID, t.Title))
			doneCount++
			if !*jsonOut {
				fmt.Printf("%s %s -> done\n", t.ID, t.Title)
			}
		}

		if err := appendJSONLine(runsPath, run); err != nil {
			return err
		}
		if err := saveTasks(tasksPath, tasks); err != nil {
			return err
		}

		processed++
		taskIDs = append(taskIDs, t.ID)
	}

	if err := appendCycleSummaryLine(filepath.Join(instDir, "cycle.log"), instance, processed, doneCount, failedCount, blockedCount, taskIDs, *dryRun); err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(goResult{
			Instance:  instance,
			Processed: processed,
			Done:      doneCount,
			Failed:    failedCount,
			Blocked:   blockedCount,
			TaskIDs:   taskIDs,
		})
	}
	fmt.Printf("processed %d task(s)\n", processed)
	return nil
}

func resolveProjectRootFromCWD() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Clean(wd), nil
}

func resolveProjectRootFromWorkdir(workdir string) (string, error) {
	w := strings.TrimSpace(workdir)
	if w == "" {
		w = "."
	}
	if !filepath.IsAbs(w) {
		cwd, err := resolveProjectRootFromCWD()
		if err != nil {
			return "", err
		}
		w = filepath.Join(cwd, w)
	}
	w = filepath.Clean(w)
	info, err := os.Stat(w)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir must be a directory: %s", w)
	}
	return w, nil
}

func projectObliviateHome(projectRoot string) string {
	return filepath.Join(projectRoot, ".obliviate")
}

func projectStateDir(projectRoot string) string {
	return filepath.Join(projectObliviateHome(projectRoot), "state")
}

func projectInstanceDir(projectRoot, instance string) string {
	return filepath.Join(projectStateDir(projectRoot), instance)
}

func resolveStateDirFromCWD() (string, error) {
	projectRoot, err := resolveProjectRootFromCWD()
	if err != nil {
		return "", err
	}
	return projectStateDir(projectRoot), nil
}

func resolveInstanceDir(instance string) (string, error) {
	projectRoot, err := resolveProjectRootFromCWD()
	if err != nil {
		return "", err
	}
	instDir := projectInstanceDir(projectRoot, instance)
	if _, err := os.Stat(filepath.Join(instDir, "instance.json")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("instance %q is not initialized in %s (run obliviate init %s --workdir %s)", instance, projectRoot, instance, projectRoot)
		}
		return "", err
	}
	return instDir, nil
}

func parseBatch(payload []byte) ([]taskInput, error) {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var raws []taskInputRaw
		if err := json.Unmarshal([]byte(trimmed), &raws); err != nil {
			return nil, err
		}
		out := make([]taskInput, 0, len(raws))
		for i, r := range raws {
			t, err := normalizeInput(r)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i+1, err)
			}
			out = append(out, t)
		}
		return out, nil
	}

	s := bufio.NewScanner(strings.NewReader(trimmed))
	lineNo := 0
	out := make([]taskInput, 0)
	for s.Scan() {
		lineNo++
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var raw taskInputRaw
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		t, err := normalizeInput(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out = append(out, t)
	}
	return out, s.Err()
}

func normalizeInput(raw taskInputRaw) (taskInput, error) {
	verify, err := parseVerify(raw.Verify)
	if err != nil {
		return taskInput{}, err
	}
	if strings.TrimSpace(raw.Title) == "" || strings.TrimSpace(raw.Spec) == "" || len(verify) == 0 {
		return taskInput{}, errors.New("title, spec, and verify are required")
	}
	if strings.TrimSpace(raw.ModelHint) == "" {
		return taskInput{}, errors.New("model_hint is required")
	}
	priority := strings.TrimSpace(raw.Priority)
	if priority == "" {
		priority = "med"
	}
	source := strings.TrimSpace(raw.Source)
	if source == "" {
		source = "agent"
	}
	return taskInput{
		Title:     raw.Title,
		Spec:      raw.Spec,
		Verify:    verify,
		ModelHint: strings.TrimSpace(raw.ModelHint),
		Priority:  priority,
		Source:    source,
	}, nil
}

func parseVerify(raw json.RawMessage) ([]string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("verify is required")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		single = strings.TrimSpace(single)
		if single == "" {
			return nil, errors.New("verify cannot be empty")
		}
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, errors.New("verify must be a string or string array")
	}
	out := make([]string, 0, len(many))
	for _, v := range many {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errors.New("verify cannot be empty")
	}
	return out, nil
}

func addTasks(instance string, inputs []taskInput) ([]Task, error) {
	instDir, err := resolveInstanceDir(instance)
	if err != nil {
		return nil, err
	}
	lockRelease, err := acquireInstanceLock(instDir)
	if err != nil {
		return nil, err
	}
	defer lockRelease()

	p := filepath.Join(instDir, "tasks.jsonl")
	tasks, err := loadTasks(p)
	if err != nil {
		return nil, err
	}
	next := nextTaskNumber(tasks)
	now := nowUTC()
	added := make([]Task, 0, len(inputs))
	for _, in := range inputs {
		id := fmt.Sprintf("OB-%03d", next)
		next++
		t := Task{
			ID:        id,
			Title:     strings.TrimSpace(in.Title),
			Spec:      strings.TrimSpace(in.Spec),
			Verify:    in.Verify,
			Status:    statusTodo,
			ModelHint: in.ModelHint,
			Priority:  in.Priority,
			Attempts:  0,
			Source:    in.Source,
			CreatedAt: now,
			UpdatedAt: now,
		}
		tasks = append(tasks, t)
		added = append(added, t)
	}
	if err := saveTasks(p, tasks); err != nil {
		return nil, err
	}
	return added, nil
}

func nextTaskNumber(tasks []Task) int {
	maxN := 0
	for _, t := range tasks {
		if strings.HasPrefix(t.ID, "OB-") {
			n, err := strconv.Atoi(strings.TrimPrefix(t.ID, "OB-"))
			if err == nil && n > maxN {
				maxN = n
			}
		}
	}
	return maxN + 1
}

func loadTasks(path string) ([]Task, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Task{}, nil
		}
		return nil, err
	}
	defer f.Close()

	tasks := make([]Task, 0)
	s := bufio.NewScanner(f)
	lineNo := 0
	for s.Scan() {
		lineNo++
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var t Task
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("tasks parse line %d: %w", lineNo, err)
		}
		tasks = append(tasks, t)
	}
	return tasks, s.Err()
}

func findTaskIndex(tasks []Task, taskID string) int {
	for i := range tasks {
		if tasks[i].ID == taskID {
			return i
		}
	}
	return -1
}

func loadRuns(path string) ([]RunLog, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []RunLog{}, nil
		}
		return nil, err
	}
	defer f.Close()

	runs := make([]RunLog, 0)
	s := bufio.NewScanner(f)
	lineNo := 0
	for s.Scan() {
		lineNo++
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var r RunLog
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("runs parse line %d: %w", lineNo, err)
		}
		runs = append(runs, r)
	}
	return runs, s.Err()
}

func saveTasks(path string, tasks []Task) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, t := range tasks {
		if err := enc.Encode(t); err != nil {
			f.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadInstanceMeta(path string) (InstanceMeta, error) {
	var m InstanceMeta
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

func nextRunnableTaskIndex(tasks []Task) int {
	for i := range tasks {
		if tasks[i].Status == statusTodo {
			return i
		}
	}
	for i := range tasks {
		if tasks[i].Status == statusFailed && tasks[i].Attempts < maxAttempts {
			return i
		}
	}
	return -1
}

func buildExecutionPrompt(home, instance string, task Task) (string, error) {
	skill, _ := readText(filepath.Join(home, "SKILL.md"))
	instDir := filepath.Join(home, "state", instance)
	promptMD, _ := readText(filepath.Join(instDir, "prompt.md"))
	specMD, _ := readText(filepath.Join(instDir, "spec.md"))
	instLearn, _ := readText(filepath.Join(instDir, "learnings.md"))
	globalLearn, _ := readText(filepath.Join(home, "state", "global", "learnings.md"))

	taskJSON, _ := json.MarshalIndent(task, "", "  ")
	parts := []string{
		"You are running inside Obliviate's fresh-context task loop. Complete exactly one task.",
		"## SKILL.md\n" + skill,
		"## Instance Prompt\n" + promptMD,
		"## Feature Spec\n" + specMD,
		"## Global Learnings\n" + globalLearn,
		"## Instance Learnings\n" + instLearn,
		"## Current Task (JSON)\n" + string(taskJSON),
		"## Output Requirements\n- Implement the task\n- Run verify commands\n- Commit changes with a clear message\n- If blocked, explain exact blocker and failing command",
	}
	return strings.Join(parts, "\n\n"), nil
}

func routeModel(hint string) (provider, model string) {
	h := strings.ToLower(strings.TrimSpace(hint))
	if h == "" {
		return "codex", ""
	}
	if strings.Contains(h, "opus") {
		return "claude", "opus"
	}
	if strings.Contains(h, "sonnet") {
		return "claude", "sonnet"
	}
	if strings.Contains(h, "haiku") {
		return "claude", "haiku"
	}
	if strings.HasPrefix(h, "claude") {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			return "claude", normalizeClaudeModel(parts[1])
		}
		return "claude", normalizeClaudeModel(h)
	}
	if strings.HasPrefix(h, "codex") || strings.HasPrefix(h, "gpt") || strings.HasPrefix(h, "o") {
		if h == "codex" {
			return "codex", ""
		}
		return "codex", h
	}
	return "codex", ""
}

func normalizeClaudeModel(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	m = strings.TrimPrefix(m, "claude-")
	return m
}

func resolveWorkdir(projectRoot, configured string) string {
	w := strings.TrimSpace(configured)
	if w == "" {
		return filepath.Clean(projectRoot)
	}
	if filepath.IsAbs(w) {
		return filepath.Clean(w)
	}
	return filepath.Clean(filepath.Join(projectRoot, w))
}

func runAgentWithFallback(primaryProvider, primaryModel, workdir, prompt string, timeout time.Duration) (provider, model, output string, err error, fb *fallbackAttempt) {
	out1, err1 := runAgent(primaryProvider, primaryModel, workdir, prompt, timeout)
	if err1 == nil {
		return primaryProvider, primaryModel, out1, nil, nil
	}
	reason := classifyProviderFailure(err1, out1)
	if reason == "" {
		return primaryProvider, primaryModel, out1, err1, nil
	}

	fallbackProvider, fallbackModel, ok := selectFallback(primaryProvider, primaryModel)
	if !ok {
		return primaryProvider, primaryModel, out1, err1, nil
	}

	out2, err2 := runAgent(fallbackProvider, fallbackModel, workdir, prompt, timeout)
	combined := strings.TrimSpace(out1 + "\n\n[obliviate fallback]\n" + out2)
	details := &fallbackAttempt{
		PrimaryProvider:  primaryProvider,
		PrimaryModel:     primaryModel,
		FallbackProvider: fallbackProvider,
		FallbackModel:    fallbackModel,
		Reason:           reason,
	}
	if err2 == nil {
		return fallbackProvider, fallbackModel, combined, nil, details
	}
	return fallbackProvider, fallbackModel, combined, fmt.Errorf("primary failed (%s): %v; fallback failed: %v", reason, err1, err2), details
}

func selectFallback(provider, model string) (fallbackProvider, fallbackModel string, ok bool) {
	if provider == "codex" {
		// Cost guardrail: codex falls back to sonnet, never opus.
		return "claude", "sonnet", true
	}
	if provider == "claude" {
		// Claude variants fall back to codex.
		return "codex", "", true
	}
	return "", "", false
}

func classifyProviderFailure(err error, output string) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error() + "\n" + output)
	// Broader contains-any pass for common provider-level failures.
	containsAny := func(keys ...string) bool {
		for _, k := range keys {
			if strings.Contains(msg, k) {
				return true
			}
		}
		return false
	}
	switch {
	case containsAny("rate limit", "rate-limited", "too many requests", "429"):
		return "rate_limit"
	case containsAny("usage limit", "quota", "daily limit", "weekly limit", "monthly limit"):
		return "quota"
	case containsAny("billing", "payment", "insufficient credits"):
		return "billing"
	case containsAny("model", "not exist", "not have access", "unknown model"):
		return "model_unavailable"
	case containsAny("temporarily unavailable", "service unavailable", "overloaded"):
		return "provider_unavailable"
	case containsAny("auth", "unauthorized", "forbidden", "login required"):
		return "auth"
	default:
		return ""
	}
}

func killProcessTree(p *os.Process) error {
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid))
	if err := kill.Run(); err != nil {
		return p.Kill()
	}
	return nil
}

func runAgent(provider, model, workdir, prompt string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var cmd *exec.Cmd
	if provider == "claude" {
		args := []string{
			"-p",
			"--output-format", "text",
			"--permission-mode", "bypassPermissions",
			"--dangerously-skip-permissions",
			"--no-session-persistence",
			"--disallowedTools", "AskUserQuestion,EnterPlanMode",
		}
		if model != "" {
			args = append(args, "--model", model)
		}
		cmd = exec.CommandContext(ctx, "claude", args...)
		cmd.Stdin = strings.NewReader(prompt)
	} else {
		args := []string{
			"exec",
			"--cd", workdir,
			"--skip-git-repo-check",
			"--dangerously-bypass-approvals-and-sandbox",
			"-",
		}
		if model != "" {
			args = append([]string{"exec", "--cd", workdir, "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--model", model, "-"})
		}
		cmd = exec.CommandContext(ctx, "codex", args...)
		cmd.Stdin = strings.NewReader(prompt)
	}

	cmd.Dir = workdir
	cmd.WaitDelay = 10 * time.Second
	cmd.Cancel = func() error { return killProcessTree(cmd.Process) }
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return out.String(), fmt.Errorf("agent timed out after %s: %w", timeout, err)
	}
	return out.String(), err
}

func runVerify(workdir, verifyCmd string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", verifyCmd)
	cmd.Dir = workdir
	cmd.WaitDelay = 10 * time.Second
	cmd.Cancel = func() error { return killProcessTree(cmd.Process) }
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return out.String(), fmt.Errorf("verify timed out after %s: %w", timeout, err)
	}
	return out.String(), err
}

func gitHead(workdir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git rev-parse HEAD in %s failed: %s", workdir, msg)
	}
	head := strings.TrimSpace(string(out))
	if head == "" {
		return "", fmt.Errorf("git rev-parse HEAD in %s returned empty output", workdir)
	}
	return head, nil
}

func printStatus(instance string, tasks []Task) {
	printStatusSummary(summarizeStatus(instance, tasks))
}

type statusSummary struct {
	Instance   string `json:"instance"`
	Total      int    `json:"total"`
	Todo       int    `json:"todo"`
	InProgress int    `json:"in_progress"`
	Done       int    `json:"done"`
	Failed     int    `json:"failed"`
	Blocked    int    `json:"blocked"`
}

func summarizeStatus(instance string, tasks []Task) statusSummary {
	counts := map[string]int{
		statusTodo:       0,
		statusInProgress: 0,
		statusDone:       0,
		statusFailed:     0,
		statusBlocked:    0,
	}
	for _, t := range tasks {
		counts[t.Status]++
	}
	return statusSummary{
		Instance:   instance,
		Total:      len(tasks),
		Todo:       counts[statusTodo],
		InProgress: counts[statusInProgress],
		Done:       counts[statusDone],
		Failed:     counts[statusFailed],
		Blocked:    counts[statusBlocked],
	}
}

func printStatusSummary(s statusSummary) {
	fmt.Printf("[%s] total=%d todo=%d in_progress=%d done=%d failed=%d blocked=%d\n",
		s.Instance,
		s.Total,
		s.Todo,
		s.InProgress,
		s.Done,
		s.Failed,
		s.Blocked)
}

func readText(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func appendJSONLine(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return appendLine(path, string(b)+"\n")
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

func appendCycleSummaryLine(path, instance string, processed, done, failed, blocked int, taskIDs []string, dryRun bool) error {
	line := fmt.Sprintf("%s instance=%s processed=%d done=%d failed=%d blocked=%d dry_run=%t task_ids=%s\n",
		nowUTC(),
		instance,
		processed,
		done,
		failed,
		blocked,
		dryRun,
		joinTaskIDs(taskIDs),
	)
	return appendLine(path, line)
}

func joinTaskIDs(taskIDs []string) string {
	if len(taskIDs) == 0 {
		return "-"
	}
	return strings.Join(taskIDs, ",")
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func defaultPrompt(instance string) string {
	return fmt.Sprintf(`# Obliviate Prompt (%s)

Rules for each task run:

1. Complete exactly one task.
2. Keep changes scoped to task requirements.
3. Run all verify commands from the task.
4. Commit once with a clear message.
5. If blocked, report failing command and why.
6. Read and apply learnings from both .obliviate/global-learnings.md and this instance's learnings.md.
7. Append non-obvious learnings to this instance's learnings.md (and promote reusable ones to .obliviate/global-learnings.md).
`, instance)
}

func acquireInstanceLock(instDir string) (func(), error) {
	lockPath := filepath.Join(instDir, ".tasks.lock")
	start := time.Now()
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if time.Since(start) > lockWaitMax {
			return nil, fmt.Errorf("runtime: timed out waiting for lock %s", lockPath)
		}
		time.Sleep(lockWaitStep)
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func classifyExitCode(err error) int {
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.HasPrefix(msg, "usage:"):
		return exitUsage
	case strings.Contains(msg, "flag provided but not defined"):
		return exitUsage
	case strings.Contains(msg, "required"), strings.Contains(msg, "must be"), strings.Contains(msg, "cannot be empty"):
		return exitValidation
	case strings.Contains(msg, "not initialized"), strings.Contains(msg, "not found"):
		return exitNotFound
	default:
		return exitRuntime
	}
}





