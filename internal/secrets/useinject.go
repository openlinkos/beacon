package secrets

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/scrub"
)

// The use action's paths: the value reaches a command, a process or an HTTP request without being
// given to the agent that asked for it. Every path resolves the reference through a live use grant,
// records the use in the same transaction the grant's counter moves in — the design's first
// invariant, no record no value — and only then decrypts. What comes back — a command's output, an
// HTTP answer — is scrubbed of the values and their base64, URL and hexadecimal forms, as the
// scrub.encodings setting says.
//
// The scrubbing, and the binding of items to machines and hosts, stop the accidental leak: a
// command printing its environment, an API echoing the credential it was called with. They do not
// stop an agent that means to leak a value — the command is the agent's own, and it can wrap the
// value in an encoding the scrubber was not given or send it out another way. That is why these
// paths are bound to machines and hosts that already hold the value.

// Errors the API and MCP turn into answers.
var (
	// ErrNotBound: the use's destination is not one the item is bound to.
	ErrNotBound = errors.New("the item is not bound to this machine or host")
	// ErrPrivateAddress: http_request refuses a private address.
	ErrPrivateAddress = errors.New("http_request refuses private addresses (http.allow_private)")
	// ErrRedirect: the redirect goes where the policy does not follow.
	ErrRedirect = errors.New("the redirect leaves where http.follow_redirects allows")
)

// UseAsk is one value a use asks for: the name it will go under — an environment variable's name,
// most often — and the beacon:// reference it comes from, with its own reason when the caller has
// one per value.
type UseAsk struct {
	Name   string `json:"name"`
	Ref    string `json:"ref"`
	Reason string `json:"reason,omitempty"`
}

// NoGrantsError names the references no live grant covers, so a caller may ask for them and try
// again (beacon run does this when --wait says to).
type NoGrantsError struct{ Refs []string }

func (e *NoGrantsError) Error() string {
	return "no grant covers: " + strings.Join(e.Refs, ", ")
}

// resolvedUse is one reference on its way through: where it came from and, once the records are
// committed, the value itself — which never leaves this package except through the paths below.
type resolvedUse struct {
	ask     UseAsk
	ref     string // the reference as parsed
	item    Item
	col     Collection
	grantID string
	value   string
}

