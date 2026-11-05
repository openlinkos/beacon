package signedhead

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/openlinkos/beacon/internal/kv"
)

func testSK() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 9)
	}
	return k
}

func TestSignVerify(t *testing.T) {
	sk := testSK()
	h := Head{Name: "settings", Epoch: 3, Entries: map[string]string{"a": HashRow([]byte("one")), "b": HashRow([]byte("two"))}}
	h.Sig = Sign(sk, h)
	if !Verify(sk, h) {
		t.Fatal("a freshly signed head should verify")
	}
	h.Epoch = 4
	if Verify(sk, h) {
		t.Fatal("tampering the epoch must fail")
	}
	h.Epoch = 3
	h.Entries["a"] = HashRow([]byte("other"))
	if Verify(sk, h) {
		t.Fatal("tampering an entry must fail")
	}
	h.Entries["a"] = HashRow([]byte("one"))
	h.Name = "other"
	if Verify(sk, h) {
		t.Fatal("tampering the name must fail")
	}
	other := bytes.Clone(sk)
	other[0] ^= 1
	h.Name = "settings"
	if Verify(other, h) {
		t.Fatal("the wrong key must fail")
	}
	if Verify(nil, h) {
		t.Fatal("an empty key must fail")
	}
}

func openDB(t *testing.T) kv.DB {
	t.Helper()
	db, err := kv.OpenSQLite(kv.SQLiteOptions{Path: filepath.Join(t.TempDir(), "h.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTable(t *testing.T, db kv.DB) *Table {
	t.Helper()
	sk := testSK()
	return &Table{
		Name:    "settings",
		Bucket:  []byte("settings"),
		HeadKey: []byte("\x00head"),
		SK:      func() []byte { return sk },
		Anchor:  FileAnchor{Dir: t.TempDir()},
	}
}

func TestCommitDoesNotScanBucket(t *testing.T) {
	// red-before: writeHead ranged the bucket and would pick up any row whose HMAC verified.
	db := openDB(t)
	tab := newTable(t, db)
	bucket := tab.Bucket
	if err := db.Update(func(tx kv.Tx) error {
		if err := tx.Put(bucket, []byte("extra"), []byte(`{"sneak":true}`)); err != nil {
			return err
		}
		h, err := tab.Commit(tx, map[string][]byte{"real": []byte("row-bytes")}, nil)
		if err != nil {
			return err
		}
		if _, ok := h.Entries["extra"]; ok {
			t.Fatal("Commit picked up an extra row sitting in the bucket")
		}
		if h.Entries["real"] != HashRow([]byte("row-bytes")) {
			t.Fatalf("real: %v", h.Entries)
		}
		if h.Epoch != 1 {
			t.Fatalf("epoch %d", h.Epoch)
		}
		return tab.AfterCommit(h)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx kv.Tx) error {
		if err := tx.Put(bucket, []byte("later"), []byte("forged")); err != nil {
			return err
		}
		h, err := tab.Commit(tx, map[string][]byte{"other": []byte("two")}, nil)
		if err != nil {
			return err
		}
		if _, ok := h.Entries["later"]; ok {
			t.Fatal("a later extra row was pulled into the head")
		}
		if _, ok := h.Entries["extra"]; ok {
			t.Fatal("the first extra row appeared after a second write")
		}
		if h.Entries["real"] != HashRow([]byte("row-bytes")) || h.Entries["other"] != HashRow([]byte("two")) {
			t.Fatalf("entries: %v", h.Entries)
		}
		return tab.AfterCommit(h)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStaleEpochIsFailsafe(t *testing.T) {
	db := openDB(t)
	tab := newTable(t, db)
	if err := db.Update(func(tx kv.Tx) error {
		h, err := tab.Commit(tx, map[string][]byte{"k": []byte("v1")}, nil)
		if err != nil {
			return err
		}
		return tab.AfterCommit(h)
	}); err != nil {
		t.Fatal(err)
	}
	var first []byte
	_ = db.View(func(tx kv.Tx) error {
		first = append([]byte(nil), tx.Get(tab.Bucket, tab.HeadKey)...)
		return nil
	})
	if err := db.Update(func(tx kv.Tx) error {
		h, err := tab.Commit(tx, map[string][]byte{"k": []byte("v2")}, nil)
		if err != nil {
			return err
		}
		return tab.AfterCommit(h)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx kv.Tx) error {
		return tx.Put(tab.Bucket, tab.HeadKey, first)
	}); err != nil {
		t.Fatal(err)
	}
	var res LoadResult
	_ = db.View(func(tx kv.Tx) error {
		res = tab.Load(tx)
		return nil
	})
	if res.Status != StatusFailsafe {
		t.Fatalf("stale epoch (file > db): %v", res.Status)
	}
	if Accept(res, "k", []byte("v1")) || Accept(res, "k", []byte("v2")) {
		t.Fatal("a stale pair must not Accept")
	}
}

func TestCatchUpRewritesFile(t *testing.T) {
	db := openDB(t)
	tab := newTable(t, db)
	if err := db.Update(func(tx kv.Tx) error {
		_, err := tab.Commit(tx, map[string][]byte{"k": []byte("v")}, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if tab.Anchor.Epoch("settings") != 0 {
		t.Fatal("the file should still be empty before AfterCommit")
	}
	var res LoadResult
	_ = db.View(func(tx kv.Tx) error {
		res = tab.Load(tx)
		return nil
	})
	if res.Status != StatusCatchUp {
		t.Fatalf("catch-up: %v", res.Status)
	}
	if tab.Anchor.Epoch("settings") != 1 {
		t.Fatalf("catch-up did not rewrite the file: %d", tab.Anchor.Epoch("settings"))
	}
	if !Accept(res, "k", []byte("v")) {
		t.Fatal("catch-up should Accept like OK")
	}
}

func TestAfterCommitFsyncs(t *testing.T) {
	var files, dirs int
	origFile, origDir := fsyncFile, fsyncDir
	t.Cleanup(func() { fsyncFile, fsyncDir = origFile, origDir })
	fsyncFile = func(f *os.File) error { files++; return origFile(f) }
	fsyncDir = func(f *os.File) error { dirs++; return origDir(f) }

	a := FileAnchor{Dir: t.TempDir()}
	if err := a.Record("settings", 4); err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("the epochs file was not fsynced")
	}
	if dirs == 0 {
		t.Fatal("directory sync was not attempted")
	}
	b, err := os.ReadFile(filepath.Join(a.Dir, "epochs"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "settings 4\n" {
		t.Fatalf("epochs file: %q", b)
	}
}

func TestCommitFailsafeDoesNotWrite(t *testing.T) {
	db := openDB(t)
	tab := newTable(t, db)
	if err := db.Update(func(tx kv.Tx) error {
		h, err := tab.Commit(tx, map[string][]byte{"k": []byte("v")}, nil)
		if err != nil {
			return err
		}
		return tab.AfterCommit(h)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx kv.Tx) error {
		return tx.Delete(tab.Bucket, tab.HeadKey)
	}); err != nil {
		t.Fatal(err)
	}
	err := db.Update(func(tx kv.Tx) error {
		_, err := tab.Commit(tx, map[string][]byte{"other": []byte("x")}, nil)
		return err
	})
	if err == nil {
		t.Fatal("Commit on a missing head should fail")
	}
}

func TestRebuildRestoresAccept(t *testing.T) {
	db := openDB(t)
	tab := newTable(t, db)
	if err := db.Update(func(tx kv.Tx) error {
		if err := tx.Put(tab.Bucket, []byte("k"), []byte("v")); err != nil {
			return err
		}
		h, err := tab.Commit(tx, map[string][]byte{"k": []byte("v")}, nil)
		if err != nil {
			return err
		}
		return tab.AfterCommit(h)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx kv.Tx) error {
		return tx.Delete(tab.Bucket, tab.HeadKey)
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.View(func(tx kv.Tx) error {
		if res := tab.Load(tx); res.Status != StatusFailsafe {
			t.Fatalf("missing head: %v", res.Status)
		}
		return nil
	})
	if err := db.Update(func(tx kv.Tx) error {
		h, err := tab.Rebuild(tx, map[string][]byte{"k": []byte("v")})
		if err != nil {
			return err
		}
		return tab.AfterCommit(h)
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.View(func(tx kv.Tx) error {
		res := tab.Load(tx)
		if res.Status != StatusOK && res.Status != StatusCatchUp {
			t.Fatalf("after rebuild: %v", res.Status)
		}
		if !Accept(res, "k", []byte("v")) {
			t.Fatal("rebuild did not restore Accept")
		}
		return nil
	})
}
