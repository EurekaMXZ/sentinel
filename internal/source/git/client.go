package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"sentinel/internal/source"
)

const maxOutput = 32 << 20

type Client struct{ Binary, CacheDir string }

type workspace struct {
	client Client
	dir    string
	format string
}

func (c Client) Open(ctx context.Context, tenant, repository, format string) (source.Workspace, error) {
	if !source.ValidID(tenant) || !source.ValidID(repository) || (format != "sha1" && format != "sha256") {
		return nil, errors.New("invalid Git workspace identity")
	}
	root, err := filepath.Abs(filepath.Join(c.CacheDir, tenant, repository))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, "sync-")
	if err != nil {
		return nil, err
	}
	w := &workspace{client: c, dir: dir, format: format}
	if _, err := w.run(ctx, "", "", "init", "--bare", "--object-format="+format, "--template=", "."); err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}

func (w *workspace) Directory() string { return w.dir }
func (w *workspace) Close() error      { return os.RemoveAll(w.dir) }

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxOutput {
		return 0, errors.New("Git output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func (w *workspace) run(ctx context.Context, token, input string, args ...string) (string, error) {
	output, code, err := w.execute(ctx, token, input, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git %s failed with status %d", args[0], code)
	}
	return output, nil
}

// execute returns the exit code instead of treating every nonzero status as an
// error, so read operations can distinguish "no matches" from a real failure.
func (w *workspace) execute(ctx context.Context, token, input string, args ...string) (string, int, error) {
	options := []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
		"-c", "protocol.file.allow=always", "-c", "http.followRedirects=false", "-c", "credential.helper=",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "gc.auto=0", "-c", "maintenance.auto=false"}
	cmd := exec.CommandContext(ctx, w.client.Binary, append(options, args...)...)
	cmd.Dir = w.dir
	// Inherited Git configuration and tracing can redirect requests or expose secrets.
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_LFS_SKIP_SMUDGE=1", "LC_ALL=C")
	if token != "" {
		header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader", "GIT_CONFIG_VALUE_0="+header)
	}
	cmd.Stdin = strings.NewReader(input)
	var output, diagnostic boundedBuffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return output.String(), exit.ExitCode(), nil
		}
		// stderr can contain upstream-controlled text or credentials; keep it out of history.
		return "", 0, fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return output.String(), 0, nil
}

func (w *workspace) Import(ctx context.Context, path string) error {
	if _, err := w.run(ctx, "", "", "bundle", "verify", path); err != nil {
		return err
	}
	output, err := w.run(ctx, "", "", "bundle", "unbundle", path)
	if err != nil {
		return err
	}
	var updates strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !source.ValidOID(fields[0], w.format) || fields[1] != "refs/retained/"+fields[0] {
			return errors.New("bundle contains an invalid retained ref")
		}
		fmt.Fprintf(&updates, "update %s %s\n", fields[1], fields[0])
	}
	_, err = w.run(ctx, "", "start\n"+updates.String()+"prepare\ncommit\n", "update-ref", "--stdin")
	return err
}