// resolveUses walks the asks: every reference must name a field (or an attachment) of an item the
// agent holds a use grant for, and bind — when it is set — must allow the destination. Every
// grant's counter and every use record move in one transaction: either each value is recorded and
// then given, or the call fails and nothing is consumed. The scrubber built alongside hides the
// values in anything that comes back.
func (s *Service) resolveUses(agentName, via string, asks []UseAsk, reason, where, ip, client string,
	bind func(item Item) error) (map[string]string, []*resolvedUse, *scrub.Scrubber, error) {
	out := map[string]string{}
	if s.keys.sealed() {
		s.anomalyFired("sealed_state", "a use came while the vault was sealed")
		return out, nil, nil, ErrSealed
	}
	if s.agentSuspended(agentName) {
		return out, nil, nil, ErrSuspendedAgent
	}
	agent, ok := s.agentInfoOf(agentName)
	if !ok {
		return out, nil, nil, fmt.Errorf("%w: no agent %q", ErrNotFound, agentName)
	}
	if len(asks) == 0 {
		return out, nil, nil, fmt.Errorf("%w: no references to resolve", ErrBadInput)
	}
	mode, minLength := s.useReasonMode()
	if mode == "required" && int64(len(strings.TrimSpace(reason))) < minLength {
		return out, nil, nil, fmt.Errorf("%w: the reason is at least %d characters", ErrBadInput, minLength)
	}
	if strings.TrimSpace(reason) == "" {
		reason = "context: " + where
	}
	names := map[string]bool{}
	var toDo []*resolvedUse
	var missing []string
	// pass one: everything must stand before anything is consumed
	for _, ask := range asks {
		if ask.Name == "" || ask.Ref == "" {
			return out, nil, nil, fmt.Errorf("%w: a use names what it is and where it comes from", ErrBadInput)
		}
		if names[ask.Name] {
			return out, nil, nil, fmt.Errorf("%w: the name %q is used twice", ErrBadInput, ask.Name)
		}
		names[ask.Name] = true
		parsed, err := ParseRef(ask.Ref)
		if err != nil {
			return out, nil, nil, err
		}
		if parsed.Field == "" {
			return out, nil, nil, fmt.Errorf("%w: a use reference names a field, like %s", ErrBadInput,
				RefOf(parsed.Collection, parsed.Item, "", "password"))
		}
		item, col, ok := s.Item(RefOfItem(parsed.Collection, parsed.Item))
		if !ok {
			missing = append(missing, ask.Ref)
			continue
		}
		// never is never given to an agent, whatever covers it (the fourth invariant)
		if s.EffectiveLevel(item) == LevelNever {
			return out, nil, nil, fmt.Errorf("%w: this item is not given to agents", ErrForbidden)
		}
		if bind != nil {
			if err := bind(item); err != nil {
				return out, nil, nil, err
			}
		}
		grant, ok := s.activeGrant(agentName, item.ID, parsed.Field, ActUse)
		if !ok {
			missing = append(missing, ask.Ref)
			continue
		}
		if !s.ownerMayRead(agent.Owner, col.ID) {
			return out, nil, nil, fmt.Errorf("%w: the agent's owner may no longer read this collection", ErrForbidden)
		}
		toDo = append(toDo, &resolvedUse{ask: ask, ref: parsed.String(), item: item, col: col, grantID: grant.ID})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return out, nil, nil, &NoGrantsError{Refs: missing}
	}
	// pass two: one transaction for every counter and every record — then, only then, the values
	err := s.d.Store.Storage().UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		sk, err := s.sk()
		if err != nil {
			return err
		}
		appendUse := s.d.AppendUse
		if appendUse == nil {
			appendUse = s.d.Store.UsesLog().AppendTx
		}
		for _, u := range toDo {
			b := tx.Get(bucketGrants, []byte(u.grantID))
			var cur Grant
			if b == nil || json.Unmarshal(b, &cur) != nil || !s.grantVerifies(cur) || cur.Status != GrantActive {
				return &NoGrantsError{Refs: []string{u.ref}}
			}
			if cur.Uses > 0 && cur.Used >= cur.Uses {
				return &NoGrantsError{Refs: []string{u.ref}}
			}
			if !cur.ExpiresAt.IsZero() && s.now().After(cur.ExpiresAt) {
				return &NoGrantsError{Refs: []string{u.ref}}
			}
			cur.Used++
			if cur.Uses > 0 && cur.Used >= cur.Uses {
				cur.Status = GrantExhausted
			}
			if err := putGrantTx(tx, cur, sk); err != nil {
				return err
			}
			why := reason
			if strings.TrimSpace(u.ask.Reason) != "" {
				why = u.ask.Reason
			}
			use := events.Event{Actor: agentName, OnBehalf: agent.Owner, Via: via, Action: "secret.use",
				Object: u.item.Title, Detail: "used " + u.item.Title + " in " + u.col.Name + " for " + where,
				Reason: why, Context: "collection=" + u.col.Name, IP: ip, Client: client}
			if err := appendUse(raw, &use); err != nil {
				// the record could not be written: the value is not given (the first invariant)
				return fmt.Errorf("the use could not be recorded, so the value is not given: %w", err)
			}
		}
		// Used and Status sit in the grant's signed parts: the head must name this row or the
		// grant vanishes the moment grants() walks it (kind=grant, missing, stale or bad).
		return writePolicyHeadTx(tx, sk)
	})
	if err != nil {
		return out, nil, nil, err
	}
	// committed: the values may be given now, and only now
	scr := scrub.New(s.scrubEncodings()...)
	for _, u := range toDo {
		ver, ok := s.Version(u.item.ID, u.item.Version)
		if !ok {
			return out, nil, nil, fmt.Errorf("%w: the item's current version is missing", ErrNotFound)
		}
		d, err := s.decryptDetailsOf(u.col, ver)
		if err != nil {
			return out, nil, nil, err
		}
		parsed, _ := ParseRef(u.ref)
		value, okV := d.Values[fieldKey(parsed.Section, parsed.Field)]
		if !okV {
			// a bare name may sit in a section; the attachment of that name may also be what is meant
			value, okV = valueByName(d.Values, parsed.Field)
			if !okV {
				data, err := s.attachmentPlain(u.item.ID, u.col, u.item.Version, parsed.Field)
				if err != nil || data == nil {
					return out, nil, nil, fmt.Errorf("%w: the item has no field or attachment %q", ErrNotFound, parsed.Field)
				}
				value = string(data)
			}
		}
		for _, f := range ver.Fields {
			if f.Name == parsed.Field && f.Section == parsed.Section && f.Type == FieldOTP {
				code, _, err := TOTPNow(value, s.now())
				if err != nil {
					return out, nil, nil, fmt.Errorf("%w: the field %s has no valid one-time-passcode seed", ErrInvalid, f.Name)
				}
				value = code // a use hands over the code that works now, never the seed
			}
		}
		out[u.ask.Name] = value
		u.value = value
		scr.Add(u.ref, value)
		s.countUse(u.col.Name, agentName, ActUse)
	}
	return out, toDo, scr, nil
}

