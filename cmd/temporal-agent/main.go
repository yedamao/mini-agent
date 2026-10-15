// temporal-agent runs the mini-agent loop as a durable Temporal workflow.
//
//	temporal server start-dev                       # local server, UI at http://localhost:8233
//	go run ./cmd/temporal-agent worker              # runs workflows and activities
//	go run ./cmd/temporal-agent start -wait "task"  # starts a run (and waits for the answer)
//
// The worker reads the API key from its environment (AGENT_API_KEY or
// DEEPSEEK_API_KEY). The starter never needs it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/yedamao/mini-agent/internal/llm"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: temporal-agent worker | start [-dir d] [-id id] [-wait] task")
		os.Exit(2)
	}
	c, err := client.Dial(client.Options{HostPort: env("TEMPORAL_ADDRESS", client.DefaultHostPort)})
	if err != nil {
		log.Fatal("connect to temporal: ", err)
	}
	defer c.Close()
	switch os.Args[1] {
	case "worker":
		runWorker(c)
	case "start":
		runStart(c, os.Args[2:])
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func runWorker(c client.Client) {
	key := env("AGENT_API_KEY", os.Getenv("DEEPSEEK_API_KEY"))
	if key == "" {
		log.Fatal("no API key: set AGENT_API_KEY or DEEPSEEK_API_KEY")
	}
	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(AgentWorkflow)
	w.RegisterActivity(&Activities{Client: &llm.Client{
		BaseURL:     env("AGENT_BASE_URL", "https://api.deepseek.com/anthropic"),
		Model:       env("AGENT_MODEL", "deepseek-chat"),
		APIKey:      key,
		CallTimeout: 2 * time.Minute,
		MaxAttempts: 1, // Temporal's RetryPolicy does the retrying
	}})
	// Run until Ctrl-C/SIGTERM. Killing it mid-run is safe: see the README.
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}

func runStart(c client.Client, args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	dir := fs.String("dir", ".", "directory the tools run in (on the worker)")
	id := fs.String("id", "", "workflow ID (default: generated)")
	wait := fs.Bool("wait", false, "wait for the result and print it")
	fs.Parse(args)
	task := strings.Join(fs.Args(), " ")
	if task == "" {
		log.Fatal("no task given")
	}
	if *dir != "" {
		if abs, err := filepath.Abs(*dir); err == nil {
			*dir = abs
		}
	}
	opts := client.StartWorkflowOptions{ID: *id, TaskQueue: TaskQueue}
	if opts.ID == "" {
		opts.ID = fmt.Sprintf("mini-agent-%d", time.Now().Unix()) // starter, not workflow code: time is fine here
	}
	run, err := c.ExecuteWorkflow(context.Background(), opts, AgentWorkflow, Input{Task: task, Dir: *dir})
	if err != nil {
		log.Fatal("start: ", err)
	}
	fmt.Printf("started workflow %s (run %s)\nUI: http://localhost:8233/namespaces/default/workflows/%s\n", run.GetID(), run.GetRunID(), run.GetID())
	if !*wait {
		return
	}
	var answer string
	if err := run.Get(context.Background(), &answer); err != nil {
		log.Fatal("workflow failed: ", err)
	}
	fmt.Println(answer)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
