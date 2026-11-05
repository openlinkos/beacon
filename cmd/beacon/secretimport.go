package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/secrets/importer"
)

// secretImportCmd is `beacon secret import …`: the export is parsed where the command runs — on
// the operator's own machine, the way the design recommends — and the batch travels to the service
// over the socket, values and all. A dry-run sends no values at all: the batch is stripped to its
// shapes and its user names (the conflict rule turns them over), so a look before the leap costs
// nothing but counts. The report never prints a value; when it is done, the command says to delete
// the plaintext export.
const importUsage = `secret import SOURCE -file PATH [-dry-run] [-collection NAME] [-on-conflict ask|new_version|skip|keep_both] [-level COL=LEVEL,...] [-map name=HEADER,...] [-vault NAME] [-user NAME] [-reason TEXT]
  SOURCE: 1pux | 1password-csv | op | chrome | bitwarden | csv
  1pux            a 1Password unencrypted export (recommended; brings attachments and history)
  1password-csv   a 1Password CSV export (Login and Password items)
  op              the signed-in op command line (over about 300 items, prefer a 1PUX export)
  chrome          Chrome's password CSV (into one collection; same credential, many sites, one item)
  bitwarden       Bitwarden's JSON export, unencrypted (into one collection; folders become tags)
  csv             any CSV, with the columns named by -map`

func secretImportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("secret import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "the export file (not for op, which reads the account itself)")
	collection := fs.String("collection", "", "the one collection the import goes into (chrome, bitwarden, csv)")
	onConflict := fs.String("on-conflict", "", "ask (the default), new_version, skip or keep_both")
	levelSpec := fs.String("level", "", "a level to start a collection at, as COLLECTION=LEVEL, comma-separated")
	columns := fs.String("map", "", "the CSV's columns, as name=HEADER comma-separated (title, url, username, password, otp, notes, tags, category)")
	vaultFlag := fs.String("vault", "", "op: one vault (name or ID); default every vault")
	dryRun := fs.Bool("dry-run", false, "answer the report and write nothing")
	user := fs.String("user", "", "the account whose hands these are; every change is audited as them")
	reason := fs.String("reason", "", "why; kept with the change and shown in the alert chat")
	pos, ok := positional(fs, args)
	if !ok {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, importUsage)
		return 2
	}
	source := pos[0]
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
	opts := secrets.ImportOptions{Reason: *reason, OnConflict: *onConflict, Collection: *collection}
	var batch importer.Batch
	var err error
	ctx := context.Background()
	switch source {
	case "1pux":
		if *file == "" {
			fmt.Fprintln(stderr, "1pux needs -file PATH (the .1pux export)")
			return 2
		}
		batch, err = readImportFile(*file, importer.Parse1PUX)
	case "1password-csv":
		if *file == "" {
			fmt.Fprintln(stderr, "1password-csv needs -file PATH")
			return 2
		}
		name := *collection
		if name == "" {
			name = "Imports"
		}
		batch, err = readImportFile(*file, func(r io.Reader) (importer.Batch, error) {
			return importer.Parse1PasswordCSV(r, name)
		})
	case "op":
		var vaults []string
		if *vaultFlag != "" {
			vaults = strings.Split(*vaultFlag, ",")
		}
		batch, err = (importer.OpConnector{}).ParseOp(ctx, vaults)
	case "chrome":
		if *file == "" {
			fmt.Fprintln(stderr, "chrome needs -file PATH (Chrome's password CSV)")
			return 2
		}
		name := *collection
		if name == "" {
			name = "Chrome"
		}
		batch, err = readImportFile(*file, func(r io.Reader) (importer.Batch, error) {
			return importer.ParseChrome(r, name)
		})
	case "bitwarden":
		if *file == "" {
			fmt.Fprintln(stderr, "bitwarden needs -file PATH (the unencrypted JSON export)")
			return 2
		}
		name := *collection
		if name == "" {
			name = "Bitwarden"
		}
		batch, err = readImportFile(*file, func(r io.Reader) (importer.Batch, error) {
			return importer.ParseBitwarden(r, name)
		})
	case "csv":
		if *file == "" {
			fmt.Fprintln(stderr, "csv needs -file PATH")
			return 2
		}
		if *collection == "" {
			fmt.Fprintln(stderr, "csv needs -collection NAME (the one collection it goes into)")
			return 2
		}
		cols, cerr := parseColumnMap(*columns)
		if cerr != nil {
			fmt.Fprintln(stderr, cerr)
			return 2
		}
		batch, err = readImportFile(*file, func(r io.Reader) (importer.Batch, error) {
			return importer.ParseCSV(r, *collection, cols)
		})
	default:
		fmt.Fprintf(stderr, "unknown source %q; one of 1pux, 1password-csv, op, chrome, bitwarden, csv\n", source)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(batch.Collections) == 0 {
		fmt.Fprintln(stderr, "the export holds nothing to import")
		return 1
	}
	for _, part := range strings.Split(*levelSpec, ",") {
		if part == "" {
			continue
		}
		name, lvl, found := strings.Cut(part, "=")
		if !found || !secrets.ValidLevel(lvl) {
			fmt.Fprintf(stderr, "a level is COLLECTION=LEVEL with LEVEL one of auto, review, person, never, not %q\n", part)
			return 2
		}
		if opts.Levels == nil {
			opts.Levels = map[string]string{}
		}
		opts.Levels[strings.TrimSpace(name)] = lvl
	}
	c, ok := dial(stderr)
	if !ok {
		return 1
	}
	send := batch
	if *dryRun {
		opts.DryRun = true
		send = batch.ForDryRun()
	}
	report, err := c.VaultImport(ctx, *user, send, opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	printImportReport(stdout, report)
	if *file != "" {
		fmt.Fprintf(stdout, "Delete the plaintext export file %s now that it is imported.\n", *file)
	}
	printImportFollowups(stdout, report)
	return 0
}

// readImportFile opens the export read-only and parses it; the file never goes anywhere but the
// parser.
func readImportFile(path string, parse func(io.Reader) (importer.Batch, error)) (importer.Batch, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the operator names their own export file
	if err != nil {
		return importer.Batch{}, err
	}
	defer f.Close() //nolint:errcheck // a read-only file the command is done with
	return parse(f)
}

// parseColumnMap reads "title=Account,url=Login URL" into the CSV's column mapping.
func parseColumnMap(in string) (importer.Columns, error) {
	var cols importer.Columns
	for _, part := range strings.Split(in, ",") {
		if part == "" {
			continue
		}
		name, header, found := strings.Cut(part, "=")
		if !found || header == "" {
			return cols, fmt.Errorf("a column is name=HEADER, not %q", part)
		}
		switch strings.TrimSpace(name) {
		case "title":
			cols.Title = header
		case "url":
			cols.URL = header
		case "username":
			cols.Username = header
		case "password":
			cols.Password = header
		case "otp":
			cols.OTP = header
		case "notes":
			cols.Notes = header
		case "tags":
			cols.Tags = header
		case "category":
			cols.Category = header
		default:
			return cols, fmt.Errorf("a column is one of title, url, username, password, otp, notes, tags, category, not %q", name)
		}
	}
	return cols, nil
}

// printImportReport writes the report: counts, categories, levels, conflicts and outcomes — never
// a value.
func printImportReport(w io.Writer, report secrets.ImportReport) {
	kind := "import"
	if report.DryRun {
		kind = "dry-run"
	}
	fmt.Fprintf(w, "%s from %s\n", kind, report.Source)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "COLLECTION\tLEVEL\tITEMS\tFIELDS\tOTPS\tATTACHMENTS\tARCHIVED\tNEVER\tCATEGORIES")
	for _, col := range report.Collections {
		mark := ""
		if col.New {
			mark = " (new)"
		}
		var cats []string
		for _, c := range col.Categories {
			cats = append(cats, fmt.Sprintf("%s %d", c.Name, c.Count))
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n", col.Name, mark, col.Level, col.Items,
			col.Fields, col.OTPs, col.Attachments, col.Archived, col.Never, strings.Join(cats, ", "))
	}
	_ = tw.Flush()
	if len(report.Conflicts) > 0 {
		fmt.Fprintln(w, "Conflicts (the same place and the same user name; no value is shown):")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "COLLECTION\tINCOMING\tALREADY THERE\tSHARED URLS")
		for _, c := range report.Conflicts {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", c.Collection, c.Incoming, c.Existing, c.SharedURLs)
		}
		_ = tw.Flush()
	}
	if report.DryRun {
		return
	}
	fmt.Fprintf(w, "created %d, versions added %d, skipped %d, held back %d, kept both %d", report.Created, report.Versions, report.Skipped, report.HeldBack, report.KeptBoth)
	if report.Merged > 0 {
		fmt.Fprintf(w, ", rows merged %d", report.Merged)
	}
	fmt.Fprintln(w)
}

// printImportFollowups writes what the design says an import ends with: the levels to confirm, the
// never suggestions to check, and the entitlements agents will need (a later package brings them).
func printImportFollowups(w io.Writer, report secrets.ImportReport) {
	fmt.Fprintln(w, "Follow-ups in the console:")
	for _, col := range report.Collections {
		fmt.Fprintf(w, "  - confirm the %s collection's level (%s)\n", col.Name, col.Level)
	}
	if never := neverCollections(report); len(never) > 0 {
		fmt.Fprintf(w, "  - check the items started at never in %s\n", strings.Join(never, ", "))
	}
	fmt.Fprintln(w, "  - give the agents their entitlements (search_secrets needs them before any agent sees an item)")
}

func neverCollections(report secrets.ImportReport) []string {
	var out []string
	for _, col := range report.Collections {
		if col.Never > 0 {
			out = append(out, col.Name)
		}
	}
	return out
}
