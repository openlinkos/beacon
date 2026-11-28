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
// and, for a heartbeat token, held to its one metric. What VictoriaMetrics gets is the plain text.
//
// One import carries at most maxImportSamples samples in maxImportBytes of text (a probe's minute of
// results is a few thousand); a heartbeat's at most maxHeartbeatSamples in maxHeartbeatBytes. Beyond
// that the import is refused with 400 import.too_large: 400 so that a probe agent drops the batch, as it
// does with other refusals it cannot fix, rather than queue it for ever.
const (
	maxImportBytes      = 64 << 20
	maxImportSamples    = 200_000
	maxHeartbeatBytes   = 64 << 10
	maxHeartbeatSamples = 100
	maxImportLine       = 64 << 10 // the buffer lines are read through; longer lines pass in parts

	// heartbeatMetric is the one metric a heartbeat token writes.
	heartbeatMetric = "beacon_heartbeat_last_success_timestamp_seconds"
)

// importError is why an import is refused, with its code; the proxy's error handler answers it as 400.
type importError struct{ code, msg string }

func (e *importError) Error() string { return e.msg }

// importLimits bound one import: its text, its samples, and the one metric it may carry ("" for any).
type importLimits struct {
	bytes   int64
	samples int
	only    string
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
	return &samples{src: bufio.NewReaderSize(src, maxImportLine), lim: lim, start: true}, nil
}

// samples passes text through line by line, counting what it passes and the samples in it.
type samples struct {
	src   *bufio.Reader
	lim   importLimits
	n     int64
	count int
	start bool   // the next byte starts a line
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
	switch {
	case err == nil, errors.Is(err, bufio.ErrBufferFull):
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
	if s.start {
		if e := s.line(chunk); e != nil {
			s.err = e
			return
		}
	}
	s.start = chunk[len(chunk)-1] == '\n'
	s.buf = chunk
}

// line checks the start of a line: a comment, a blank line or a sample.
func (s *samples) line(b []byte) error {
	b = bytes.TrimLeft(b, " \t")
	if len(bytes.TrimSpace(b)) == 0 || b[0] == '#' {
		return nil
	}
	if s.count++; s.count > s.lim.samples {
		return &importError{"import.too_large", "an import carries at most " + strconv.Itoa(s.lim.samples) + " samples; send fewer at once"}
	}
	if s.lim.only != "" {
		name := b
		if i := bytes.IndexAny(b, "{ \t\r\n"); i >= 0 {
			name = b[:i]
		}
		if string(name) != s.lim.only {
			return &importError{"heartbeat.metric", "a heartbeat token writes only " + s.lim.only + "; this import has " +
				strconv.Quote(string(name[:min(len(name), 100)]))}
		}
	}
	return nil
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
