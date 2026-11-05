package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/scrub"
	"github.com/openlinkos/beacon/internal/store"
)

// The files an item declares: where on which machine its value is put, as whom and with which mode.
// A service's .env, a compose file's environment, a TLS key — written by Beacon, so nobody copies a
// value by hand. When the item gains a new version the files are dealt with again as file.on_change
// says: delivered at once (the default), held for the next maintenance window, or only the holders
// notified. An agent reading such a file back through read_file sees it scrubbed: the values a
// machine holds are not for the agent's context.
//
// The delivery is the system's, not an agent's: it goes through the channel the wiring hands the
// vault (the SSH channel's own writer), as the machine's own user, and every delivery is a recorded
// session like any other.

// FileDeliverer writes a file to a machine, as the owner and with the mode given. The SSH channel
// stands in; nil (a test, or a deployment without the channel) records the delivery without making
// it.
type FileDeliverer interface {
	DeliverFile(ctx context.Context, machine, path, content, owner, mode string) error
}

// UseDeps are the use paths' own dependencies, set after the vault opens (SetUseDeps): where files
// are delivered, and whether a maintenance window is on (the next_window policy of file.on_change).
// A nil field leaves what was set before.
type UseDeps struct {
	Deliver    FileDeliverer
	Maintenance func() bool
}

// SetUseDeps fills the use paths' dependencies; only the fields that are set move.
func (s *Service) SetUseDeps(d UseDeps) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Deliver != nil {
		s.d.Deliver = d.Deliver
	}
	if d.Maintenance != nil {
		s.d.Maintenance = d.Maintenance
	}
}

// fileState is what became of one declared file: which version was delivered, when, whether a newer
// one waits for its moment, and the last error when it could not be written.
type fileState struct {
	Version   int64     `json:"version"`
	At        time.Time `json:"at"`
	Pending   int64     `json:"pending,omitempty"` // the version waiting for its window
	LastErr   string    `json:"last_error,omitempty"`
	FailedAt  time.Time `json:"failed_at,omitzero"`
}

