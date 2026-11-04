package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const keyUsage = "key status | key unseal [--passphrase-file FILE | --passphrase-from-stdin] | key seal | key rotate [--passphrase-file FILE | --passphrase-from-stdin] | key rotate COLLECTION"

// keyCmd tends the vault's root key through the running service's socket: its state, unsealing the
// passphrase mode, sealing again, rotating the root key (every collection key wrapped again) or one
// collection's key (its items encrypted again in the background).
func keyCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, keyUsage)
		return 2
	}
	fs := flag.NewFlagSet("key "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	passFile := fs.String("passphrase-file", "", "a file holding the passphrase (the new one, for rotate); the secrets-directory way, never a shell argument")
	fromStdin := fs.Bool("passphrase-from-stdin", false, "read the passphrase from stdin (it shows while you type; prefer the file)")
	pos, ok := positional(fs, args[1:])
	if !ok {
		return 2
	}
	ctx := context.Background()
	passphrase := ""
	needPass := args[0] == "unseal" || args[0] == "rotate" && len(pos) == 0
	if needPass {
		p, err := readSecretInput(os.Stdin, stderr, *passFile, *fromStdin)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		passphrase = p
	}
	c, ok := dial(stderr)
	if !ok {
		return 1
	}
	switch args[0] {
	case "status":
		st, err := c.VaultState(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		state := "unsealed"
		if st.Sealed {
			state = "sealed"
		}
		fmt.Fprintf(stdout, "%s\t%s\n", state, st.Mode)
		for _, b := range st.Bad {
			fmt.Fprintf(stdout, "warning: a row failed its signature and is treated as absent: %s\n", b)
		}
		return 0
	case "unseal":
		st, err := c.VaultUnseal(ctx, passphrase)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "unsealed: %t\n", !st.Sealed)
		return 0
	case "seal":
		if err := c.VaultSeal(ctx); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "sealed")
		return 0
	case "rotate":
		if len(pos) == 1 { // one collection's key
			if err := c.VaultRotate(ctx, "", pos[0]); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			fmt.Fprintln(stdout, "the collection's key rotation started; the items are encrypted again in the background")
			return 0
		}
		if len(pos) != 0 {
			fmt.Fprintln(stderr, keyUsage)
			return 2
		}
		if err := c.VaultRotate(ctx, passphrase, ""); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "the root key was rotated; every collection key was wrapped again")
		return 0
	default:
		fmt.Fprintln(stderr, keyUsage)
		return 2
	}
}

// readSecretInput reads a secret the way Beacon reads them: from a file when one is named, else
// (when asked) a line from stdin — which shows while it is typed, so the file is the better way.
func readSecretInput(stdin *os.File, stderr io.Writer, file string, fromStdin bool) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file) //nolint:gosec // the operator names the file
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if !fromStdin {
		return "", fmt.Errorf("name the file that holds it (--passphrase-file FILE) or say --passphrase-from-stdin")
	}
	fmt.Fprintln(stderr, "passphrase: (it shows while you type; prefer --passphrase-file)")
	r := bufio.NewReader(stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
