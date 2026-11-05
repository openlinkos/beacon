package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/openlinkos/beacon/internal/secrets"
)

// runSecretCmd is `beacon run`: beacon:// references in the environment and in a .env file are
// resolved through the agent's use grants and injected into a child process. The values never
// print; they live only in that process, the way `op run` does.
func runSecretCmd(args []string, stdout, stderr io.Writer) int {
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep == len(args)-1 {
		fmt.Fprintln(stderr, "run -agent NAME -reason TEXT [--env-file FILE] [--wait DURATION] -- CMD [ARG...]")
		return 2
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agent := fs.String("agent", "", "the agent whose grants the references ride")
	reason := fs.String("reason", "", "why; recorded with every use")
	envFile := fs.String("env-file", "", "a .env file whose beacon:// values are filled")
	wait := fs.String("wait", "", "how long to wait for a grant when none covers a reference (a Go duration, such as 30s)")
	if err := fs.Parse(args[:sep]); err != nil {
		return 2
	}
	if *agent == "" {
		fmt.Fprintln(stderr, "name the agent: -agent NAME")
		return 2
	}
	if strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "say why: -reason TEXT")
		return 2
	}
	cmdArgs := args[sep+1:]
	fileEnv := map[string]string{}
	if *envFile != "" {
		var err error
		fileEnv, err = parseEnvFile(*envFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	refs := map[string]string{}
	for k, v := range fileEnv {
		if strings.HasPrefix(v, secrets.RefPrefix) {
			refs[k] = v
		}
	}
	for _, e := range os.Environ() {
		k, v, ok := strings.Cut(e, "=")
		if ok && strings.HasPrefix(v, secrets.RefPrefix) {
			refs[k] = v
		}
	}
	resolved := map[string]string{}
	if len(refs) > 0 {
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		var err error
		resolved, err = c.VaultUse(context.Background(), *agent, refs, *reason, *wait)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	env := os.Environ()
	seen := map[string]bool{}
	put := func(k, v string) {
		env = append(env, k+"="+v)
		seen[k] = true
	}
	for k, v := range fileEnv {
		if r, ok := resolved[k]; ok {
			v = r
		}
		put(k, v)
	}
	for k, v := range resolved {
		if !seen[k] {
			put(k, v)
		}
	}
	cmd := exec.CommandContext(context.Background(), cmdArgs[0], cmdArgs[1:]...) //nolint:gosec // the operator names the command
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func parseEnvFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, nil
}
