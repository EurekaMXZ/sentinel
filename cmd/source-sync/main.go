// Command source-sync imports a GitHub repository or synchronizes an existing one.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"sentinel/internal/app"
	"sentinel/internal/platform/config"
)

func main() {
	options := app.StartSourceOptions{Output: os.Stdout}
	flag.StringVar(&options.TenantID, "tenant", "", "tenant UUID (required)")
	flag.StringVar(&options.URL, "url", "", "create a repository from https://github.com/owner/repository")
	flag.StringVar(&options.RepositoryID, "repository", "", "sync an existing repository UUID")
	flag.StringVar(&options.ResumeID, "resume", "", "attach to an existing sync UUID")
	flag.StringVar(&options.CredentialRef, "credential-ref", "", "private repository credential reference, e.g. env:GITHUB_TOKEN")
	refs := flag.String("refs", "", "comma-separated tracked refs for a new repository; default branches and tags")
	flag.BoolVar(&options.Wait, "wait", false, "wait for publication and print new snapshots")
	flag.Parse()
	if flag.NArg() != 0 || options.TenantID == "" {
		fmt.Fprintln(os.Stderr, "Usage: source-sync -tenant UUID (-url URL | -repository UUID | -resume UUID) [-wait]")
		flag.PrintDefaults()
		os.Exit(2)
	}
	if *refs != "" {
		for _, ref := range strings.Split(*refs, ",") {
			options.TrackedRefs = append(options.TrackedRefs, strings.TrimSpace(ref))
		}
	}
	app.Main("source-sync", func(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
		return app.StartSourceSync(ctx, cfg, logger, options)
	})
}
