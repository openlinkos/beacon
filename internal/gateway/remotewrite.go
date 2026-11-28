package gateway

import (
	"encoding/binary"
	"net"
	"slices"
	"strconv"
	"strings"
)

// Remote write (Prometheus' protocol, version 1) is read on its way to VictoriaMetrics as imports are:
// the body is decompressed (snappy, the block format), each series is checked as a sample line is
// (imports.go), and VictoriaMetrics gets a request the gateway wrote itself: the series that passed, each
// with its labels written out once, without those the gateway sets itself, compressed again. A series the
// request names oddly (a label given twice, too many labels) or that its token may not write is dropped
// and counted; a body that cannot be read is refused. VictoriaMetrics' own protocol (zstd) is refused
// with 415, on which vmagent goes back to Prometheus', and so is remote write 2.0 (a Content-Type whose
// proto is not version 1's), which the sender is to be configured not to send.

// maxWriteBytes is the most one remote-write request may hold once decompressed: twice what vmagent
// sends at most by default (-remoteWrite.maxBlockSize), far more than Prometheus sends at once. A
// request is held about three times over while it is checked (compressed, decompressed and written
// again), so this bounds what one request costs the centre.
const maxWriteBytes = 16 << 20

// maxWriteBody is the most one remote-write request may hold compressed: the longest snappy block of
// maxWriteBytes, as data that does not compress becomes a little longer.
const maxWriteBody = 32 + maxWriteBytes + maxWriteBytes/6

// writeV1 is the proto parameter of a remote-write request's Content-Type that says it is version 1;
// a request without one is version 1 as well.
const writeV1 = "prometheus.WriteRequest"

// writeProtocol is the protocol a remote-write request's Content-Type names other than version 1 (as
// remote write 2.0's application/x-protobuf;proto=io.prometheus.write.v2.Request), or "" for version 1,
// which a Content-Type without a proto parameter (or none at all) is.
func writeProtocol(contentType string) string {
	_, params, _ := strings.Cut(contentType, ";")
	for p := range strings.SplitSeq(params, ";") {
		name, value, _ := strings.Cut(p, "=")
		if !strings.EqualFold(strings.TrimSpace(name), "proto") {
			continue
		}
		if value = strings.Trim(strings.TrimSpace(value), `"`); value != writeV1 {
			return value
		}
	}
	return ""
}

var errSnappy = &importError{"bad_input", "the body is not a remote-write request compressed with snappy"}

// snappyDecode decompresses a snappy block of at most limit bytes.
func snappyDecode(src []byte, limit int) ([]byte, error) {
	n, k := binary.Uvarint(src)
	if k <= 0 {
		return nil, errSnappy
	}
	if n > uint64(limit) {
		return nil, &importError{"import.too_large", "a remote-write request holds at most " + strconv.Itoa(limit>>20) + " MiB once decompressed; send less at once"}
	}
	dst := make([]byte, 0, n)
	s := src[k:]
	for len(s) > 0 {
		tag := s[0]
		var length, offset int
		switch tag & 3 {
		case 0: // a literal
			length = int(tag >> 2)
			s = s[1:]
			if length >= 60 {
				extra := length - 59
				if len(s) < extra {
					return nil, errSnappy
				}
				length = 0
				for i := range extra {
					length |= int(s[i]) << (8 * i)
				}
				s = s[extra:]
			}
			length++
			if length <= 0 || length > len(s) || len(dst)+length > int(n) {
				return nil, errSnappy
			}
			dst = append(dst, s[:length]...)
			s = s[length:]
			continue
		case 1:
			if len(s) < 2 {
				return nil, errSnappy
			}
			length = 4 + int(tag>>2)&7
			offset = int(tag&0xe0)<<3 | int(s[1])
			s = s[2:]
		case 2:
			if len(s) < 3 {
				return nil, errSnappy
			}
			length = 1 + int(tag>>2)
			offset = int(binary.LittleEndian.Uint16(s[1:3]))
			s = s[3:]
		case 3:
			if len(s) < 5 {
				return nil, errSnappy
			}
			length = 1 + int(tag>>2)
			offset = int(binary.LittleEndian.Uint32(s[1:5]))
			s = s[5:]
		}
		if offset <= 0 || offset > len(dst) || len(dst)+length > int(n) {
			return nil, errSnappy
		}
		for range length { // byte by byte: a copy may overlap what it writes
			dst = append(dst, dst[len(dst)-offset])
		}
	}
	if len(dst) != int(n) {
		return nil, errSnappy
	}
	return dst, nil
}

// snappyLiterals compresses src as snappy without compressing it: a block of literals, which every
// snappy decoder reads.
func snappyLiterals(src []byte) []byte {
	b := literalBlock(src)
	out := make([]byte, 0, blockLen(b))
	for _, p := range b {
		out = append(out, p...)
	}
	return out
}

