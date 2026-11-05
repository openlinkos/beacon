package secrets

import (
	"context"
	"crypto/sha1" //nolint:gosec // the leak corpus's own interface is defined over SHA-1
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The password health report: weak passwords, repeated ones, their age, and — only when the
// deployment turns it on — the leak corpus. Everything here is computed locally, from values the
// vault opens for itself; what leaves the report is judgements and references, never a value, so
// the report needs no re-authentication and leaves no value in any log. The leak corpus (HIBP's
// k-anonymity interface) is off by default, for it sends the first five characters of each
// password's hash outward; the setting that turns it on says so, and the console repeats it.

// The finding kinds, as the console and the API show them.
const (
	KindWeak     = "weak"
	KindRepeated = "repeated"
	KindOld      = "old"
	KindLeaked   = "leaked"
)

// hibpBaseURL is the leak corpus's k-anonymity interface. Only the first five characters of a
// password's SHA-1 hash ever travel: the range asked for and nothing of the password itself.
const hibpBaseURL = "https://api.pwnedpasswords.com"

// commonPasswords is the local weak list: the worn-out ones every guesser tries first. It is
// public knowledge, kept short on purpose; the length and the looks-alike rules around it do the
// rest.
var commonPasswords = map[string]bool{
	"123456": true, "password": true, "123456789": true, "12345678": true, "qwerty": true,
	"12345": true, "1234": true, "111111": true, "1234567": true, "dragon": true,
	"123123": true, "abc123": true, "iloveyou": true, "monkey": true, "letmein": true,
	"welcome": true, "admin": true, "login": true, "princess": true, "qwertyuiop": true,
	"sunshine": true, "master": true, "football": true, "shadow": true, "superman": true,
	"trustno1": true, "password1": true, "p@ssw0rd": true, "passwd": true, "hunter2": true,
}

// passwordFieldName says whether a field's name reads like a password's, for items whose fields
// carry no purpose (an import that did not say, a hand-written item).
func passwordFieldName(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "password") || strings.Contains(n, "passphrase") ||
		strings.Contains(n, "passwd") || strings.Contains(n, "pwd")
}

// isPasswordField says whether a field is judged by the health report.
func isPasswordField(f Field) bool {
	if f.Type == FieldOTP || f.Type == FieldFile {
		return false
	}
	return f.Purpose == PurposePassword || passwordFieldName(f.Name)
}

// WeakPassword judges one value locally: too short for the setting, in the common list, or wearing
// the item's own title or user name. username may be empty.
func WeakPassword(value, username, title string, minLength int64) bool {
	if minLength <= 0 {
		minLength = 8
	}
	low := strings.ToLower(value)
	if int64(len([]rune(value))) < minLength || commonPasswords[low] {
		return true
	}
	if title != "" && low == strings.ToLower(title) {
		return true
	}
	return username != "" && low == strings.ToLower(username)
}

// HIBPRangeCount asks the leak corpus by the k-anonymity interface and answers how many times the
// value appears in it: the first five characters of its SHA-1 hash select the range, the rest stays
// home. base is the interface's root (the public one by default); a test stands its own server in.
func HIBPRangeCount(ctx context.Context, client *http.Client, base, value string) (int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	sum := sha1.Sum([]byte(value)) //nolint:gosec // the interface is defined over SHA-1, not by us
	hexed := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, suffix := hexed[:5], hexed[5:]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/range/"+prefix, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the leak corpus answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		hash, count, found := strings.Cut(strings.TrimSpace(line), ":")
		if found && hash == suffix {
			var n int
			if _, err := fmt.Sscanf(count, "%d", &n); err == nil {
				return n, nil
			}
		}
	}
	return 0, nil
}

// HealthFinding is one password the report has something to say about: where it sits (the
// reference, never the value), what is wrong with it, how old it is and — when it is repeated —
// how many places wear it.
type HealthFinding struct {
	Ref        string   `json:"ref"`
	Collection string   `json:"collection"`
	Item       string   `json:"item"`
	Title      string   `json:"title"`
	Section    string   `json:"section,omitempty"`
	Field      string   `json:"field"`
	Kinds      []string `json:"kinds"` // weak, repeated, old, leaked
	AgeDays    int      `json:"age_days"`
	Shares     int      `json:"shares,omitempty"` // how many fields wear this same value
}

// HealthReport is the console's report over the collections the reader may read.
type HealthReport struct {
	Findings   []HealthFinding `json:"findings"`
	Passwords  int             `json:"passwords"` // the password fields judged
	Items      int             `json:"items"`     // the items walked
	HIBP       bool            `json:"hibp"`      // whether the leak corpus was asked
	HIBPFailed int             `json:"hibp_failed,omitempty"` // values the corpus did not answer
}

