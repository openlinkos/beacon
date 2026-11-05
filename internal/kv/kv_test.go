package kv

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func openSQLite(t *testing.T) (*SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "beacon.db")
	db, err := OpenSQLite(SQLiteOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func keys(tx Tx, bucket string, r Range) []string {
	var out []string
	tx.Range([]byte(bucket), r, func(k, v []byte) bool {
		out = append(out, string(k))
		return true
	})
	return out
}

func TestPutGetDelete(t *testing.T) {
	db := openAny(t)
	if err := db.Update(func(tx Tx) error {
		if err := tx.Put([]byte("b"), []byte("k1"), []byte("v1")); err != nil {
			return err
		}
		return tx.Put([]byte("b"), []byte("k2"), []byte("v2"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(func(tx Tx) error {
		if v := tx.Get([]byte("b"), []byte("k1")); !bytes.Equal(v, []byte("v1")) {
			t.Errorf("Get k1: %q", v)
		}
		if v := tx.Get([]byte("b"), []byte("missing")); v != nil {
			t.Errorf("Get missing: %q", v)
		}
		if v := tx.Get([]byte("nobucket"), []byte("k1")); v != nil {
			t.Errorf("Get from a bucket never written: %q", v)
		}
		if n := tx.Count([]byte("b")); n != 2 {
			t.Errorf("Count: %d", n)
		}
		if n := tx.Count([]byte("nobucket")); n != 0 {
			t.Errorf("Count of nothing: %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx Tx) error { return tx.Delete([]byte("b"), []byte("k1")) }); err != nil {
		t.Fatal(err)
	}
	_ = db.View(func(tx Tx) error {
		if v := tx.Get([]byte("b"), []byte("k1")); v != nil {
			t.Errorf("deleted key still there: %q", v)
		}
		if n := tx.Count([]byte("b")); n != 1 {
			t.Errorf("Count after delete: %d", n)
		}
		return nil
	})
	// deleting a key that is not there is not an error
	if err := db.Update(func(tx Tx) error { return tx.Delete([]byte("b"), []byte("nope")) }); err != nil {
		t.Fatal(err)
	}
}

func TestRange(t *testing.T) {
	db := openAny(t)
	err := db.Update(func(tx Tx) error {
		for _, k := range []string{"a", "ab", "abc", "b", "z", ""} { // includes the empty key
			if err := tx.Put([]byte("b"), []byte(k), []byte("v")); err != nil {
				return err
			}
		}
		return tx.Put([]byte("other"), []byte("a"), []byte("v"))
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.View(func(tx Tx) error {
		if got, want := keys(tx, "b", Range{}), []string{"", "a", "ab", "abc", "b", "z"}; !slices.Equal(got, want) {
			t.Errorf("whole bucket in key order: %q", got)
		}
		if got, want := keys(tx, "b", Range{Reverse: true}), []string{"z", "b", "abc", "ab", "a", ""}; !slices.Equal(got, want) {
			t.Errorf("whole bucket reversed: %q", got)
		}
		if got, want := keys(tx, "b", Range{Prefix: []byte("a")}), []string{"a", "ab", "abc"}; !slices.Equal(got, want) {
			t.Errorf("prefix: %q", got)
		}
		if got, want := keys(tx, "b", Range{Prefix: []byte("a"), Reverse: true}), []string{"abc", "ab", "a"}; !slices.Equal(got, want) {
			t.Errorf("prefix reversed: %q", got)
		}
		if got, want := keys(tx, "b", Range{Start: []byte("ab")}), []string{"ab", "abc", "b", "z"}; !slices.Equal(got, want) {
			t.Errorf("start: %q", got)
		}
		// reverse from a start begins at the last key before it, and never at it
		if got, want := keys(tx, "b", Range{Start: []byte("ab"), Reverse: true}), []string{"a", ""}; !slices.Equal(got, want) {
			t.Errorf("reverse start: %q", got)
		}
		if got, want := keys(tx, "b", Range{Start: []byte("zz"), Reverse: true}), []string{"z", "b", "abc", "ab", "a", ""}; !slices.Equal(got, want) {
			t.Errorf("reverse start past the end: %q", got)
		}
		if got, want := keys(tx, "b", Range{Prefix: []byte("a"), Start: []byte("ab")}), []string{"ab", "abc"}; !slices.Equal(got, want) {
			t.Errorf("prefix and start: %q", got)
		}
		// the prefix ends the walk when it falls out, in either direction
		var n int
		tx.Range([]byte("b"), Range{Prefix: []byte("a"), Reverse: true}, func(k, v []byte) bool {
			n++
			return true
		})
		if n != 3 {
			t.Errorf("prefix reversed walked %d", n)
		}
		// a callback can stop the walk
		var first string
		tx.Range([]byte("b"), Range{}, func(k, v []byte) bool { first = string(k); return false })
		if first != "" {
			t.Errorf("stop at once: %q", first)
		}
		if got := keys(tx, "never", Range{}); got != nil {
			t.Errorf("a bucket never written: %q", got)
		}
		return nil
	})
	// the value is what was put, byte for byte
	_ = db.Update(func(tx Tx) error {
		raw := []byte{0, 1, 2, 0xFF, 0xFE, 'x'}
		if err := tx.Put([]byte("raw"), []byte("k"), raw); err != nil {
			return err
		}
		if v := tx.Get([]byte("raw"), []byte("k")); !bytes.Equal(v, raw) {
			t.Errorf("binary value: %x", v)
		}
		return nil
	})
}

func TestSequence(t *testing.T) {
	db := openAny(t)
	var first, second uint64
	err := db.Update(func(tx Tx) error {
		var err error
		if first, err = tx.NextSequence([]byte("b")); err != nil {
			return err
		}
		second, err = tx.NextSequence([]byte("b"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("NextSequence: %d %d", first, second)
	}
	// a rolled-back Update does not consume a number
	_ = db.Update(func(tx Tx) error {
		if _, err := tx.NextSequence([]byte("b")); err != nil {
			return err
		}
		return errors.New("roll back")
	})
	var third uint64
	if err := db.Update(func(tx Tx) error {
		var err error
		third, err = tx.NextSequence([]byte("b"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if third != 3 {
		t.Fatalf("after a rollback: %d", third)
	}
	_ = db.View(func(tx Tx) error {
		if s := tx.Sequence([]byte("b")); s != 3 {
			t.Errorf("Sequence: %d", s)
		}
		if s := tx.Sequence([]byte("never")); s != 0 {
			t.Errorf("Sequence of nothing: %d", s)
		}
		if _, err := tx.NextSequence([]byte("b")); err == nil {
			t.Error("NextSequence in a View should fail")
		}
		return nil
	})
	// the sequence survives a restart
	path := filepath.Join(t.TempDir(), "beacon.db")
	db2, err := OpenSQLite(SQLiteOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := db2.Update(func(tx Tx) error {
		if _, err := tx.NextSequence([]byte("c")); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = db2.Close()
	db3, err := OpenSQLite(SQLiteOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db3.Close()
	_ = db3.View(func(tx Tx) error {
		if s := tx.Sequence([]byte("c")); s != 1 {
			t.Errorf("Sequence after reopen: %d", s)
		}
		return nil
	})
}

func TestCompareAndSwap(t *testing.T) {
	db := openAny(t)
	err := db.Update(func(tx Tx) error {
		if err := tx.CompareAndSwap([]byte("b"), []byte("k"), nil, []byte("v1")); err != nil {
			return err
		}
		if err := tx.CompareAndSwap([]byte("b"), []byte("k"), []byte("v1"), []byte("v2")); err != nil {
			return err
		}
		if err := tx.CompareAndSwap([]byte("b"), []byte("missing"), nil, []byte("v")); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ old, value []byte }{
		{[]byte("v1"), []byte("v3")}, // the value moved on
		{nil, []byte("v3")},          // the key exists now
	} {
		err := db.Update(func(tx Tx) error { return tx.CompareAndSwap([]byte("b"), []byte("k"), c.old, c.value) })
		if !errors.Is(err, ErrCompareFailed) {
			t.Errorf("CAS with old %q: %v", c.old, err)
		}
	}
	// a failed insert-CAS must not abort the transaction: later Gets still see the row
	err = db.Update(func(tx Tx) error {
		if err := tx.CompareAndSwap([]byte("b"), []byte("k"), nil, []byte("v3")); !errors.Is(err, ErrCompareFailed) {
			t.Errorf("CAS insert on an existing key: %v", err)
		}
		if v := tx.Get([]byte("b"), []byte("k")); !bytes.Equal(v, []byte("v2")) {
			t.Errorf("Get after a failed CAS insert: %q (nil means the transaction was aborted)", v)
		}
		return ErrCompareFailed
	})
	if !errors.Is(err, ErrCompareFailed) {
		t.Errorf("the failed CAS insert: %v", err)
	}
	// the failed swaps changed nothing
	_ = db.View(func(tx Tx) error {
		if v := tx.Get([]byte("b"), []byte("k")); !bytes.Equal(v, []byte("v2")) {
			t.Errorf("after failed swaps: %q", v)
		}
		return nil
	})
	// swapping in nil deletes (on its own: a failed operation takes its whole transaction with it)
	if err := db.Update(func(tx Tx) error {
		return tx.CompareAndSwap([]byte("b"), []byte("k"), []byte("v2"), nil)
	}); err != nil {
		t.Fatal(err)
	}
	// a swap on a missing key with an expectation fails
	if err := db.Update(func(tx Tx) error {
		return tx.CompareAndSwap([]byte("b"), []byte("gone"), []byte("was"), []byte("v"))
	}); !errors.Is(err, ErrCompareFailed) {
		t.Fatalf("CAS on a missing key: %v", err)
	}
	_ = db.View(func(tx Tx) error {
		if v := tx.Get([]byte("b"), []byte("k")); v != nil {
			t.Errorf("CAS delete left %q", v)
		}
		return nil
	})
}

func TestUpdateRollsBack(t *testing.T) {
	db := openAny(t)
	err := db.Update(func(tx Tx) error {
		if err := tx.Put([]byte("b"), []byte("k"), []byte("v")); err != nil {
			return err
		}
		return errors.New("no")
	})
	if err == nil {
		t.Fatal("the error should come back")
	}
	_ = db.View(func(tx Tx) error {
		if v := tx.Get([]byte("b"), []byte("k")); v != nil {
			t.Errorf("a rolled-back write left %q", v)
		}
		return nil
	})
}

// TestSQLiteLocked: a second open of a locked database fails with ErrLocked, and reads still work
// on a handle that was open read-only — the write lock is one process's, not the file's.
func TestSQLiteLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.db")
	db, err := OpenSQLite(SQLiteOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := time.Now()
	if _, err := OpenSQLite(SQLiteOptions{Path: path}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the lock took %s to refuse; it should not wait", d)
	}
	// a read-only handle can read while the writer holds the lock
	ro, err := OpenSQLite(SQLiteOptions{Path: path, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if err := ro.View(func(tx Tx) error {
		if n := tx.Count([]byte("kv")); n != 0 { // the kv table itself, not a bucket
			t.Errorf("kv rows: %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := ro.Update(func(tx Tx) error { return tx.Put([]byte("b"), []byte("k"), []byte("v")) }); err == nil {
		t.Error("an Update on a read-only handle should fail")
	}
}

// TestWritesAfterLock: the process that holds the lock keeps writing, and its transactions
// serialise.
func TestWritesAfterLock(t *testing.T) {
	db := openAny(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := db.Update(func(tx Tx) error {
				return tx.Put([]byte("b"), []byte(fmt.Sprintf("k%d", i)), []byte("v"))
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	_ = db.View(func(tx Tx) error {
		if n := tx.Count([]byte("b")); n != 8 {
			t.Errorf("writes: %d", n)
		}
		return nil
	})
}

func TestSQLiteReadOnlyMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.db")
	if _, err := OpenSQLite(SQLiteOptions{Path: path, ReadOnly: true}); err == nil {
		t.Fatal("a missing database should not open read-only")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the read-only open created the file")
	}
}

func TestSQLiteOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.db")
	db, err := OpenSQLite(SQLiteOptions{Path: path, JournalMode: "wal", Synchronous: "full"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if m := db.JournalMode(); m != "wal" {
		t.Errorf("journal mode: %s", m)
	}
	if _, err := OpenSQLite(SQLiteOptions{Path: path, JournalMode: "nope"}); err == nil {
		t.Error("a bad journal mode should fail")
	}
	if _, err := OpenSQLite(SQLiteOptions{Path: path, Synchronous: "nope"}); err == nil {
		t.Error("a bad synchronous should fail")
	}
	if _, err := OpenSQLite(SQLiteOptions{}); err == nil {
		t.Error("no path should fail")
	}
}

func TestSQLiteEmptyValue(t *testing.T) {
	db, _ := openSQLite(t)
	if err := db.Update(func(tx Tx) error {
		if err := tx.Put([]byte("b"), []byte("empty"), []byte{}); err != nil {
			return err
		}
		return tx.Put([]byte("b"), []byte("nil"), nil)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(func(tx Tx) error {
		for _, k := range []string{"empty", "nil"} {
			v := tx.Get([]byte("b"), []byte(k))
			if v == nil {
				t.Errorf("Get %s: nil, want an empty slice", k)
			} else if len(v) != 0 {
				t.Errorf("Get %s: %q", k, v)
			}
		}
		if v := tx.Get([]byte("b"), []byte("missing")); v != nil {
			t.Errorf("missing: %q", v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteReadErrorFailsTheTransaction(t *testing.T) {
	db, _ := openSQLite(t)
	if err := db.Update(func(tx Tx) error {
		return tx.Put([]byte("b"), []byte("k"), []byte("v"))
	}); err != nil {
		t.Fatal(err)
	}
	// The table is renamed so Get fails, then renamed back so Put could succeed if the
	// transaction were still treated as healthy. The callback returns nil: Update must still
	// fail from the read, and the Put must not commit.
	err := db.Update(func(tx Tx) error {
		st := tx.(*sqliteTx)
		if _, err := st.tx.Exec("ALTER TABLE kv RENAME TO kv_hidden"); err != nil {
			t.Fatal(err)
		}
		if v := tx.Get([]byte("b"), []byte("k")); v != nil {
			t.Errorf("Get after a failed read: %q", v)
		}
		if _, err := st.tx.Exec("ALTER TABLE kv_hidden RENAME TO kv"); err != nil {
			t.Fatal(err)
		}
		_ = tx.Put([]byte("b"), []byte("k2"), []byte("v2"))
		return nil
	})
	if err == nil {
		t.Fatal("Update should return the read error")
	}
	if err := db.View(func(tx Tx) error {
		if v := tx.Get([]byte("b"), []byte("k2")); v != nil {
			t.Errorf("a write after a failed read was kept: %q", v)
		}
		if v := tx.Get([]byte("b"), []byte("k")); !bytes.Equal(v, []byte("v")) {
			t.Errorf("the earlier value: %q", v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteMkdir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "beacon.db")
	db, err := OpenSQLite(SQLiteOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Update(func(tx Tx) error {
		return tx.Put([]byte("b"), []byte("k"), []byte("v"))
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.db")
	db, err := OpenSQLite(SQLiteOptions{Path: path, BusyTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ms int
	if err := db.Handle().QueryRow("PRAGMA busy_timeout").Scan(&ms); err != nil {
		t.Fatal(err)
	}
	if ms != 3000 {
		t.Errorf("busy_timeout: %d, want 3000", ms)
	}
}