// literalBlock is snappyLiterals(src) in pieces, src's among them as they are, so that it is read
// without being copied.
func literalBlock(src []byte) net.Buffers {
	b := net.Buffers{binary.AppendUvarint(nil, uint64(len(src)))}
	for len(src) > 0 {
		n := min(len(src), 65536)
		switch l := n - 1; {
		case l < 60:
			b = append(b, []byte{byte(l) << 2})
		case l < 1<<8:
			b = append(b, []byte{60 << 2, byte(l)})
		default:
			b = append(b, []byte{61 << 2, byte(l), byte(l >> 8)})
		}
		b = append(b, src[:n])
		src = src[n:]
	}
	return b
}

func blockLen(b net.Buffers) int {
	n := 0
	for _, p := range b {
		n += len(p)
	}
	return n
}

var errProto = &importError{"bad_input", "the body is not a remote-write request: Prometheus' WriteRequest, version 1"}

// protoField reads one field of a protobuf message: its number, its wire type, its bytes (the value of
// a length-delimited field; the whole encoding, key included, in raw) and what follows.
func protoField(b []byte) (num uint64, typ byte, val, raw, rest []byte, err error) {
	key, k := binary.Uvarint(b)
	if k <= 0 || key>>3 == 0 {
		return 0, 0, nil, nil, nil, errProto
	}
	num, typ = key>>3, byte(key&7)
	i := k
	switch typ {
	case 0:
		_, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return 0, 0, nil, nil, nil, errProto
		}
		i += n
	case 1:
		i += 8
	case 5:
		i += 4
	case 2:
		l, n := binary.Uvarint(b[i:])
		if n <= 0 || l > uint64(len(b)-i-n) {
			return 0, 0, nil, nil, nil, errProto
		}
		i += n
		val = b[i : i+int(l)]
		i += int(l)
	default: // groups are not in the protocol
		return 0, 0, nil, nil, nil, errProto
	}
	if i > len(b) {
		return 0, 0, nil, nil, nil, errProto
	}
	return num, typ, val, b[:i], b[i:], nil
}

// filterWrite is the remote-write request raw with only the series check lets pass (reason "" for
// those), their labels written out again; drop is told why each other one was left out. A series may
// give a label of forced twice (textSample.duplicate). A request's metadata pass as they are; fields
// outside the protocol do not.
func filterWrite(raw []byte, forced []string, check func(textSample) string, drop func(string)) ([]byte, error) {
	out := make([]byte, 0, len(raw))
	for b := raw; len(b) > 0; {
		num, typ, val, whole, rest, err := protoField(b)
		if err != nil {
			return nil, err
		}
		b = rest
		switch {
		case num == 1 && typ == 2:
			ts, reason, err := series(val, forced, check)
			if err != nil {
				return nil, err
			}
			if reason != "" {
				drop(reason)
				continue
			}
			out = appendField(out, 1, ts)
		case num == 3 && typ == 2:
			out = append(out, whole...)
		}
	}
	return out, nil
}

// series checks one TimeSeries and writes it again: its labels once each, name then value, but for
// those of forced, which the gateway sets itself, and its samples, exemplars and histograms as they were.
// One with more than maxLabels labels besides its name is dropped as soon as that shows, the rest of it
// unread.
func series(b []byte, forced []string, check func(textSample) string) ([]byte, string, error) {
	var s textSample
	var rest []byte
	for len(b) > 0 {
		num, typ, val, whole, next, err := protoField(b)
		if err != nil {
			return nil, "", err
		}
		b = next
		switch {
		case num == 1 && typ == 2:
			l, err := protoLabel(val)
			if err != nil {
				return nil, "", err
			}
			if s.labels = append(s.labels, l); len(s.labels) > maxLabels+1 { // more, or the name twice
				return nil, dropTooMany, nil
			}
		case num >= 2 && num <= 4 && typ == 2:
			rest = append(rest, whole...)
		}
	}
	switch {
	case s.tooManyLabels():
		return nil, dropTooMany, nil
	case s.duplicate(forced):
		return nil, dropLabels, nil
	}
	s.name, _ = s.label("__name__")
	if reason := check(s); reason != "" {
		return nil, reason, nil
	}
	out := make([]byte, 0, len(rest)+64)
	for _, l := range s.labels {
		if slices.Contains(forced, l[0]) { // the gateway sets it
			continue
		}
		lb := appendField(appendField(nil, 1, []byte(l[0])), 2, []byte(l[1]))
		out = appendField(out, 1, lb)
	}
	return append(out, rest...), "", nil
}

// protoLabel reads a Label: as protobuf does, a field given twice counts as its last.
func protoLabel(b []byte) ([2]string, error) {
	var l [2]string
	for len(b) > 0 {
		num, typ, val, _, rest, err := protoField(b)
		if err != nil {
			return l, err
		}
		b = rest
		if typ == 2 && (num == 1 || num == 2) {
			l[num-1] = string(val)
		}
	}
	return l, nil
}

func appendField(b []byte, num uint64, val []byte) []byte {
	b = binary.AppendUvarint(b, num<<3|2)
	b = binary.AppendUvarint(b, uint64(len(val)))
	return append(b, val...)
}