// HealthReport walks the collections the actor may read, opens the current version of every live
// item and judges its password fields: weak (locally), repeated (same value in more than one
// place), old (past hygiene.password_age) and — when hygiene.hibp_check is on — leaked (the
// k-anonymity interface, one range per distinct value). The findings carry references and
// judgements only; no value, no hash, leaves the vault.
func (s *Service) HealthReport(who Actor) (HealthReport, error) {
	out := HealthReport{}
	if s.keys.sealed() {
		return out, ErrSealed
	}
	if !who.Person() {
		return out, ErrAgent
	}
	minLength := s.settingInt("hygiene.weak_min_length", 8)
	ageLimit := s.settingDuration("hygiene.password_age", 90*24*time.Hour)
	hibp := s.settingBool("hygiene.hibp_check", false)
	out.HIBP = hibp

	type spot struct {
		finding  HealthFinding
		value    string
		username string // the item's own user name, for the looks-alike rule
		at       time.Time
	}
	var spots []spot
	now := s.now()
	for _, col := range s.Collections() {
		if !s.May(who, col.ID, PermRead) {
			continue // values this reader may not see are not theirs to be told about either
		}
		for _, item := range s.Items(col.Name, StateNormal) {
			out.Items++
			ver, ok := s.Version(item.ID, item.Version)
			if !ok {
				continue
			}
			d, err := s.decryptDetailsOf(col, ver)
			if err != nil {
				continue // the item keeps its value; the report says nothing about what it cannot open
			}
			username := ""
			for _, f := range ver.Fields {
				if f.Purpose == PurposeUsername {
					if v, ok := d.Values[fieldKey(f.Section, f.Name)]; ok {
						username = v
					}
				}
			}
			for _, f := range ver.Fields {
				if !isPasswordField(f) {
					continue
				}
				value, ok := d.Values[fieldKey(f.Section, f.Name)]
				if !ok {
					continue
				}
				out.Passwords++
				spots = append(spots, spot{finding: HealthFinding{
					Ref: RefOfItem(col.Name, itemRefTitle(item)), Collection: col.Name, Item: item.ID,
					Title: item.Title, Section: f.Section, Field: f.Name,
					AgeDays: int(now.Sub(ver.At).Hours() / 24),
				}, value: value, username: username, at: ver.At})
			}
		}
	}

	// the leak corpus is asked once per distinct value, and only when the deployment turned it on
	leaked := map[string]bool{}
	if hibp {
		seen := map[string]bool{}
		for _, sp := range spots {
			sum := sha256.Sum256([]byte(sp.value))
			key := hex.EncodeToString(sum[:])
			if seen[key] {
				continue
			}
			seen[key] = true
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			n, err := HIBPRangeCount(ctx, s.d.HTTP, hibpBaseURL, sp.value)
			cancel()
			if err != nil {
				s.d.Log.Warn("the leak corpus did not answer", "err", err)
				out.HIBPFailed++
				continue
			}
			leaked[key] = n > 0
		}
	}

	// the repeated values: same value in more than one place, grouped by hash
	byHash := map[string][]int{}
	for i, sp := range spots {
		sum := sha256.Sum256([]byte(sp.value))
		byHash[hex.EncodeToString(sum[:])] = append(byHash[hex.EncodeToString(sum[:])], i)
	}

	for _, sp := range spots {
		kinds := []string{}
		if WeakPassword(sp.value, sp.username, sp.finding.Title, minLength) {
			kinds = append(kinds, KindWeak)
		}
		sum := sha256.Sum256([]byte(sp.value))
		key := hex.EncodeToString(sum[:])
		if len(byHash[key]) > 1 {
			kinds = append(kinds, KindRepeated)
			sp.finding.Shares = len(byHash[key])
		}
		if ageLimit > 0 && now.Sub(sp.at) >= ageLimit {
			kinds = append(kinds, KindOld)
		}
		if hibp && leaked[key] {
			kinds = append(kinds, KindLeaked)
		}
		if len(kinds) > 0 {
			sp.finding.Kinds = kinds
			out.Findings = append(out.Findings, sp.finding)
		}
	}
	sort.Slice(out.Findings, func(i, j int) bool {
		if out.Findings[i].Collection != out.Findings[j].Collection {
			return out.Findings[i].Collection < out.Findings[j].Collection
		}
		return out.Findings[i].Title < out.Findings[j].Title
	})
	if out.Findings == nil {
		out.Findings = []HealthFinding{}
	}
	return out, nil
}
