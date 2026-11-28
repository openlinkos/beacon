package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Imports in the Prometheus text format are read on their way to VictoriaMetrics: decompressed, counted
// and checked line by line. What VictoriaMetrics gets is the plain text of the lines that passed.
//
// One import carries at most maxImportSamples samples in maxImportBytes of text (a probe's minute of
// results is a few thousand); a heartbeat's at most maxHeartbeatSamples in maxHeartbeatBytes. Beyond
// that the import is refused with 400 import.too_large: 400 so that a probe agent drops the batch, as it
// does with other refusals it cannot fix, rather than queue it for ever.
//
// Every sample line must be in the plain form (textline.go), name its metric only before the braces,
// have at most maxLabels labels and give each label once, but for the labels the gateway sets itself. A
// probe's or a host's line that is not, or that its token may not write (a heartbeat the catalogue does
// not give its machine, a name the rule engine keeps for itself), is dropped and counted, and the rest go
// on; a heartbeat token's import is refused whole.
const (
	maxImportBytes      = 64 << 20
	maxImportSamples    = 200_000
	maxHeartbeatBytes   = 64 << 10
	maxHeartbeatSamples = 100
	maxImportLine       = 64 << 10 // no sample line is longer; a longer one is not a sample
	// maxLabels is the most labels a sample has besides its name: more than any series needs, and few
	// enough that checking them costs little.
	maxLabels = 64

	// heartbeatMetric is the one metric a heartbeat token writes, and the one a probe or a host writes
	// only for the heartbeats of its machine.
	heartbeatMetric = "beacon_heartbeat_last_success_timestamp_seconds"
)

// Why a sample does not pass.
const (
	dropSyntax    = "syntax"          // not in the plain form, or longer than any sample
	dropNameLabel = "name_label"      // names its metric with a __name__ label
	dropLabels    = "labels"          // gives a label twice
	dropTooMany   = "too_many_labels" // has more than maxLabels labels
	dropHeartbeat = "heartbeat"       // a heartbeat the catalogue does not give the writer's machine
	dropReserved  = "reserved"        // a name the rule engine keeps for itself (reservedName)
	dropMetric    = "metric"          // a heartbeat token's metric other than its heartbeat's
)

// reservedName reports whether a metric's name is one the rule engine keeps for what it writes itself: a
// name with a colon (by convention a recording rule's, such as beacon:service_down), or vmalert's ALERTS
// and ALERTS_FOR_STATE, from which it restores its alerts' state on a restart. The rules read these as
// the rule engine wrote them, so no machine writes them.
func reservedName(name string) bool {
	return strings.Contains(name, ":") || name == "ALERTS" || name == "ALERTS_FOR_STATE"
}

// importError is why an import is refused, with its code; the proxy's error handler answers it as 400.
type importError struct{ code, msg string }

func (e *importError) Error() string { return e.msg }

// importLimits bound one import and say which lines pass.
type importLimits struct {
	bytes   int64
	samples int
	// forced are the labels the gateway sets on what passes, whatever the sample gives: a sample may
	// give one of them twice (duplicate).
	forced []string
	// check says why a well-formed sample may not pass, or "" when it may.
	check func(textSample) string
	// refuse, when set, refuses the whole import for a line that does not pass, with the error it makes
	// of the reason; otherwise the line is dropped and drop, when set, is told why.
	refuse func(reason string, s textSample, line []byte) *importError
	drop   func(reason string)
}

// importBody is the request's text, decompressed, as a reader that fails with an *importError once the
// limits are passed; or the error that the encoding cannot be read.
func importBody(r *http.Request, lim importLimits) (io.Reader, error) {
	var src io.Reader = r.Body
	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, &importError{"bad_input", "the body says it is gzipped, but it is not: " + err.Error()}
		}
		src = zr
	default:
		return nil, &importError{"bad_input", "send the text format as it is or gzipped, not with Content-Encoding " + strconv.Quote(enc)}
	}
	return &samples{src: bufio.NewReaderSize(src, maxImportLine), lim: lim}, nil
}

// samples passes text through line by line, counting what it reads and the samples in it.
type samples struct {
	src   *bufio.Reader
	lim   importLimits
	n     int64
	count int
	skip  bool   // within a line too long to be a sample, dropped
	buf   []byte // read, not yet passed on
	err   error
}

