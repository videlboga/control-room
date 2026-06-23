package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"control-room/internal/config"
	"control-room/internal/epic"
	"control-room/internal/orchestrator"
	"control-room/internal/project"
	"control-room/internal/run"
	"control-room/internal/store"
	"control-room/internal/task"
	"control-room/internal/team"
	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	var root string
	var hermesUser string
	var hermesSource string
	var stub bool
	rootCmd := &cobra.Command{
		Use:   "hw",
		Short: "Hermes Workspace -- lightweight project/team/run orchestrator",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if root == "" {
				root = config.DefaultWorkspace()
			}
			cfg, err := config.LoadOrCreate(root)
			if err != nil {
				return err
			}
			if hermesUser != "" {
				cfg.HermesUser = hermesUser
			}
			if hermesSource != "" {
				cfg.HermesSourceProfile = hermesSource
			}
			if stub {
				cfg.StubMode = true
			}
			return nil
		},
	}
	rootCmd.PersistentFlags().StringVarP(&root, "workspace", "w", "", "workspace root directory")
	rootCmd.PersistentFlags().StringVar(&hermesUser, "hermes-user", "", "user that owns Hermes profiles")
	rootCmd.PersistentFlags().StringVar(&hermesSource, "hermes-source", "", "default source Hermes profile to clone")
	rootCmd.PersistentFlags().BoolVar(&stub, "stub", false, "stub mode: simulate agent runs without invoking Hermes")

	rootCmd.AddCommand(projectCmd())
	rootCmd.AddCommand(epicCmd())
	rootCmd.AddCommand(teamCmd())
	rootCmd.AddCommand(taskCmd())
	rootCmd.AddCommand(orchestrateCmd())
	rootCmd.AddCommand(runCmd())
	return rootCmd
}

func projectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage projects"}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a new project",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			id, _ := cmd.Flags().GetString("id")
			title, _ := cmd.Flags().GetString("title")
			repo, _ := cmd.Flags().GetString("repo")
			team, _ := cmd.Flags().GetString("default-team")
			docsDir, _ := cmd.Flags().GetString("docs-dir")
			testCmd, _ := cmd.Flags().GetString("test-command")
			lintCmd, _ := cmd.Flags().GetString("lint-command")
			p := &project.Project{
				ID: id, Title: title, RepoPath: repo, DefaultTeam: team, DocsDir: docsDir,
				TestCommand: testCmd, LintCommand: lintCmd,
			}
			if err := project.Create(st, p); err != nil {
				return err
			}
			fmt.Printf("project %s created\n", id)
			return nil
		},
	}
	create.Flags().String("id", "", "project id")
	create.Flags().String("title", "", "project title")
	create.Flags().String("repo", "", "path to git repo")
	create.Flags().String("default-team", "", "default team id")
	create.Flags().String("docs-dir", "", "directory with project docs")
	create.Flags().String("test-command", "", "command to run tests (e.g. go test ./...)")
	create.Flags().String("lint-command", "", "command to run lint (e.g. go vet ./...)")
	_ = create.MarkFlagRequired("id")
	_ = create.MarkFlagRequired("title")

	list := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			projects, err := project.List(st)
			if err != nil {
				return err
			}
			for _, p := range projects {
				fmt.Printf("%s\t%s\t%s\n", p.ID, p.Title, p.RepoPath)
			}
			return nil
		},
	}

	show := &cobra.Command{
		Use:   "show [id]",
		Short: "Show project details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			p, err := project.Get(st, args[0])
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(p, "", "  ")
			fmt.Println(string(data))
			return nil
		},
	}

	docs := &cobra.Command{Use: "docs", Short: "Manage project docs"}
	addDoc := &cobra.Command{
		Use:   "add",
		Short: "Add a doc file to a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			projectID, _ := cmd.Flags().GetString("project")
			file, _ := cmd.Flags().GetString("file")
			if err := project.AddDoc(st, projectID, file); err != nil {
				return err
			}
			fmt.Printf("added %s to project %s\n", file, projectID)
			return nil
		},
	}
	addDoc.Flags().String("project", "", "project id")
	addDoc.Flags().String("file", "", "path to doc file")
	_ = addDoc.MarkFlagRequired("project")
	_ = addDoc.MarkFlagRequired("file")

	listDocs := &cobra.Command{
		Use:   "list",
		Short: "List project docs",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			projectID, _ := cmd.Flags().GetString("project")
			docs, err := project.ListDocs(st, projectID)
			if err != nil {
				return err
			}
			for _, d := range docs {
				fmt.Println(d)
			}
			return nil
		},
	}
	listDocs.Flags().String("project", "", "project id")
	_ = listDocs.MarkFlagRequired("project")

	docs.AddCommand(addDoc, listDocs)
	cmd.AddCommand(create, list, show, docs)
	return cmd
}

func teamCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "team", Short: "Manage teams"}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a team from a JSON file",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			file, _ := cmd.Flags().GetString("file")
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			var t team.Team
			if err := json.Unmarshal(data, &t); err != nil {
				return err
			}
			if err := team.Create(st, &t); err != nil {
				return err
			}
			fmt.Printf("team %s created\n", t.ID)
			return nil
		},
	}
	create.Flags().String("file", "", "path to team JSON")
	_ = create.MarkFlagRequired("file")

	list := &cobra.Command{
		Use:   "list",
		Short: "List teams",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			teams, err := team.List(st)
			if err != nil {
				return err
			}
			for _, t := range teams {
				fmt.Printf("%s\t%s\t%d agents\n", t.ID, t.Name, len(t.Agents))
			}
			return nil
		},
	}

	show := &cobra.Command{
		Use:   "show [id]",
		Short: "Show team details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			t, err := team.Get(st, args[0])
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(t, "", "  ")
			fmt.Println(string(data))
			return nil
		},
	}

	cmd.AddCommand(create, list, show)
	return cmd
}

func taskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Manage tasks"}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a new task",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			title, _ := cmd.Flags().GetString("title")
			projectID, _ := cmd.Flags().GetString("project")
			teamID, _ := cmd.Flags().GetString("team")
			desc, _ := cmd.Flags().GetString("description")
			typ, _ := cmd.Flags().GetString("type")
			t := &task.Task{
				Title:       title,
				ProjectID:   projectID,
				TeamID:      teamID,
				Type:        task.TaskType(typ),
				Description: desc,
			}
			created, err := task.Create(st, t)
			if err != nil {
				return err
			}
			fmt.Printf("task %s created\n", created.ID)
			return nil
		},
	}
	create.Flags().String("title", "", "task title")
	create.Flags().String("project", "", "project id")
	create.Flags().String("team", "", "team id")
	create.Flags().String("description", "", "task description")
	create.Flags().String("type", "engineering", "task type: research, qa_review, pm_plan, engineering, qa_verify, pm_consistency")
	for _, f := range []string{"title", "project", "team", "type"} {
		_ = create.MarkFlagRequired(f)
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			projectID, _ := cmd.Flags().GetString("project")
			tasks, err := task.ListByProject(st, projectID)
			if err != nil {
				return err
			}
			for _, t := range tasks {
				fmt.Printf("%s\t%s\t%s\t%s\n", t.ID, t.Status, t.ProjectID, t.Title)
			}
			return nil
		},
	}
	list.Flags().String("project", "", "filter by project id")

	show := &cobra.Command{
		Use:   "show [id]",
		Short: "Show task details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			t, err := task.Get(st, args[0])
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(t, "", "  ")
			fmt.Println(string(data))
			return nil
		},
	}

	cmd.AddCommand(create, list, show)
	return cmd
}

func epicCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "epic", Short: "Manage epics"}

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a new epic",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			title, _ := cmd.Flags().GetString("title")
			projectID, _ := cmd.Flags().GetString("project")
			teamID, _ := cmd.Flags().GetString("team")
			desc, _ := cmd.Flags().GetString("description")
			if teamID == "" {
				proj, err := project.Get(st, projectID)
				if err == nil {
					teamID = proj.DefaultTeam
				}
			}
			e := &epic.Epic{Title: title, ProjectID: projectID, TeamID: teamID, Description: desc}
			created, err := epic.Create(st, e)
			if err != nil {
				return err
			}
			fmt.Printf("epic %s created\n", created.ID)
			return nil
		},
	}
	create.Flags().String("title", "", "epic title")
	create.Flags().String("project", "", "project id")
	create.Flags().String("team", "", "team id (optional, defaults to project default team)")
	create.Flags().String("description", "", "epic description")
	_ = create.MarkFlagRequired("title")
	_ = create.MarkFlagRequired("project")

	list := &cobra.Command{
		Use:   "list",
		Short: "List epics",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			epics, err := epic.List(st)
			if err != nil {
				return err
			}
			for _, e := range epics {
				fmt.Printf("%s\t%s\t%s\t%s\n", e.ID, e.Status, e.ProjectID, e.Title)
			}
			return nil
		},
	}

	show := &cobra.Command{
		Use:   "show [id]",
		Short: "Show epic details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			e, err := epic.Get(st, args[0])
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(e, "", "  ")
			fmt.Println(string(data))
			return nil
		},
	}

	cmd.AddCommand(create, list, show)
	return cmd
}

func orchestrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "orchestrate",
		Short: "Run the deterministic workflow orchestrator for an epic",
	}

	run := &cobra.Command{
		Use:   "run",
		Short: "Start orchestrating an epic",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			epicID, _ := cmd.Flags().GetString("epic")
			manual, _ := cmd.Flags().GetBool("manual-approve")
			if epicID == "" {
				return errors.New("--epic is required")
			}
			o := orchestrator.Orchestrator{Store: st, ManualApprove: manual, MaxRedo: 3}
			if manual {
				o.Prompt = manualApprovePrompt(cmd.InOrStdin(), cmd.OutOrStdout())
			}
			return o.RunEpic(epicID, func(event string, args ...interface{}) {
				fmt.Printf("[orch] %s", event)
				for _, a := range args {
					fmt.Printf(" %v", a)
				}
				fmt.Println()
			})
		},
	}
	run.Flags().String("epic", "", "epic id")
	run.Flags().Bool("manual-approve", false, "prompt for manual approval on QA verify tasks")
	_ = run.MarkFlagRequired("epic")

	watch := &cobra.Command{
		Use:   "watch",
		Short: "Watch an epic and run ready tasks in parallel batches",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			epicID, _ := cmd.Flags().GetString("epic")
			manual, _ := cmd.Flags().GetBool("manual-approve")
			if epicID == "" {
				return errors.New("--epic is required")
			}
			o := orchestrator.Orchestrator{Store: st, ManualApprove: manual, MaxRedo: 3}
			if manual {
				o.Prompt = manualApprovePrompt(cmd.InOrStdin(), cmd.OutOrStdout())
			}
			return o.WatchEpic(epicID, func(event string, args ...interface{}) {
				fmt.Printf("[orch] %s", event)
				for _, a := range args {
					fmt.Printf(" %v", a)
				}
				fmt.Println()
			})
		},
	}
	watch.Flags().String("epic", "", "epic id")
	watch.Flags().Bool("manual-approve", false, "prompt for manual approval on QA verify tasks")
	_ = watch.MarkFlagRequired("epic")

	cmd.AddCommand(run, watch)
	return cmd
}

func manualApprovePrompt(in io.Reader, out io.Writer) func(string) (string, string) {
	return func(taskID string) (string, string) {
		fmt.Fprintf(out, "Approve QA verify task %s? [approve/reject]: ", taskID)
		var line string
		_, _ = fmt.Fscanln(in, &line)
		line = strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(line, "rej") {
			return "reject", "manual rejection"
		}
		if line == "" || strings.HasPrefix(line, "app") {
			return "approve", "manual approval"
		}
		return "reject", fmt.Sprintf("unrecognized response %q, defaulting to reject", line)
	}
}

func runCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "run", Short: "Manage runs"}

	start := &cobra.Command{
		Use:   "start --task [id]",
		Short: "Start a run for a task (blocks until done; use --detach for background)",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			taskID, _ := cmd.Flags().GetString("task")
			detach, _ := cmd.Flags().GetBool("detach")
			maxConcurrent, _ := cmd.Flags().GetInt("max-concurrent")
			if detach {
				return spawnDetached(cmd, taskID, maxConcurrent)
			}
			r, err := run.Start(st, taskID)
			if err != nil {
				return err
			}
			fmt.Printf("run %s started for task %s\n", r.ID, taskID)
			return run.WaitFor(st, r.ID, func(ev run.Event) {
				ts := ev.Timestamp
				if len(ts) > 19 {
					ts = ts[:19]
				}
				fmt.Printf("[%s] %-10s %-10s %-10s %s\n", ts, ev.Agent, ev.Type, ev.Step, ev.Payload)
			})
		},
	}
	start.Flags().String("task", "", "task id")
	start.Flags().Bool("detach", false, "detach and run in background")
	start.Flags().Int("max-concurrent", 0, "override max concurrent runs limit")
	_ = start.MarkFlagRequired("task")

	list := &cobra.Command{
		Use:   "list",
		Short: "List runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			runs, err := run.List(st)
			if err != nil {
				return err
			}
			fmt.Printf("%-20s %-12s %-12s %-12s %-20s\n", "ID", "STATUS", "AGENT", "STEP", "STARTED")
			for _, r := range runs {
				started := r.StartedAt
				if len(started) > 19 {
					started = started[:19]
				}
				fmt.Printf("%-20s %-12s %-12s %-12s %-20s\n", r.ID, r.Status, r.Agent, r.Step, started)
			}
			return nil
		},
	}

	show := &cobra.Command{
		Use:   "show [id]",
		Short: "Show run details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			r, err := run.Get(st, args[0])
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(r, "", "  ")
			fmt.Println(string(data))
			return nil
		},
	}

	logs := &cobra.Command{
		Use:   "logs [id]",
		Short: "Show run logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			events, err := run.Logs(st, args[0])
			if err != nil {
				return err
			}
			for _, ev := range events {
				ts := ev.Timestamp
				if len(ts) > 19 {
					ts = ts[:19]
				}
				fmt.Printf("[%s] %-10s %-10s %-10s %s\n", ts, ev.Agent, ev.Type, ev.Step, ev.Payload)
			}
			return nil
		},
	}

	cancel := &cobra.Command{
		Use:   "cancel [id]",
		Short: "Cancel a running run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := storeFromFlags(cmd)
			if err := run.Cancel(st, args[0]); err != nil {
				return err
			}
			fmt.Printf("run %s cancelled\n", args[0])
			return nil
		},
	}

	cmd.AddCommand(start, list, show, logs, cancel)
	return cmd
}

func spawnDetached(cmd *cobra.Command, taskID string, maxConcurrent int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"run", "start", "--task", taskID}
	if root, err := cmd.Flags().GetString("workspace"); err == nil && root != "" {
		args = append(args, "--workspace", root)
	}
	if maxConcurrent > 0 {
		args = append(args, "--max-concurrent", fmt.Sprintf("%d", maxConcurrent))
	}
	c := exec.Command(self, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		return err
	}
	fmt.Printf("run detached as pid %d for task %s\n", c.Process.Pid, taskID)
	return nil
}

func storeFromFlags(cmd *cobra.Command) *store.Store {
	root, _ := cmd.Flags().GetString("workspace")
	if root == "" {
		root = config.DefaultWorkspace()
	}
	cfg, _ := config.LoadOrCreate(root)
	s := store.New(root)
	if cfg != nil {
		s.HermesUser = cfg.HermesUser
		s.HermesSourceProfile = cfg.HermesSourceProfile
		s.MaxConcurrentRuns = cfg.MaxConcurrentRuns
		s.StubMode = cfg.StubMode
	}
	if user, err := cmd.Flags().GetString("hermes-user"); err == nil && user != "" {
		s.HermesUser = user
	}
	if source, err := cmd.Flags().GetString("hermes-source"); err == nil && source != "" {
		s.HermesSourceProfile = source
	}
	if max, err := cmd.Flags().GetInt("max-concurrent"); err == nil && max > 0 {
		s.MaxConcurrentRuns = max
	}
	if stub, err := cmd.Flags().GetBool("stub"); err == nil {
		s.StubMode = stub
	}
	return s
}

var _ = time.Now