func fileStateKey(item, machine, path string) []byte {
	return []byte("filedel\x00" + item + "\x00" + machine + "\x00" + path)
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var ownerRe = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]*(:[A-Za-z0-9_.][A-Za-z0-9_.-]*)?$`)
var modeRe = regexp.MustCompile(`^[0-7]{3,4}$`)

// checkFiles is the shape check for the files an item declares.
func checkFiles(files []ItemFile) error {
	for _, f := range files {
		switch {
		case f.Machine == "":
			return fmt.Errorf("%w: a declared file names its machine", ErrBadInput)
		case !strings.HasPrefix(f.Path, "/"):
			return fmt.Errorf("%w: a declared file's path is absolute, starting with /", ErrBadInput)
		case f.Owner != "" && !ownerRe.MatchString(f.Owner):
			return fmt.Errorf("%w: the owner of %s is a user name, or user:group", ErrBadInput, f.Path)
		case f.Mode != "" && !modeRe.MatchString(f.Mode):
			return fmt.Errorf("%w: the mode of %s is octal, like 0600", ErrBadInput, f.Path)
		case f.Field != "" && strings.ContainsAny(f.Field, "/\x00"):
			return fmt.Errorf("%w: the field of %s is a field name", ErrBadInput, f.Path)
		}
	}
	return nil
}

// deliverFiles deals with an item's files after a change: a file never delivered goes out at once;
// one whose value changed goes out as file.on_change says (deliver now, wait for a maintenance
// window, or only let the holders know, which notifyItemChanged has done already).
func (s *Service) deliverFiles(item Item, col Collection) {
	if len(item.Files) == 0 {
		return
	}
	onChange := s.settingString("file.on_change", "deliver")
	for _, f := range item.Files {
		var st fileState
		haveState := false
		_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
			b := tx.Get(bucketMeta, fileStateKey(item.ID, f.Machine, f.Path))
			haveState = b != nil && json.Unmarshal(b, &st) == nil
			return nil
		})
		switch {
		case !haveState:
			s.deliverOneFile(item, col, f, fileState{})
		case st.Version >= item.Version && st.Pending == 0:
			// already at this version, nothing waiting
		case st.Pending > 0:
			// a change already waiting for its window; the newer version waits with it
			st.Pending = item.Version
			s.putFileState(item.ID, f, st)
		default:
			switch onChange {
			case "next_window":
				st.Pending = item.Version
				s.putFileState(item.ID, f, st)
				_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_file_pending",
					Target: item.Title, Detail: "the file " + f.Path + " on " + f.Machine + " waits for a maintenance window (file.on_change)"})
			case "notify":
				// the holders were notified by notifyItemChanged; the file keeps the value it has
				_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_file_held",
					Target: item.Title, Detail: "the file " + f.Path + " on " + f.Machine + " keeps its value (file.on_change: notify)"})
			default: // deliver
				s.deliverOneFile(item, col, f, st)
			}
		}
	}
}

// deliverOneFile renders the file's content from the item's current version and writes it through
// the deliverer, keeping the state that says where the delivery stands.
func (s *Service) deliverOneFile(item Item, col Collection, f ItemFile, st fileState) {
	ver, ok := s.Version(item.ID, item.Version)
	if !ok {
		return
	}
	d, err := s.decryptDetailsOf(col, ver)
	if err != nil {
		s.fileFailed(item, f, st, "the current version could not be opened: "+err.Error())
		return
	}
	content, err := renderFile(f, ver, d)
	if err != nil {
		s.fileFailed(item, f, st, err.Error())
		return
	}
	if s.d.Deliver == nil {
		// no deliverer wired: the state says the delivery waits, and the log says why
		s.fileFailed(item, f, st, "no file deliverer is wired")
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()
	if err := s.d.Deliver.DeliverFile(ctx, f.Machine, f.Path, content, f.Owner, f.Mode); err != nil {
		s.fileFailed(item, f, st, err.Error())
		return
	}
	sum := sha256.Sum256([]byte(content))
	st = fileState{Version: item.Version, At: s.now()}
	s.putFileState(item.ID, f, st)
	_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_file_deliver",
		Target: item.Title, Detail: fmt.Sprintf("delivered %s to %s:%s (version %d, sha256 %s, %d bytes)",
			item.Title, f.Machine, f.Path, item.Version, hex.EncodeToString(sum[:8]), len(content))})
	if s.d.Bot != nil {
		_ = s.d.Bot.Send(s.ctx, s.t("The secret \"%s\" was delivered to %s:%s.", item.Title, f.Machine, f.Path))
	}
}

// fileFailed records a delivery that could not be made, and tells the chat the first time an error
// is new (the sweep keeps trying; the chat need not hear every try).
func (s *Service) fileFailed(item Item, f ItemFile, st fileState, msg string) {
	newErr := st.LastErr != msg
	st.LastErr, st.FailedAt, st.Version = msg, s.now(), st.Version
	if st.Pending == 0 {
		st.Pending = item.Version
	}
	s.putFileState(item.ID, f, st)
	if newErr {
		s.d.Log.Error("a declared file could not be delivered", "item", item.Title, "machine", f.Machine, "path", f.Path, "err", msg)
		_ = s.alarm("a secret's file could not be delivered",
			item.Title+" to "+f.Machine+":"+f.Path+": "+msg)
	}
}

func (s *Service) putFileState(item string, f ItemFile, st fileState) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketMeta, fileStateKey(item, f.Machine, f.Path), b)
	})
}

// deliverDueFiles is the sweep's half: files waiting for a maintenance window go out when one is
// on, and files that failed are tried again every sweep until they stand.
func (s *Service) deliverDueFiles() int {
	if s.keys.sealed() {
		return 0
	}
	type due struct {
		item Item
		col  Collection
		f    ItemFile
		st   fileState
	}
	var out []due
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketMeta, kv.Range{Prefix: []byte("filedel\x00")}, func(k, v []byte) bool {
			parts := strings.SplitN(strings.TrimPrefix(string(k), "filedel\x00"), "\x00", 3)
			if len(parts) != 3 {
				return true
			}
			var st fileState
			if json.Unmarshal(v, &st) != nil {
				return true
			}
			item, col, ok := s.itemTx(tx, parts[0])
			if !ok {
				return true
			}
			for _, f := range item.Files {
				if f.Machine == parts[1] && f.Path == parts[2] {
					window := st.Pending > 0 && (s.d.Maintenance != nil && s.d.Maintenance())
					retry := st.LastErr != "" && s.now().After(st.FailedAt.Add(time.Minute))
					if window || retry {
						out = append(out, due{item: item, col: col, f: f, st: st})
					}
				}
			}
			return true
		})
		return nil
	})
	for _, d := range out {
		s.deliverOneFile(d.item, d.col, d.f, d.st)
	}
	return len(out)
}

// renderFile writes the content a declared file carries: one field's value exactly as it is, or the
// item as a .env file — every field a NAME=value line, a section's fields under SECTION__NAME.
func renderFile(f ItemFile, ver ItemVersion, d *Details) (string, error) {
	if f.Field != "" {
		v, ok := d.Values[fieldKey("", f.Field)]
		if !ok {
			v, ok = valueByName(d.Values, f.Field)
		}
		if !ok {
			return "", fmt.Errorf("the item has no field %q for the file %s", f.Field, f.Path)
		}
		return v, nil
	}
	names := make([]string, 0, len(d.Values))
	for k := range d.Values {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		sec, name, _ := strings.Cut(k, "\x00")
		if !envNameRe.MatchString(name) {
			continue // not a name an .env line could carry; the single-field form is for these
		}
		if sec != "" {
			if !envNameRe.MatchString(sec) {
				continue
			}
			name = strings.ToUpper(sec) + "__" + strings.ToUpper(name)
		}
		b.WriteString(name)
		b.WriteString("=")
		b.WriteString(d.Values[k])
		b.WriteString("\n")
	}
	return b.String(), nil
}

// MachineScrubber is the scrubber of the values a machine may hold: every field value of every item
// bound to the machine, or with a file declared on it. read_file runs with it, so the values that
// live on a machine never reach an agent's context through the file tools.
func (s *Service) MachineScrubber(machine string) *scrub.Scrubber {
	if s.keys.sealed() {
		return nil
	}
	scr := scrub.New(s.scrubEncodings()...)
	type one struct {
		ref string
		ver ItemVersion
		col Collection
	}
	var items []one
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil || !s.itemVerifies(r) {
				return true
			}
			bound := containsString(r.Machines, machine)
			for _, f := range r.Files {
				if f.Machine == machine {
					bound = true
				}
			}
			if !bound {
				return true
			}
			if ver, ok := getVersionTx(tx, r.ID, r.Version); ok {
				if col, ok := s.collectionByIDTx(tx, r.Collection); ok {
					items = append(items, one{ref: RefOfItem(col.Name, r.Title), ver: ver, col: col})
				}
			}
			return true
		})
		return nil
	})
	for _, it := range items {
		d, err := s.decryptDetailsOf(it.col, it.ver)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(d.Values))
		for k := range d.Values {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			scr.Add(it.ref+"/"+fieldLabel(k), d.Values[k])
		}
	}
	return scr
}

// fieldLabel is a value's key as a reference names it: section and field, joined with /.
func fieldLabel(key string) string {
	sec, name, found := strings.Cut(key, "\x00")
	if found {
		return sec + "/" + name
	}
	return name
}

// FilesOf lists the delivery state of an item's declared files, for the console and the CLI: what
// is on the machine, what waits, what failed.
func (s *Service) FilesOf(itemID string) map[string]fileState {
	out := map[string]fileState{}
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketMeta, kv.Range{Prefix: []byte("filedel\x00" + itemID + "\x00")}, func(k, v []byte) bool {
			var st fileState
			if json.Unmarshal(v, &st) == nil {
				parts := strings.SplitN(strings.TrimPrefix(string(k), "filedel\x00"+itemID+"\x00"), "\x00", 2)
				if len(parts) == 2 {
					out[parts[0]+":"+parts[1]] = st
				}
			}
			return true
		})
		return nil
	})
	return out
}