func (s *samples) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		s.fill()
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *samples) fill() {
	chunk, err := s.src.ReadSlice('\n')
	var tooLarge *http.MaxBytesError
	long := false
	switch {
	case err == nil:
	case errors.Is(err, bufio.ErrBufferFull):
		long = true
	case errors.Is(err, io.EOF):
		s.err = io.EOF
	case errors.As(err, &tooLarge):
		s.err = &importError{"import.too_large", "the body is larger than " + strconv.FormatInt(tooLarge.Limit>>20, 10) + " MiB; send less at once"}
	default:
		s.err = &importError{"bad_input", "the body could not be read: " + err.Error()}
	}
	if len(chunk) == 0 {
		return
	}
	if s.n += int64(len(chunk)); s.n > s.lim.bytes {
		s.err = &importError{"import.too_large", "an import carries at most " + strconv.FormatInt(s.lim.bytes>>10, 10) + " KiB of text; send less at once"}
		return
	}
	if s.skip { // the rest of a long line
		s.skip = long
		return
	}
	if long {
		s.skip = true
		if e := s.reject(dropSyntax, textSample{}, chunk); e != nil {
			s.err = e
		}
		return
	}
	out, e := s.line(chunk)
	if e != nil {
		s.err = e
		return
	}
	s.buf = out
}

// line is what passes of one line with its line break: a blank line or a comment as it is, a sample that
// passes up to its end, nothing of one that does not; or the error that refuses the import.
func (s *samples) line(chunk []byte) ([]byte, *importError) {
	body := bytes.TrimSuffix(chunk, []byte("\n"))
	body = bytes.TrimSuffix(body, []byte("\r"))
	brk := chunk[len(body):]
	t := bytes.TrimLeft(body, " \t")
	if len(t) == 0 || t[0] == '#' {
		return chunk, nil
	}
	if s.count++; s.count > s.lim.samples {
		return nil, &importError{"import.too_large", "an import carries at most " + strconv.Itoa(s.lim.samples) + " samples; send fewer at once"}
	}
	smp, ok := parseSample(t)
	reason := ""
	switch {
	case !ok:
		reason = dropSyntax
	case hasLabel(smp, "__name__"):
		reason = dropNameLabel
	case smp.tooManyLabels():
		reason = dropTooMany
	case smp.duplicate(s.lim.forced):
		reason = dropLabels
	case s.lim.check != nil:
		reason = s.lim.check(smp)
	}
	if reason != "" {
		return nil, s.reject(reason, smp, t)
	}
	if smp.end == len(t) {
		return chunk[len(chunk)-len(t)-len(brk):], nil
	}
	out := make([]byte, 0, smp.end+len(brk)) // the sample without the comment after it
	return append(append(out, t[:smp.end]...), brk...), nil
}

// reject drops a line that does not pass, or makes the error that refuses the import for it.
func (s *samples) reject(reason string, smp textSample, line []byte) *importError {
	if s.lim.refuse != nil {
		return s.lim.refuse(reason, smp, line)
	}
	if s.lim.drop != nil {
		s.lim.drop(reason)
	}
	return nil
}

func hasLabel(s textSample, name string) bool {
	_, ok := s.label(name)
	return ok
}

// heartbeatOnly is what a heartbeat token may not write: any metric but its heartbeat's.
func heartbeatOnly(s textSample) string {
	if s.name != heartbeatMetric {
		return dropMetric
	}
	return ""
}

// heartbeatRefusal is why a heartbeat token's import is refused for a line that does not pass.
func heartbeatRefusal(reason string, s textSample, line []byte) *importError {
	switch reason {
	case dropMetric:
		return &importError{"heartbeat.metric", "a heartbeat token writes only " + heartbeatMetric + "; this import has " +
			strconv.Quote(s.name[:min(len(s.name), 100)])}
	case dropNameLabel:
		return &importError{"heartbeat.metric", "a heartbeat token writes only " + heartbeatMetric + ", named before the braces and not with a __name__ label"}
	case dropLabels:
		return &importError{"bad_input", "a sample gives the same label twice"}
	case dropTooMany:
		return &importError{"bad_input", "a sample has more than " + strconv.Itoa(maxLabels) + " labels"}
	}
	return &importError{"bad_input", "a line is not a sample of the Prometheus text format, name{label=\"value\"} value [timestamp]: " +
		strconv.Quote(string(line[:min(len(line), 100)]))}
}

// readImport reads all of a small import (a heartbeat's) before anything is sent on, so that a refused
// one sends nothing.
func readImport(r *http.Request, lim importLimits) ([]byte, error) {
	body, err := importBody(r, lim)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(body) // the reader's errors are *importError
}
