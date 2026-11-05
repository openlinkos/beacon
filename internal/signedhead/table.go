package signedhead

import (
	"fmt"

	"github.com/openlinkos/beacon/internal/kv"
)

// Status is how Load judged the stored head against the epochs file.
type Status int

const (
	// StatusEmpty is a first start: no head and the epochs file does not expect one.
	StatusEmpty Status = iota
	// StatusOK is a verified head whose epoch matches the file (or the file has caught up).
	StatusOK
	// StatusCatchUp is a verified head newer than the file (crash after the DB commit); Load
	// rewrites the file and Accept treats it as OK.
	StatusCatchUp
	// StatusFailsafe is a missing, bad, or stale head: the whole table is unusable until Rebuild.
	StatusFailsafe
)

// LoadResult is Load's answer.
type LoadResult struct {
	Status Status
	Head   Head
}

// Table is one signed table: the head row in Bucket at HeadKey, signed with SK, anchored in Dir.
type Table struct {
	Name    string
	Bucket  []byte
	HeadKey []byte
	SK      func() []byte
	Anchor  FileAnchor
	Alert   func(string)
}

func (t *Table) alert(msg string) {
	if t.Alert != nil {
		t.Alert(msg)
	}
}

func (t *Table) sk() []byte {
	if t.SK == nil {
		return nil
	}
	return t.SK()
}

// Load reads the head, verifies it, and compares its epoch to the epochs file.
func (t *Table) Load(tx kv.Tx) LoadResult {
	sk := t.sk()
	fileEpoch, fileErr := t.fileEpoch()
	if fileErr != nil {
		t.alert("signed head " + t.Name + ": the epochs file is unreadable")
		return LoadResult{Status: StatusFailsafe}
	}
	raw := tx.Get(t.Bucket, t.HeadKey)
	if len(raw) == 0 {
		if fileEpoch > 0 {
			t.alert("signed head " + t.Name + ": missing from the database")
			return LoadResult{Status: StatusFailsafe}
		}
		return LoadResult{Status: StatusEmpty, Head: Head{Name: t.Name, Entries: map[string]string{}}}
	}
	h, err := decodeHead(raw)
	if err != nil || h.Name != t.Name || len(sk) == 0 || !Verify(sk, h) {
		t.alert("signed head " + t.Name + ": signature failed")
		return LoadResult{Status: StatusFailsafe}
	}
	if h.Entries == nil {
		h.Entries = map[string]string{}
	}
	if h.Epoch < fileEpoch {
		t.alert("signed head " + t.Name + ": epoch is older than the epochs file")
		return LoadResult{Status: StatusFailsafe}
	}
	if h.Epoch > fileEpoch {
		if err := t.Anchor.Record(t.Name, h.Epoch); err != nil {
			t.alert("signed head " + t.Name + ": cannot catch up the epochs file")
		}
		return LoadResult{Status: StatusCatchUp, Head: h}
	}
	return LoadResult{Status: StatusOK, Head: h}
}

func (t *Table) fileEpoch() (uint64, error) {
	m, err := t.Anchor.readAll()
	if err != nil {
		return 0, err
	}
	if m == nil {
		return 0, nil
	}
	return m[t.Name], nil
}

// Commit updates the head for the keys this transaction wrote or deleted, signs it, and writes
// it in the same transaction. It does not scan the bucket. A failsafe head is an error.
func (t *Table) Commit(tx kv.Tx, put map[string][]byte, del []string) (Head, error) {
	return t.commit(tx, t.Load(tx), put, del)
}

// commit applies put/del to a LoadResult already taken (SetKey verifies with the previous key).
func (t *Table) commit(tx kv.Tx, res LoadResult, put map[string][]byte, del []string) (Head, error) {
	sk := t.sk()
	if len(sk) == 0 {
		return Head{}, ErrNoKey
	}
	if res.Status == StatusFailsafe {
		return Head{}, ErrFailsafe
	}
	h := res.Head
	if res.Status == StatusEmpty {
		h = Head{Name: t.Name, Epoch: 0, Entries: map[string]string{}}
	}
	ents := cloneEntries(h.Entries)
	if ents == nil {
		ents = map[string]string{}
	}
	for k, row := range put {
		ents[k] = HashRow(row)
	}
	for _, k := range del {
		delete(ents, k)
	}
	out := Head{Name: t.Name, Epoch: h.Epoch + 1, Entries: ents}
	out.Sig = Sign(sk, out)
	raw, err := encodeHead(out)
	if err != nil {
		return Head{}, err
	}
	if err := tx.Put(t.Bucket, t.HeadKey, raw); err != nil {
		return Head{}, err
	}
	return out, nil
}

// CommitFrom is Commit using a head already loaded and verified (a key rotation that must
// verify under the previous key and sign under the new one).
func (t *Table) CommitFrom(tx kv.Tx, res LoadResult, put map[string][]byte, del []string) (Head, error) {
	return t.commit(tx, res, put, del)
}

// AfterCommit writes the epochs file. Call it after the database transaction has committed.
func (t *Table) AfterCommit(h Head) error {
	if h.Name == "" {
		h.Name = t.Name
	}
	return t.Anchor.Record(h.Name, h.Epoch)
}

// Accept reports whether row (the exact stored bytes, or nil if missing) matches the head.
func Accept(result LoadResult, key string, row []byte) bool {
	switch result.Status {
	case StatusFailsafe, StatusEmpty:
		return false
	}
	want, named := result.Head.Entries[key]
	if !named {
		return false
	}
	if len(row) == 0 || HashRow(row) != want {
		return false
	}
	return true
}

// Rebuild writes a new signed head from rows (the only scan-based write) and advances the epoch
// past both the stored head and the epochs file, so a recovery is not itself stale.
func (t *Table) Rebuild(tx kv.Tx, rows map[string][]byte) (Head, error) {
	sk := t.sk()
	if len(sk) == 0 {
		return Head{}, ErrNoKey
	}
	fileEpoch, err := t.fileEpoch()
	if err != nil {
		return Head{}, fmt.Errorf("%w: the epochs file is unreadable", ErrFailsafe)
	}
	var dbEpoch uint64
	if raw := tx.Get(t.Bucket, t.HeadKey); len(raw) > 0 {
		if h, err := decodeHead(raw); err == nil {
			dbEpoch = h.Epoch
		}
	}
	epoch := fileEpoch
	if dbEpoch > epoch {
		epoch = dbEpoch
	}
	ents := make(map[string]string, len(rows))
	for k, row := range rows {
		ents[k] = HashRow(row)
	}
	out := Head{Name: t.Name, Epoch: epoch + 1, Entries: ents}
	out.Sig = Sign(sk, out)
	raw, err := encodeHead(out)
	if err != nil {
		return Head{}, err
	}
	if err := tx.Put(t.Bucket, t.HeadKey, raw); err != nil {
		return Head{}, err
	}
	return out, nil
}
