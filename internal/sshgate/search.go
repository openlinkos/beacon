package sshgate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openlinkos/beacon/internal/store"
)

const (
	searchTimeout     = 10 * time.Second
	searchDefaultHits = 25
	searchMaxHits     = 50
	searchMaxEntries  = 256
	searchMaxDepth    = 4
	searchMaxBytes    = 64 << 10
	searchMaxQuery    = 128
	searchMaxPath     = 1024
	searchMaxPreview  = 256
)

// ErrSearchUnavailable means the enrolled machine lacks the standard shell utilities for search.
var ErrSearchUnavailable = errors.New("file search needs awk, head, tr and wc on the machine")

// SearchInput asks for literal, case-sensitive matches in an explicit project directory.
type SearchInput struct {
	Directory string `json:"directory" jsonschema:"absolute physical project directory"`
	Query     string `json:"query" jsonschema:"literal case-sensitive text on one line"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum matches (default 25, maximum 50)"`
}

type SearchMatch struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

type SearchResult struct {
	Machine         string              `json:"machine"`
	Directory       string              `json:"directory"`
	Matches         []SearchMatch       `json:"matches"`
	ScannedFiles    *int                `json:"scanned_files,omitempty"`
	Limited         bool                `json:"limited"`
	TimedOut        bool                `json:"timed_out,omitempty"`
	Session         string              `json:"session"`
	RecordingState  store.EvidenceState `json:"recording_state,omitempty"`
	TranscriptState store.EvidenceState `json:"transcript_state,omitempty"`
}

// SearchFiles searches a single authorised machine under its ordinary operating-system identity.
// The directory selects the search, without granting broader access than other file tools.
func (g *Gate) SearchFiles(ctx context.Context, who Actor, machine string, in SearchInput) (SearchResult, error) {
	m, err := g.machineFor(who, machine)
	if err != nil {
		return SearchResult{}, err
	}
	if !searchDirectory(in.Directory) {
		return SearchResult{}, fmt.Errorf("%w: directory must be an absolute project path without dot or parent components", ErrBadInput)
	}
	directory := path.Clean(in.Directory)
	if strings.TrimSpace(in.Query) == "" || len(in.Query) > searchMaxQuery || strings.ContainsAny(in.Query, "\r\n\x00") {
		return SearchResult{}, fmt.Errorf("%w: query must be literal text on one line, at most %d bytes", ErrBadInput, searchMaxQuery)
	}
	limit := in.Limit
	if limit == 0 {
		limit = searchDefaultHits
	}
	if limit < 1 || limit > searchMaxHits {
		return SearchResult{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrBadInput, searchMaxHits)
	}

	command := script(searchBody, directory, in.Query, strconv.Itoa(limit), strconv.Itoa(searchMaxEntries),
		strconv.Itoa(searchMaxDepth), strconv.Itoa(searchMaxBytes), strconv.Itoa(searchMaxPath), strconv.Itoa(searchMaxPreview))
	t, timedOut, err := g.execute(ctx, who, m, "file", "search "+directory, command, nil, searchTimeout)
	if err != nil {
		return SearchResult{}, err
	}
	current, err := g.machineFor(who, machine)
	if err != nil {
		return SearchResult{}, err
	}
	if current.Registration() != m.Registration() {
		return SearchResult{}, ErrMachineChanged
	}
	if !timedOut && t.Exit != nil && *t.Exit != 0 {
		switch *t.Exit {
		case 65:
			return SearchResult{}, fmt.Errorf("%w: open the physical project directory and use its absolute path", ErrBadInput)
		case 69:
			return SearchResult{}, ErrSearchUnavailable
		default:
			return SearchResult{}, fmt.Errorf("%w: the machine did not complete the search", ErrUnreachable)
		}
	}
	result := SearchResult{Machine: m.Name, Directory: directory, Matches: []SearchMatch{}, Session: t.ID,
		TimedOut: timedOut, Limited: timedOut || t.textTruncated || t.Stderr != "",
		RecordingState: t.RecordingState, TranscriptState: t.TranscriptState}
	result.collect(t.raw, limit)
	return result, nil
}