// valueByName finds a value by field name whatever section it sits in.
func valueByName(values map[string]string, name string) (string, bool) {
	if v, ok := values[name]; ok {
		return v, ok
	}
	for k, v := range values {
		if _, nm, found := strings.Cut(k, "\x00"); found && nm == name {
			return v, true
		}
	}
	return "", false
}

// scrubEncodings reads scrub.encodings; the default hides the value itself and its base64, URL and
// hexadecimal forms.
func (s *Service) scrubEncodings() []string {
	var encs []string
	if s.d.Settings != nil {
		if r, err := s.d.Settings.System("scrub.encodings"); err == nil {
			if json.Unmarshal(r.Value, &encs) == nil {
				return encs
			}
		}
	}
	return append([]string(nil), scrub.AllEncodings...)
}

// UseForMachine resolves the values run_command's secrets ask for, on the machine the command runs
// on: every item must be bound to that machine, and the answer carries the scrubber that hides the
// values in the command's output, recording and transcript.
func (s *Service) UseForMachine(agentName, machine string, asks []UseAsk, reason, ip, client string) (map[string]string, *scrub.Scrubber, error) {
	if machine == "" {
		return nil, nil, fmt.Errorf("%w: the machine is empty", ErrBadInput)
	}
	values, _, scr, err := s.resolveUses(agentName, "api", asks, reason, "a command on "+machine, ip, client,
		func(item Item) error {
			if !containsString(item.Machines, machine) {
				return fmt.Errorf("%w: %s is not bound to %s (the item's bound machines: %s)", ErrNotBound,
					item.Title, machine, joinOrNone(item.Machines))
			}
			return nil
		})
	if err != nil {
		return nil, nil, err
	}
	return values, scr, nil
}

// joinOrNone joins a list for an error's text; an empty list says so.
func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

// UseForProcess resolves the values a local process asks for, as `beacon run` sends them: the .env
// and the environment's beacon:// references, filled for the child process alone. No binding: the
// process runs where the agent's own harness runs, and the token it holds is the agent.
func (s *Service) UseForProcess(agentName, via string, refs map[string]string, reason, ip, client string) (map[string]string, error) {
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	var asks []UseAsk
	for _, name := range names {
		asks = append(asks, UseAsk{Name: name, Ref: refs[name]})
	}
	values, _, _, err := s.resolveUses(agentName, via, asks, reason, "a local process", ip, client, nil)
	return values, err
}

// ---- http_request: the request is sent for the agent, the answer comes back scrubbed

// HTTPRequestIn is a request with {{secret.<reference>}} in its URL, headers or body; Beacon puts
// the values in and sends it to a host the item is bound to.
type HTTPRequestIn struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Reason  string            `json:"reason"`
}

// HTTPResponse is the scrubbed answer. Redirects says how many were followed; a redirect the policy
// refuses is the answer itself, with its Location header in Headers.
type HTTPResponse struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body"`
	Bytes     int               `json:"bytes"`
	Truncated bool              `json:"truncated,omitempty"`
	Redirects int               `json:"redirects"`
	Ms        int64             `json:"duration_ms"`
}

