// Command agent starts or attaches to a persisted root Agent Workflow.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"sentinel/internal/agent"
	"sentinel/internal/app"
	"sentinel/internal/platform/config"
)

func main() {
	options := app.StartAgentOptions{Limits: agent.DefaultLimits(), Output: os.Stdout}
	flag.StringVar(&options.TenantID, "tenant", "", "tenant UUID (required)")
	flag.StringVar(&options.Prompt, "task", "", "task text")
	taskFile := flag.String("task-file", "", "read task text from a UTF-8 file instead of -task")
	resourcesFile := flag.String("resources", "", "optional JSON file containing a resource-binding array")
	flag.StringVar(&options.ContextRef, "context", "", "optional stored initial-context object UUID")
	flag.StringVar(&options.ResumeID, "resume", "", "attach to or retry scheduling an existing root Agent UUID")
	flag.BoolVar(&options.Wait, "wait", false, "wait for completion and print the summary")
	flag.Int64Var(&options.Limits.MaxSteps, "max-steps", options.Limits.MaxSteps, "maximum model steps per Agent")
	flag.IntVar(&options.Limits.MaxChildDepth, "max-child-depth", options.Limits.MaxChildDepth, "maximum child depth; 0 disables delegation")
	flag.Parse()
	if flag.NArg() != 0 || options.TenantID == "" || (options.Prompt != "" && *taskFile != "") ||
		(options.ResumeID == "" && options.Prompt == "" && *taskFile == "") ||
		(options.ResumeID != "" && (options.Prompt != "" || *taskFile != "" || *resourcesFile != "" || options.ContextRef != "")) {
		fmt.Fprintln(os.Stderr, "Usage: agent -tenant UUID (-task text | -task-file path | -resume UUID) [-wait]")
		flag.PrintDefaults()
		os.Exit(2)
	}
	app.Main("agent", func(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
		if *taskFile != "" {
			data, err := os.ReadFile(*taskFile)
			if err != nil {
				return err
			}
			options.Prompt = string(data)
		}
		if *resourcesFile != "" {
			data, err := os.ReadFile(*resourcesFile)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &options.Resources); err != nil {
				return errors.New("resources file must contain a JSON resource-binding array")
			}
		}
		return app.StartAgent(ctx, cfg, logger, options)
	})
}
