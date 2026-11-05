package secrets

import (
	"fmt"
	"regexp"
	"strings"
)

// The reference syntax, after 1Password's op://: beacon://<collection>/<item>/[<section>/]<field>[?attribute=otp].
// Names are addressed by name when they are up to letters, digits, - _ . and spaces; anything else
// is addressed by ID. The attribute names what to do with an otp field: the current code, not the
// seed.

// RefPrefix is what a reference starts with.
const RefPrefix = "beacon://"

// refNameRe is the shape a name must have to be addressed by name in a reference.
var refNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 \-_.]{0,127}$`)

// Ref is a parsed reference.
type Ref struct {
	Collection string
	Item       string
	Section    string
	Field      string
	Attribute  string // otp, or empty
}

// ParseRef reads a reference. The field may be empty (a reference to a whole item).
func ParseRef(ref string) (Ref, error) {
	out := Ref{}
	s, ok := strings.CutPrefix(ref, RefPrefix)
	if !ok {
		return out, fmt.Errorf("%w: a reference starts with %s", ErrBadInput, RefPrefix)
	}
	if q, found := strings.CutSuffix(s, "?attribute=otp"); found {
		out.Attribute, s = "otp", q
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 || len(parts) > 4 {
		return out, fmt.Errorf("%w: a reference is %scollection/item/[section/]field", ErrBadInput, RefPrefix)
	}
	for _, p := range parts {
		if p == "" {
			return out, fmt.Errorf("%w: a reference part is empty", ErrBadInput)
		}
	}
	out.Collection, out.Item = parts[0], parts[1]
	if len(parts) >= 3 {
		out.Field = parts[len(parts)-1]
	}
	if len(parts) == 4 {
		out.Section = parts[2]
	}
	return out, nil
}

// RefOf writes a reference out of its parts, leaving the section and field out when empty.
func RefOf(collection, item, section, field string) string {
	ref := RefPrefix + collection + "/" + item
	if field != "" {
		if section != "" {
			ref += "/" + section
		}
		ref += "/" + field
	}
	return ref
}

// RefNameOK reports whether a name may be used in a reference as itself (else the ID is used).
func RefNameOK(name string) bool { return refNameRe.MatchString(name) }

// RefOfItem is the item's own reference (no field).
func RefOfItem(collection, item string) string { return RefPrefix + collection + "/" + item }

// String writes the reference back out.
func (r Ref) String() string {
	ref := RefOf(r.Collection, r.Item, r.Section, r.Field)
	if r.Attribute != "" {
		ref += "?attribute=" + r.Attribute
	}
	return ref
}
