package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/openlinkos/beacon/internal/secrets"
)

const vaultUsage = `secret ls [COLLECTION] [-state normal] | secret show REF | secret reveal REF [FIELD...] [-user NAME] [--password-file FILE | --password-from-stdin] |
secret set COLLECTION/TITLE [FIELD=VALUE ...] [-note TEXT] [-tags a,b] [-level auto|review|person|never|-] [-category TEXT] [-url URL] [-user NAME] [-reason TEXT] [--value-from-stdin | --value-file FILE] |
secret tag REF TAGS [-user NAME] [-reason TEXT] | secret level REF LEVEL [-user NAME] [-reason TEXT] |
secret archive REF | secret rm REF | secret restore REF [-user NAME] |
secret generate REF FIELD [-length 24] [-charset letters|alnum|all] [-user NAME] [-reason TEXT] |
secret rollback REF VERSION [-note TEXT] | secret collection NAME [-level review] [-user NAME] |
secret perms COLLECTION [user:NAME|role:ROLE=describe|read|edit|manage|-] [-user NAME] [-reason TEXT] |
secret tags [NAME [LEVEL] [-description TEXT]] [-user NAME]`

// secretCmd works the vault through the running service's socket, as `beacon secret …`. Everything
// is done as a named account (-user, an owner for the policy changes), and viewing values needs
// that account's password — the command line is a person's hands, under the same rules as the
// console.
func secretCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, vaultUsage)
		return 2
	}
	fs := flag.NewFlagSet("secret "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	user := fs.String("user", "", "the account whose hands these are; every change is audited as them")
	reason := fs.String("reason", "", "why; kept with the change and shown in the alert chat")
	note := fs.String("note", "", "why (rollback)")
	state := fs.String("state", "normal", "normal, archived or trash")
	tags := fs.String("tags", "", "comma-separated tags, for set and tag")
	level := fs.String("level", "", "the approval level (auto, review, person, never), or - to clear the item's own")
	category := fs.String("category", "", "the item's category, for set")
	urlFlag := fs.String("url", "", "a URL of the item, for set (repeat not supported; give one)")
	setNote := fs.String("set-note", "", "the item's notes, for set")
	length := fs.Int("length", 24, "how long a generated value is")
	charset := fs.String("charset", "letters", "letters, alnum or all")
	description := fs.String("description", "", "a collection's or tag's description")
	passwordFile := fs.String("password-file", "", "a file holding the account's password, for reveal")
	passwordStdin := fs.Bool("password-from-stdin", false, "read the account's password from stdin (it shows; prefer the file)")
	valueFile := fs.String("value-file", "", "a file holding a field's value for set (use FIELD=-); prefer this over putting the value on the command line")
	valueStdin := fs.Bool("value-from-stdin", false, "read a field's value from stdin for set (use FIELD=-); prefer this over putting the value on the command line")
	pos, ok := positional(fs, args[1:])
	if !ok {
		return 2
	}
	if *user == "" {
		*user = os.Getenv("USERNAME")
		if *user == "" {
			*user = os.Getenv("USER")
		}
	}
	if *user == "" {
		fmt.Fprintln(stderr, "name the account whose hands these are: -user NAME")
		return 2
	}
	ctx := context.Background()
	c, ok := dial(stderr)
	if !ok {
		return 1
	}
	switch args[0] {
	case "ls":
		if len(pos) > 1 {
			fmt.Fprintln(stderr, vaultUsage)
			return 2
		}
		if len(pos) == 0 {
			cols, err := c.VaultCollections(ctx)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "COLLECTION\tLEVEL\tITEMS")
			for _, col := range cols {
				_, items, err := c.VaultItems(ctx, *user, col.Name, "")
				if err != nil {
					fmt.Fprintln(stderr, err)
					return 1
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\n", col.Name, col.Level, len(items))
			}
			_ = tw.Flush()
			return 0
		}
		_, items, err := c.VaultItems(ctx, *user, pos[0], *state)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "TITLE\tVERSION\tSTATE\tLEVEL\tTAGS")
		for _, it := range items {
			_, _, lvl, _, err := c.VaultItem(ctx, *user, it.ID)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", it.Title, it.Version, it.State, lvl, strings.Join(it.Tags, ","))
		}
		_ = tw.Flush()
		return 0
	case "collection":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "secret collection NAME [-level LEVEL] [-description TEXT]")
			return 2
		}
		lv := *level
		if lv == "" {
			lv = secrets.LevelReview
		}
		col, err := c.VaultCreateCollection(ctx, *user, pos[0], *description, lv)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "made %s (%s), key version %d\n", col.Name, col.Level, col.KeyVersion)
		return 0
	case "show":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, "secret show REF")
			return 2
		}
		item, col, lvl, versions, err := c.VaultItem(ctx, *user, pos[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ref := "beacon://" + col.Name + "/" + item.Title
		fmt.Fprintf(stdout, "%s\t%s\tversion %d\tlevel %s\tstate %s\n", item.Title, col.Name, item.Version, lvl, item.State)
		fmt.Fprintf(stdout, "ref %s\n", ref)
		if len(item.Tags) > 0 {
			fmt.Fprintf(stdout, "tags %s\n", strings.Join(item.Tags, ","))
		}
		for _, v := range versions {
			if v.Version != item.Version {
				continue
			}
			for _, f := range v.Fields {
				name := f.Name
				if f.Section != "" {
					name = f.Section + "/" + f.Name
				}
				fmt.Fprintf(stdout, "field %s\t%s\t%s\n", name, f.Type, f.Purpose)
			}
		}
		fmt.Fprintf(stdout, "%d version(s)\n", len(versions))
		return 0
	case "reveal":
		if len(pos) < 1 {
			fmt.Fprintln(stderr, "secret reveal REF [FIELD...]")
			return 2
		}
		password, err := readSecretInput(os.Stdin, stderr, *passwordFile, *passwordStdin)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var fields []string
		if len(pos) > 1 {
			fields = pos[1:]
		}
		values, _, err := c.VaultReveal(ctx, *user, pos[0], password, fields)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(stdout, "%s\t%s\n", k, values[k])
		}
		return 0
	case "set":
		if len(pos) < 1 {
			fmt.Fprintln(stderr, "secret set COLLECTION/TITLE [FIELD=VALUE ...]")
			return 2
		}
		in := secrets.ItemIn{Reason: *reason, HasNotes: *setNote != "", Notes: *setNote}
		if *setNote != "" {
			in.Notes = *setNote
		}
		collection, title, found := strings.Cut(pos[0], "/")
		in.Collection = collection
		if found && title != "" {
			in.Title = title
		}
		if *tags != "" {
			in.Tags = strings.Split(*tags, ",")
		}
		if *level != "" {
			in.Level = *level
		}
		if *category != "" {
			in.Category = *category
		}
		if *urlFlag != "" {
			in.URLs = []string{*urlFlag}
		}
		fromStdin := 0
		for _, f := range pos[1:] {
			name, value, ok := strings.Cut(f, "=")
			if !ok {
				fmt.Fprintf(stderr, "a field is FIELD=VALUE, not %q\n", f)
				return 2
			}
			if value == "-" && (*valueFile != "" || *valueStdin) {
				fromStdin++
				got, err := readFieldValue(os.Stdin, stderr, *valueFile, *valueStdin)
				if err != nil {
					fmt.Fprintln(stderr, err)
					return 1
				}
				value = got
			}
			typ := secrets.FieldConcealed
			if name == "username" {
				typ = secrets.FieldString
			}
			section, name2, hasSection := strings.Cut(name, "/")
			field := secrets.FieldIn{Name: name, Type: typ, Value: value}
			if hasSection {
				field.Section, field.Name = section, name2
			}
			in.Fields = append(in.Fields, field)
		}
		if fromStdin > 1 {
			fmt.Fprintln(stderr, "only one field may be FIELD=- with --value-from-stdin or --value-file")
			return 2
		}
		item, err := c.VaultSetItem(ctx, *user, in)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s version %d\n", item.Title, item.Version)
		return 0
	case "tag", "level":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, vaultUsage)
			return 2
		}
		item, _, _, _, err := c.VaultItem(ctx, *user, pos[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		in := secrets.ItemIn{ID: item.ID, Collection: item.Collection, Reason: *reason}
		if args[0] == "tag" {
			in.Tags = strings.Split(pos[1], ",")
		} else {
			in.Level = pos[1]
		}
		out, err := c.VaultSetItem(ctx, *user, in)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s version %d\n", out.Title, out.Version)
		return 0
	case "archive", "rm", "restore":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, vaultUsage)
			return 2
		}
		item, err := c.VaultStateChange(ctx, *user, pos[0], args[0], *reason, 0, "", "", 0, "")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s %s\n", item.Title, item.State)
		return 0
	case "rollback":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "secret rollback REF VERSION")
			return 2
		}
		version, err := strconv.ParseInt(pos[1], 10, 64)
		if err != nil {
			fmt.Fprintln(stderr, "the version is a whole number")
			return 2
		}
		item, err := c.VaultStateChange(ctx, *user, pos[0], "rollback", *note, version, "", "", 0, "")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s version %d\n", item.Title, item.Version)
		return 0
	case "generate":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "secret generate REF FIELD")
			return 2
		}
		item, err := c.VaultStateChange(ctx, *user, pos[0], "generate", *reason, 0, "", pos[1], *length, *charset)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: a new value was generated and stored as version %d (the value is not shown; reveal it with reason)\n", item.Title, item.Version)
		return 0
	case "perms":
		if len(pos) < 1 || len(pos) > 2 {
			fmt.Fprintln(stderr, "secret perms COLLECTION [user:NAME=perm|-]")
			return 2
		}
		if len(pos) == 1 {
			rows, err := c.VaultPerms(ctx, pos[0])
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "SUBJECT\tPERM\tBY\tWHEN")
			for _, p := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Subject, p.Perm, p.Actor, p.At.Format("2006-01-02 15:04"))
			}
			_ = tw.Flush()
			return 0
		}
		subject, permPart, ok2 := strings.Cut(pos[1], "=")
		if !ok2 {
			fmt.Fprintln(stderr, "a permission is user:NAME=perm or user:NAME=- to remove")
			return 2
		}
		perm := permPart
		if perm == "-" {
			perm = ""
		}
		if err := c.VaultSetPerm(ctx, *user, pos[0], subject, perm, *reason); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "set %s on %s\n", pos[1], pos[0])
		return 0
	case "tags":
		if len(pos) == 0 {
			tags, err := c.VaultTags(ctx)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "TAG\tLEVEL\tDESCRIPTION")
			for _, t := range tags {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, t.Level, t.Description)
			}
			_ = tw.Flush()
			return 0
		}
		if len(pos) == 1 || len(pos) > 2 {
			fmt.Fprintln(stderr, "secret tags [NAME [LEVEL]]")
			return 2
		}
		if err := c.VaultSetTag(ctx, *user, pos[0], pos[1], *description); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "set tag %s\n", pos[0])
		return 0
	default:
		fmt.Fprintln(stderr, vaultUsage)
		return 2
	}
}

// readFieldValue reads a field's value from a file or stdin, so `secret set` does not put the
// secret on the command line.
func readFieldValue(stdin *os.File, stderr io.Writer, file string, fromStdin bool) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file) //nolint:gosec // the operator names the file
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if !fromStdin {
		return "", fmt.Errorf("name the file that holds it (--value-file FILE) or say --value-from-stdin")
	}
	fmt.Fprintln(stderr, "value: (it shows while you type; prefer --value-file)")
	r := bufio.NewReader(stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