// maxHTTPBody caps what an answer may bring back; the rest of it is cut and said so.
const maxHTTPBody = 1 << 20

// secretPlaceholder finds {{secret.beacon://…}} in a request's parts.
var secretPlaceholder = regexp.MustCompile(`\{\{secret\.(beacon://[^}]+)\}\}`)

// HTTPRequest sends one request for the agent: the placeholders are resolved through use grants —
// every item bound to the URL's host, and under the path prefix when the binding has one — the
// values go into the request, and the answer (status, headers, body) comes back scrubbed of them.
// Only https; private addresses are refused unless http.allow_private says otherwise; redirects are
// followed only as far as http.follow_redirects allows, and the audit trail keeps the URL with its
// placeholders, never with a value in it.
func (s *Service) HTTPRequest(agentName, via string, in HTTPRequestIn, ip, client string) (HTTPResponse, error) {
	// the placeholders: each distinct reference is one use
	refs := map[string]bool{}
	scan := func(text string) {
		for _, m := range secretPlaceholder.FindAllStringSubmatch(text, -1) {
			refs[m[1]] = true
		}
	}
	scan(in.URL)
	scan(in.Body)
	for _, v := range in.Headers {
		scan(v)
	}
	if len(refs) == 0 {
		return HTTPResponse{}, fmt.Errorf("%w: the request carries no {{secret.beacon://…}} reference", ErrBadInput)
	}
	target, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || target.Scheme == "" || target.Host == "" {
		return HTTPResponse{}, fmt.Errorf("%w: the URL does not parse", ErrBadInput)
	}
	if target.Scheme != "https" {
		return HTTPResponse{}, fmt.Errorf("%w: http_request sends https only", ErrBadInput)
	}
	allowPrivate := s.settingBool("http.allow_private", false)
	if !allowPrivate {
		if ip := net.ParseIP(target.Hostname()); ip != nil && isPrivateIP(ip) {
			return HTTPResponse{}, fmt.Errorf("%w: %s is a private address", ErrPrivateAddress, target.Hostname())
		}
	}
	names := make([]string, 0, len(refs))
	for r := range refs {
		names = append(names, r)
	}
	sort.Strings(names)
	asks := make([]UseAsk, len(names))
	for i, r := range names {
		asks[i] = UseAsk{Name: "s" + strconv.Itoa(i), Ref: r}
	}
	values, _, scr, err := s.resolveUses(agentName, via, asks, in.Reason, "an HTTPS request to "+target.Host, ip, client,
		func(item Item) error {
			if hostBinds(item.Hosts, target) {
				return nil
			}
			return fmt.Errorf("%w: %s is not bound to %s (the item's bound hosts: %s)", ErrNotBound, item.Title,
				target.Host, joinOrNone(item.Hosts))
		})
	if err != nil {
		return HTTPResponse{}, err
	}
	valueOf := func(ref string) string {
		for i, r := range names {
			if r == ref {
				return values["s"+strconv.Itoa(i)]
			}
		}
		return ""
	}
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if in.Body != "" {
		body = strings.NewReader(fillSecrets(in.Body, valueOf))
	}
	req, err := http.NewRequestWithContext(s.ctx, method, fillSecrets(in.URL, valueOf), body)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("%w: the request does not build: %v", ErrBadInput, err) //nolint:errorlint // one wrapped error is enough
	}
	for k, v := range in.Headers {
		req.Header.Set(fillSecrets(k, valueOf), fillSecrets(v, valueOf))
	}
	// the policy: redirects and private addresses (literal private hosts were refused above)
	follow := s.settingString("http.follow_redirects", "same_host")
	redirects := 0
	hc := s.useHTTPClient(follow, allowPrivate, func() { redirects++ })
	started := s.now()
	resp, err := hc.Do(req)
	if err != nil {
		return HTTPResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	out := HTTPResponse{Status: resp.StatusCode, Headers: map[string]string{}, Ms: s.now().Sub(started).Milliseconds()}
	for k, v := range resp.Header {
		out.Headers[k] = scr.Scrub(strings.Join(v, ", "))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	if err != nil {
		return HTTPResponse{}, err
	}
	if len(b) > maxHTTPBody {
		b, out.Truncated = b[:maxHTTPBody], true
	}
	out.Body = scr.Scrub(string(b))
	out.Bytes = len(out.Body)
	out.Redirects = redirects
	return out, nil
}

// fillSecrets puts the values in where the placeholders are.
func fillSecrets(text string, valueOf func(ref string) string) string {
	if !strings.Contains(text, "{{secret.") {
		return text
	}
	return secretPlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		return valueOf(secretPlaceholder.FindStringSubmatch(m)[1])
	})
}