// Fetch accepts a transport URL at this adapter boundary. The production
// Activity only passes normalized GitHub HTTPS URLs; local URLs enable Git tests.
func (w *workspace) Fetch(ctx context.Context, remote string, patterns []string, token string) ([]source.RefObservation, error) {
	if strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\x00\r\n") {
		return nil, errors.New("invalid Git remote")
	}
	for _, pattern := range patterns {
		if !source.ValidTrackedRef(pattern) {
			return nil, errors.New("invalid tracked ref")
		}
	}
	args := append([]string{"ls-remote", "--refs", "--", remote}, patterns...)
	output, err := w.run(ctx, token, "", args...)
	if err != nil {
		return nil, err
	}
	refs := make([]source.RefObservation, 0)
	seen := make(map[string]bool)
	var refspecs strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !source.ValidOID(fields[0], w.format) || !source.ValidTrackedRef(fields[1]) || strings.Contains(fields[1], "*") {
			return nil, errors.New("invalid remote ref advertisement")
		}
		matches := false
		for _, pattern := range patterns {
			matches = matches || source.MatchesRef(pattern, fields[1])
		}
		if !matches {
			continue
		}
		refs = append(refs, source.RefObservation{Name: fields[1], TargetOID: fields[0]})
		if !seen[fields[0]] {
			fmt.Fprintf(&refspecs, "+%s:refs/retained/%s\n", fields[0], fields[0])
			seen[fields[0]] = true
		}
		if len(refs) > 10000 {
			return nil, errors.New("remote exceeds 10000 tracked refs")
		}
	}
	if len(refs) != 0 {
		// Pin advertised OIDs so a moving branch cannot silently change this sync.
		_, err = w.run(ctx, token, refspecs.String(), "fetch", "--atomic", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--stdin", "--", remote)
		if err != nil {
			return nil, err
		}
	}
	for i := range refs {
		peeled, err := w.run(ctx, "", "", "rev-parse", "--verify", refs[i].TargetOID+"^{commit}")
		if err != nil {
			if strings.HasPrefix(refs[i].Name, "refs/heads/") {
				return nil, errors.New("branch does not point to a commit")
			}
			continue // Tags may identify trees or blobs.
		}
		refs[i].CommitSHA = strings.TrimSpace(peeled)
	}
	slices.SortFunc(refs, func(a, b source.RefObservation) int { return strings.Compare(a.Name, b.Name) })
	return refs, nil
}

func (w *workspace) State(ctx context.Context) (source.GitState, error) {
	var state source.GitState
	output, err := w.run(ctx, "", "", "for-each-ref", "--format=%(objectname)", "refs/retained/")
	if err != nil {
		return state, err
	}
	state.Roots = strings.Fields(output)
	slices.Sort(state.Roots)
	if len(state.Roots) == 0 {
		return state, nil
	}
	output, err = w.run(ctx, "", "", "log", "--all", "--format=%H %T", "--")
	if err != nil {
		return state, err
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !source.ValidOID(fields[0], w.format) || !source.ValidOID(fields[1], w.format) {
			return state, errors.New("invalid Git commit index")
		}
		state.Commits = append(state.Commits, source.Commit{SHA: fields[0], TreeSHA: fields[1]})
		if len(state.Commits) > 100000 {
			return state, errors.New("repository exceeds 100000 retained commits")
		}
	}
	return state, nil
}

func (w *workspace) Bundle(ctx context.Context, path string, previous []string) (source.BundleInfo, error) {
	var result source.BundleInfo
	state, err := w.State(ctx)
	if err != nil {
		return result, err
	}
	if len(state.Roots) == 0 {
		return source.BundleInfo{Empty: true}, nil
	}
	var revisions strings.Builder
	for _, oid := range state.Roots {
		fmt.Fprintf(&revisions, "refs/retained/%s\n", oid)
	}
	for _, oid := range previous {
		if !source.ValidOID(oid, w.format) {
			return result, errors.New("invalid durable root")
		}
		fmt.Fprintf(&revisions, "^%s\n", oid)
	}
	objects, err := w.run(ctx, "", revisions.String(), "rev-list", "--objects", "--no-object-names", "--stdin")
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(objects) == "" {
		return source.BundleInfo{Empty: true}, nil
	}
	if _, err := w.run(ctx, "", revisions.String(), "bundle", "create", path, "--stdin"); err != nil {
		return result, err
	}
	if _, err := w.run(ctx, "", "", "bundle", "verify", path); err != nil {
		return result, err
	}
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, maxOutput))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			return result, nil
		}
		if strings.HasPrefix(line, "-") {
			oid, _, _ := strings.Cut(line[1:], " ")
			if !source.ValidOID(oid, w.format) {
				return result, errors.New("invalid bundle prerequisite")
			}
			result.Prerequisites = append(result.Prerequisites, oid)
		}
	}
	return result, errors.New("invalid or oversized bundle header")
}

func (w *workspace) Verify(ctx context.Context) error {
	_, err := w.run(ctx, "", "", "fsck", "--full", "--strict", "--no-reflogs")
	return err
}