func searchDirectory(directory string) bool {
	if !path.IsAbs(directory) || path.Clean(directory) == "/" || len(directory) > searchMaxPath ||
		!utf8.ValidString(directory) || strings.ContainsFunc(directory, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(directory, "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return true
}

type searchFrame struct {
	Type string `json:"type"`
	SearchMatch
	Scanned int  `json:"scanned_files"`
	Limited bool `json:"limited"`
}

func (r *SearchResult) collect(raw string, limit int) {
	summary := false
	for line := range strings.SplitSeq(strings.TrimSuffix(raw, "\n"), "\n") {
		var f searchFrame
		if summary || json.Unmarshal([]byte(line), &f) != nil {
			r.Limited = true
			continue
		}
		switch f.Type {
		case "match":
			name, ok := searchWireText(f.Path)
			if !ok || !utf8.ValidString(name) || name == "" || path.IsAbs(name) || path.Clean(name) != name ||
				name == "." || name == ".." || strings.HasPrefix(name, "../") || f.Line < 1 || len(r.Matches) == limit {
				r.Limited = true
				continue
			}
			preview, ok := searchWireText(f.Text)
			if !ok {
				r.Limited = true
				continue
			}
			f.Path = path.Join(r.Directory, name)
			f.Text = strings.Map(func(r rune) rune {
				if unicode.IsControl(r) && r != '\t' {
					return -1
				}
				return r
			}, clean(strings.ToValidUTF8(preview, "\uFFFD")))
			r.Matches = append(r.Matches, f.SearchMatch)
		case "summary":
			if f.Scanned < 0 || f.Scanned > searchMaxEntries {
				r.Limited = true
				continue
			}
			summary = true
			r.ScannedFiles = &f.Scanned
			r.Limited = r.Limited || f.Limited
		default:
			r.Limited = true
		}
	}
	r.Limited = r.Limited || !summary
}

// searchWireText restores bytes escaped as Latin-1 code points in the remote ASCII JSON.
// This preserves unusual path names and lets the caller handle excerpts cut across a UTF-8 character.
func searchWireText(value string) (string, bool) {
	var bytes strings.Builder
	bytes.Grow(len(value))
	for _, r := range value {
		if r > 255 || r == 0 {
			return "", false
		}
		bytes.WriteByte(byte(r))
	}
	return bytes.String(), true
}

// The command uses the same small Unix tools as the existing file operations. The shell never
// interprets the query as code or an option. JSON lines keep newlines in file names unambiguous
// and remain readable in the retained transcript.
const searchBody = `
LC_ALL=C
export LC_ALL
for utility in awk head tr wc; do
	command -v "$utility" >/dev/null 2>&1 || exit 69
done
root=$1
needle=$2
remaining=$3
entry_limit=$4
depth_limit=$5
byte_limit=$6
path_limit=$7
preview=$8
cd -P "$root" || exit 65
[ "$(pwd -P)" = "$root" ] || exit 65
entries=0
scanned=0
limited=false
walk() {
	for entry in "$2"/*; do
		[ -L "$entry" ] || [ -e "$entry" ] || continue
		entries=$((entries + 1))
		if [ "$entries" -gt "$entry_limit" ] || [ "$remaining" -eq 0 ]; then
			limited=true
			return
		fi
		if [ -L "$entry" ]; then
			continue
		elif [ -d "$entry" ]; then
			if [ "$1" -ge "$depth_limit" ] || [ ! -r "$entry" ] || [ ! -x "$entry" ]; then
				limited=true
			else
				walk "$(($1 + 1))" "$entry"
			fi
		elif [ -f "$entry" ]; then
			scan_file "$entry"
		fi
	done
}
scan_file() {
	if [ ! -r "$1" ] || [ "${#1}" -gt "$path_limit" ]; then
		limited=true
		return
	fi
	scanned=$((scanned + 1))
	nul=$(head -c "$byte_limit" < "$1" | tr -d -c '\000' | wc -c)
	[ "$nul" -eq 0 ] || return
	head -c "$byte_limit" < "$1" |
		BEACON_SEARCH_NEEDLE="$needle" BEACON_SEARCH_PATH="${1#./}" \
		BEACON_SEARCH_BUDGET="$remaining" BEACON_SEARCH_BYTES="$byte_limit" BEACON_SEARCH_PREVIEW="$preview" awk '` + searchLines + `'
	code=$?
	if [ "$code" -ge 64 ]; then
		limited=true
		code=$((code - 64))
	fi
	if [ "$code" -gt "$remaining" ]; then
		limited=true
		return
	fi
	remaining=$((remaining - code))
	if [ "$remaining" -eq 0 ]; then
		limited=true
	fi
}
walk 1 .
printf '{"type":"summary","scanned_files":%d,"limited":%s}\n' "$scanned" "$limited"
`

const searchLines = `
function ascii_json(value, encoded, i, c, n) {
	encoded = "\""
	for (i = 1; i <= length(value); i++) {
		c = substr(value, i, 1)
		n = ordinal[c]
		if (c == "\"" || c == "\\") encoded = encoded "\\" c
		else if (n < 32 || n > 126) encoded = encoded sprintf("\\u%04x", n)
		else encoded = encoded c
	}
	return encoded "\""
}
BEGIN {
	for (i = 1; i <= 255; i++) ordinal[sprintf("%c", i)] = i
	needle = ENVIRON["BEACON_SEARCH_NEEDLE"]
	file = ascii_json(ENVIRON["BEACON_SEARCH_PATH"])
	budget = ENVIRON["BEACON_SEARCH_BUDGET"] + 0
	bytes_limit = ENVIRON["BEACON_SEARCH_BYTES"] + 0
	preview = ENVIRON["BEACON_SEARCH_PREVIEW"] + 0
}
{
	size += length($0) + 1
	position = index($0, needle)
	if (!position || count >= budget) next
	from = position - int(preview / 3)
	if (from < 1) from = 1
	clipped = (from > 1 || length($0) >= from + preview) ? "true" : "false"
	printf "{\"type\":\"match\",\"path\":%s,\"line\":%d,\"text\":%s,\"truncated\":%s}\n", file, NR, ascii_json(substr($0, from, preview)), clipped
	count++
}
END {
	exit count + (size >= bytes_limit ? 64 : 0)
}
`