// hostBinding is one entry of an item's bound hosts, parsed.
type hostBinding struct {
	host   string
	port   string
	prefix string
}

// parseHostBinding reads one binding: HOST, HOST:PORT, HOST/PREFIX or HOST:PORT/PREFIX, the host
// name as the URL it belongs to would carry it (an IPv6 literal in brackets, say).
func parseHostBinding(h string) (hostBinding, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return hostBinding{}, false
	}
	if !strings.Contains(h, "://") {
		h = "https://" + h
	}
	u, err := url.Parse(h)
	if err != nil || u.Hostname() == "" {
		return hostBinding{}, false
	}
	return hostBinding{host: strings.ToLower(u.Hostname()), port: u.Port(), prefix: strings.Trim(u.Path, "/")}, true
}

// hostBinds says whether one of an item's bound hosts takes the URL: the host matches (a port in
// the binding must match too; a binding without one takes any) and the path is under the binding's
// prefix when it has one.
func hostBinds(hosts []string, target *url.URL) bool {
	name := strings.ToLower(target.Hostname())
	port := target.Port()
	path := target.Path
	if path == "" {
		path = "/"
	}
	for _, h := range hosts {
		b, ok := parseHostBinding(h)
		if !ok || b.host != name {
			continue
		}
		if b.port != "" && b.port != port {
			continue
		}
		if b.prefix == "" || strings.HasPrefix(path, "/"+b.prefix) {
			return true
		}
	}
	return false
}

// useHTTPClient builds the client a request goes with: a dialer that refuses private addresses
// unless allowed, and a redirect policy that stops where it may not go. The transport's TLS
// settings come from the service's own client when one was given (a test's, say).
func (s *Service) useHTTPClient(follow string, allowPrivate bool, onFollow func()) *http.Client {
	base := http.DefaultTransport
	if s.d.HTTP != nil && s.d.HTTP.Transport != nil {
		base = s.d.HTTP.Transport
	}
	tr, ok := base.(*http.Transport)
	if ok {
		tr = tr.Clone()
	} else {
		tr = &http.Transport{}
	}
	if allowPrivate {
		tr.DialContext = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	} else {
		tr.DialContext = privateRefusingDial
	}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		switch follow {
		case "any_bound":
			if s.anyHostBound(req.URL) {
				onFollow()
				return nil
			}
			return ErrRedirect
		case "none":
			return http.ErrUseLastResponse
		default: // same_host: the redirect may not leave the host the request started on
			if strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
				onFollow()
				return nil
			}
			return http.ErrUseLastResponse
		}
	}}
}

// anyHostBound says whether the URL's host is bound to any item at all. It is the any_bound
// redirect policy's check, and it is the union on purpose: the destinations are the operator's
// list, not the caller's.
func (s *Service) anyHostBound(target *url.URL) bool {
	found := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) == nil && s.itemVerifies(r) && hostBinds(r.Hosts, target) {
				found = true
				return false
			}
			return true
		})
		return nil
	})
	return found
}

// privateRefusingDial connects only to addresses that are neither private, loopback, link-local nor
// unspecified — the http.allow_private setting's default. The name is resolved, every address it
// gives is checked, and the connection goes to the address that was checked.
func privateRefusingDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("%w: %s is a private address", ErrPrivateAddress, host)
		}
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, addr)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		if isPrivateIP(ip.IP) {
			continue
		}
		c, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("%w: %s resolves to private addresses only", ErrPrivateAddress, host)
	}
	return nil, last
}

// isPrivateIP says whether an address is one http_request keeps to itself.
func isPrivateIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}
