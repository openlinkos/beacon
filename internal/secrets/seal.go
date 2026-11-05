package secrets

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/argon2"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

// Where the root key comes from (the design's section 8.3). File is the default: a 32-byte file in
// the secrets directory, made at the first start. Passphrase needs a person's passphrase on top of
// the file key, and starts sealed after a restart — monitoring and the SSH channel carry on; the
// key functions wait. The KMS modes have the KMS unwrap the root key at startup; the wrapped blob
// alone is on the disk.
const (
	SealFile       = "file"
	SealPassphrase = "passphrase"
	SealAWSKMS     = "aws-kms"
	SealGCPKMS     = "gcp-kms"
	SealTransit    = "transit"
)

// SealModes lists the unseal modes.
var SealModes = []string{SealFile, SealPassphrase, SealAWSKMS, SealGCPKMS, SealTransit}

// ValidSealMode reports whether s is an unseal mode.
func ValidSealMode(s string) bool {
	for _, m := range SealModes {
		if m == s {
			return true
		}
	}
	return false
}

// The files of the secrets directory, none of them ever logged, all 0600.
const (
	fileRootKey   = "root.key"       // file mode: the root key itself
	filePassKey   = "passphrase.key" // passphrase mode: the file-key half
	filePassSalt  = "passphrase.salt"
	fileRKWrapped = "rk.wrapped" // the KMS modes: the root key as the KMS wrapped it
)

// SealConfig is the unseal configuration, from config.yaml: it is needed before the database says
// anything, so it lives in the file and not among the settings. Credentials are read from their own
// files, as every Beacon secret is.
type SealConfig struct {
	Mode string `json:"mode"` // one of SealModes; file when empty
	Dir  string `json:"dir"`  // the secrets directory

	// AWS KMS. Endpoint is for a local one (a test); empty is AWS's own.
	AWS struct {
		Region           string `json:"region,omitempty"`
		KeyID            string `json:"key_id,omitempty"`
		Endpoint         string `json:"endpoint,omitempty"`
		AccessKeyFile    string `json:"access_key_file,omitempty"`
		SecretKeyFile    string `json:"secret_key_file,omitempty"`
		SessionTokenFile string `json:"session_token_file,omitempty"`
	} `json:"aws,omitempty"`

	// GCP KMS: KeyName is the full resource name
	// projects/…/locations/…/keyRings/…/cryptoKeys/…; TokenFile holds a bearer token. Endpoint is
	// for a local one (a test); empty is Google's own.
	GCP struct {
		KeyName   string `json:"key_name,omitempty"`
		Endpoint  string `json:"endpoint,omitempty"`
		TokenFile string `json:"token_file,omitempty"`
	} `json:"gcp,omitempty"`

	// Vault or OpenBao transit: Address is its URL, KeyName the transit key, TokenFile the token.
	Transit struct {
		Address   string `json:"address,omitempty"`
		KeyName   string `json:"key_name,omitempty"`
		TokenFile string `json:"token_file,omitempty"`
	} `json:"transit,omitempty"`
}

// ErrSealed is what a sealed vault answers: the key functions wait for the owner, while monitoring
// and the SSH channel carry on.
var ErrSealed = errors.New("the vault is sealed; enter the passphrase to unseal it")

// ErrWrongPassphrase is a passphrase that does not give this vault's root key.
var ErrWrongPassphrase = errors.New("the passphrase does not unseal this vault")

// rootKeys holds the unsealed keys of the moment. The zero value is sealed.
type rootKeys struct {
	mu sync.RWMutex
	rk []byte // nil while sealed
	sk []byte
}

// keys returns the root and signature keys, or ErrSealed.
func (r *rootKeys) keys() (rk, sk []byte, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.rk == nil {
		return nil, nil, ErrSealed
	}
	return r.rk, r.sk, nil
}

// sealed reports whether the vault is sealed right now.
func (r *rootKeys) sealed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rk == nil
}

// set takes a root key in, zeroing what was there.
func (r *rootKeys) set(rk []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.rk)
	clear(r.sk)
	r.rk = append([]byte(nil), rk...)
	r.sk = policyKey(rk)
}

// drop seals the vault again, best effort over the memory.
func (r *rootKeys) drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.rk)
	clear(r.sk)
	r.rk, r.sk = nil, nil
}

// argon2id parameters for the passphrase derivation, the same RFC 9106 second recommended option
// the accounts use: a passphrase is entered rarely, so the wait is welcome.
var passParams = struct {
	time, memory uint32
	threads      uint8
}{3, 64 * 1024, 4}

func argonPassphrase(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, passParams.time, passParams.memory, passParams.threads, 32)
}

// UseCheapPassphraseForTest lowers argon2id so tests that unseal many times (the address lockout)
// finish. Restore with the returned function.
func UseCheapPassphraseForTest() func() {
	old := passParams
	passParams = struct {
		time, memory uint32
		threads      uint8
	}{1, 32, 1}
	return func() { passParams = old }
}

func (s *Service) putKeyFile(path string, b []byte) error {
	if s.writeKeyFileFn != nil {
		return s.writeKeyFileFn(path, b)
	}
	return writeKeyFile(path, b)
}

func (s *Service) moveFile(tmp, dest string) error {
	if s.replaceFileFn != nil {
		return s.replaceFileFn(tmp, dest)
	}
	return replaceFile(tmp, dest)
}

func (s *Service) keeper() (kmsClient, error) {
	if s.keeperFn != nil {
		return s.keeperFn()
	}
	return s.kmsClient()
}

// takeRootKeyFile reads a 32-byte root key file and takes it in against the key check.
func (s *Service) takeRootKeyFile(path, mode string) error {
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return err
	}
	if len(b) != RKSize {
		return fmt.Errorf("%s: the root key file holds %d bytes, not %d; it is made at the first start and holds exactly %d",
			filepath.Base(path), len(b), RKSize, RKSize)
	}
	return s.takeRootKey(b, mode)
}

// finishStagedRootKey takes the staged root key in and replaces dest with it, for the crash
// between the database commit and the rename.
func (s *Service) finishStagedRootKey(staged, dest, mode string) error {
	if err := s.takeRootKeyFile(staged, mode); err != nil {
		return err
	}
	if err := s.moveFile(staged, dest); err != nil {
		s.d.Log.Error("the staged root key opened the vault but could not replace the file", "err", err)
	}
	return nil
}

func joinModes() string { return joinStrings(SealModes) }

// joinStrings joins with ", " (kept here so the package needs no strings import in this file).
func joinStrings(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// initSeal brings a vault's root key up at start: it makes what the mode needs on the first run
// and unseals what unseals itself (every mode but the passphrase one). The key check — a fixed
// plaintext under the root key — tells a right key from a wrong one before anything is tried.
func (s *Service) initSeal() error {
	switch s.cfg.Mode {
	case SealFile:
		return s.initFileSeal()
	case SealPassphrase:
		return s.initPassphraseSeal()
	case SealAWSKMS, SealGCPKMS, SealTransit:
		return s.initKMSSeal()
	default:
		return fmt.Errorf("unknown seal mode %q; one of %s", s.cfg.Mode, joinModes())
	}
}

// initFileSeal makes or reads the 32-byte root key file.
func (s *Service) initFileSeal() error {
	path := filepath.Join(s.cfg.Dir, fileRootKey)
	staged := path + ".new"
	err := s.takeRootKeyFile(path, SealFile)
	switch {
	case err == nil:
		_ = os.Remove(staged)
		return nil
	case errors.Is(err, os.ErrNotExist):
		if s.finishStagedRootKey(staged, path, SealFile) == nil {
			return nil
		}
		rk, err := randomKey()
		if err != nil {
			return err
		}
		if err := s.putKeyFile(path, rk); err != nil {
			return err
		}
		s.d.Log.Info("the vault made its root key", "mode", SealFile, "file", fileRootKey)
		return s.takeRootKey(rk, SealFile)
	default:
		if s.finishStagedRootKey(staged, path, SealFile) == nil {
			return nil
		}
		return err
	}
}

// initPassphraseSeal makes the two halves the passphrase mode needs — the file key and the argon2
// salt — at the first start; afterwards it always starts sealed and waits for the passphrase (the
// file halves alone give nothing, which is the point of the mode).
func (s *Service) initPassphraseSeal() error {
	keyPath := filepath.Join(s.cfg.Dir, filePassKey)
	saltPath := filepath.Join(s.cfg.Dir, filePassSalt)
	if _, err := os.Stat(keyPath); err == nil {
		if _, err := os.Stat(saltPath); err != nil {
			return fmt.Errorf("the passphrase mode needs %s beside %s", filePassSalt, filePassKey)
		}
		s.d.Log.Info("the vault is sealed; enter the passphrase to unseal it", "mode", SealPassphrase)
		return nil // sealed until a person unseals it
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fileKey, err := randomKey()
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return err
	}
	if err := s.putKeyFile(keyPath, fileKey); err != nil {
		return err
	}
	if err := s.putKeyFile(saltPath, salt); err != nil {
		return err
	}
	s.d.Log.Info("the vault made its passphrase halves; the first passphrase entered with `beacon key unseal` becomes the one",
		"mode", SealPassphrase)
	return nil // sealed; the first Unseal sets the key check for the passphrase it is given
}

// initKMSSeal reads the wrapped root key and asks the KMS to unwrap it; a KMS that cannot be
// reached leaves the vault sealed, with the alarm, rather than failing the service.
func (s *Service) initKMSSeal() error {
	wrapped, err := os.ReadFile(filepath.Join(s.cfg.Dir, fileRKWrapped))
	if errors.Is(err, os.ErrNotExist) {
		rk, err := randomKey()
		if err != nil {
			return err
		}
		c, err := s.keeper()
		if err != nil {
			return err
		}
		w, err := c.encrypt(s.ctx, rk)
		if err != nil {
			return fmt.Errorf("the KMS could not wrap the new root key: %w", err)
		}
		if err := s.putKeyFile(filepath.Join(s.cfg.Dir, fileRKWrapped), w); err != nil {
			return err
		}
		s.d.Log.Info("the vault made its root key and left it wrapped with the KMS", "mode", s.cfg.Mode)
		return s.takeRootKey(rk, s.cfg.Mode)
	}
	if err != nil {
		return err
	}
	c, err := s.keeper()
	if err != nil {
		return err
	}
	rk, err := c.decrypt(s.ctx, wrapped)
	if err != nil {
		staged := filepath.Join(s.cfg.Dir, fileRKWrapped) + ".new"
		if sb, rerr := os.ReadFile(staged); rerr == nil { //nolint:gosec // the operator names the secrets directory
			if rk2, derr := c.decrypt(s.ctx, sb); derr == nil {
				if terr := s.takeRootKey(rk2, s.cfg.Mode); terr == nil {
					if merr := s.moveFile(staged, filepath.Join(s.cfg.Dir, fileRKWrapped)); merr != nil {
						s.d.Log.Error("the staged root key opened the vault but could not replace the file", "err", merr)
					}
					return nil
				}
			}
		}
		s.d.Log.Error("the KMS could not unwrap the root key; the vault is sealed", "mode", s.cfg.Mode, "err", err)
		_ = s.alarm("the vault could not be unsealed", "the KMS did not unwrap the root key")
		return nil
	}
	_ = os.Remove(filepath.Join(s.cfg.Dir, fileRKWrapped) + ".new")
	return s.takeRootKey(rk, s.cfg.Mode)
}

// takeRootKey checks a root key against the key check and takes it in; a key that fails is refused
// (wrong passphrase, wrong KMS answer, a replaced file), leaving the vault sealed.
func (s *Service) takeRootKey(rk []byte, mode string) error {
	ct, ok := s.metaGet(metaKeyCheck)
	if !ok {
		// the very first start: make the check
		check, err := wrap(rk, []byte(adKeyCheckV1), keyCheckAAD())
		if err != nil {
			return err
		}
		if err := s.metaPut(metaKeyCheck, check); err != nil {
			return err
		}
		s.keys.set(rk)
		s.d.Log.Info("the vault is unsealed", "mode", mode, "key", keyID(rk))
		return nil
	}
	if _, err := unwrap(rk, ct, keyCheckAAD()); err != nil {
		return fmt.Errorf("the root key does not open this vault (key %s)", keyID(rk))
	}
	s.keys.set(rk)
	s.d.Log.Info("the vault is unsealed", "mode", mode, "key", keyID(rk))
	return nil
}

// Unseal takes the passphrase, derives the root key from it with the file halves and unseals the
// vault when it opens the key check. A vault that has never been given a passphrase is refused
// (InitPassphrase is that first step).
func (s *Service) Unseal(passphrase string) error {
	if s.cfg.Mode != SealPassphrase {
		return errors.New("only the passphrase mode is unsealed with a passphrase")
	}
	fileKey, err := os.ReadFile(filepath.Join(s.cfg.Dir, filePassKey))
	if err != nil {
		return err
	}
	salt, err := os.ReadFile(filepath.Join(s.cfg.Dir, filePassSalt))
	if err != nil {
		return err
	}
	rk := rootKeyFromPassphrase(argonPassphrase(passphrase, salt), fileKey)
	ct, ok := s.metaGet(metaKeyCheck)
	if !ok {
		return ErrNotInitialised
	}
	if _, err := unwrap(rk, ct, keyCheckAAD()); err != nil {
		return ErrWrongPassphrase
	}
	s.keys.set(rk)
	s.keyChanged()
	s.ensurePolicyHead()
	s.reloadSignedRows()
	s.auditKey("unseal", "the vault was unsealed with the passphrase")
	return nil
}

// ErrNotInitialised is Unseal before InitPassphrase has set the first passphrase.
var ErrNotInitialised = errors.New("this vault has no passphrase yet; initialise it first")

// InitPassphrase is the first passphrase a fresh passphrase-mode vault is given. Later Unseal
// calls only open with that passphrase; they never take a new one.
func (s *Service) InitPassphrase(passphrase string) error {
	if s.cfg.Mode != SealPassphrase {
		return errors.New("only the passphrase mode is initialised with a passphrase")
	}
	if _, ok := s.metaGet(metaKeyCheck); ok {
		return errors.New("this vault already has a passphrase")
	}
	fileKey, err := os.ReadFile(filepath.Join(s.cfg.Dir, filePassKey))
	if err != nil {
		return err
	}
	salt, err := os.ReadFile(filepath.Join(s.cfg.Dir, filePassSalt))
	if err != nil {
		return err
	}
	rk := rootKeyFromPassphrase(argonPassphrase(passphrase, salt), fileKey)
	check, err := wrap(rk, []byte(adKeyCheckV1), keyCheckAAD())
	if err != nil {
		return err
	}
	if err := s.metaPut(metaKeyCheck, check); err != nil {
		return err
	}
	s.keys.set(rk)
	s.keyChanged()
	s.ensurePolicyHead()
	s.reloadSignedRows()
	s.auditKey("init", "the vault's passphrase was set")
	return nil
}

// Seal drops the keys from memory; the passphrase mode needs the passphrase again, the others a
// fresh start or their own unseal.
func (s *Service) Seal() error {
	if s.keys.sealed() {
		return ErrSealed
	}
	s.keys.drop()
	s.auditKey("seal", "the vault was sealed")
	return nil
}

// Sealed reports whether the vault is sealed right now.
func (s *Service) Sealed() bool { return s.keys.sealed() }

// Close drops the keys from memory, best effort; the database stays with its holder.
func (s *Service) Close() error {
	s.keys.drop()
	return nil
}

// SealModeName is the configured unseal mode.
func (s *Service) SealModeName() string { return s.cfg.Mode }

// SK exposes the signature key to the settings' migration and the audit checkpoints. Nil while
// sealed; the copy is the caller's to zero.
func (s *Service) SK() []byte {
	_, key, err := s.keys.keys()
	if err != nil {
		return nil
	}
	out := make([]byte, len(key))
	copy(out, key)
	return out
}

// RotateRootKey turns the root key: in the file and KMS modes a new random key is made and every
// collection key of every version is wrapped again under it; in the passphrase mode the new
// passphrase derives the new root key with the same file halves. The key check moves with it, and
// the signature key changes with it — the settings' rows are re-signed onto the new one by the
// wiring that calls this. The old key never opens the vault again; the whole turn is a few seconds
// even with many collections, as the design promises.
func (s *Service) RotateRootKey(newPassphrase string) error {
	oldRK, oldSK, err := s.keys.keys()
	if err != nil {
		return err // rotating needs the vault open (the passphrase mode rotates onto a new passphrase, not from a sealed one)
	}
	var newRK []byte
	switch s.cfg.Mode {
	case SealFile:
		newRK, err = randomKey()
		if err != nil {
			return err
		}
	case SealPassphrase:
		if newPassphrase == "" {
			return errors.New("the passphrase mode rotates onto a new passphrase; give the new one")
		}
		fileKey, err := os.ReadFile(filepath.Join(s.cfg.Dir, filePassKey))
		if err != nil {
			return err
		}
		salt, err := os.ReadFile(filepath.Join(s.cfg.Dir, filePassSalt))
		if err != nil {
			return err
		}
		newRK = rootKeyFromPassphrase(argonPassphrase(newPassphrase, salt), fileKey)
	default: // the KMS modes: a new random key, wrapped by the KMS again
		newRK, err = randomKey()
		if err != nil {
			return err
		}
	}
	newSK := policyKey(newRK)
	// wrap and stage the new root key before the database commit, so a crash after rewrap
	// still has a file that can open the new wraps
	var staged, dest string
	switch s.cfg.Mode {
	case SealFile:
		dest = filepath.Join(s.cfg.Dir, fileRootKey)
		staged = dest + ".new"
		if err := s.putKeyFile(staged, newRK); err != nil {
			return err
		}
	case SealPassphrase:
		// nothing on disk changes: the new passphrase derives the new root key with the same halves
	default:
		c, err := s.keeper()
		if err != nil {
			return err
		}
		w, err := c.encrypt(s.ctx, newRK)
		if err != nil {
			return fmt.Errorf("the KMS could not wrap the rotated root key: %w", err)
		}
		dest = filepath.Join(s.cfg.Dir, fileRKWrapped)
		staged = dest + ".new"
		if err := s.putKeyFile(staged, w); err != nil {
			return err
		}
	}
	if err := s.rewrapAll(newRK, oldRK, oldSK, newSK); err != nil {
		if staged != "" {
			_ = os.Remove(staged)
		}
		return err
	}
	if staged != "" {
		if err := s.moveFile(staged, dest); err != nil {
			return err
		}
	}
	s.keys.set(newRK)
	s.keyChanged() // the settings re-sign onto the new SK here
	s.reloadSignedRows()
	s.auditKey("rotate", "the root key was rotated; every collection key was wrapped again")
	return nil
}

// rewrapAll wraps every collection key of every version under newRK, inside one transaction, moves
// the key check, and re-signs every policy row onto the new signature key (the root key's rotation
// turns SK with it, so the rows would otherwise all fail their signatures). The old root key is
// needed only here; the collection keys themselves do not change, so no item ciphertext moves.
func (s *Service) rewrapAll(newRK, oldRK, oldSK, newSK []byte) error {
	type pair struct {
		collection string
		version    int64
		ck         []byte
	}
	var pairs []pair
	defer func() {
		for i := range pairs {
			clear(pairs[i].ck)
		}
	}()
	err := s.d.Store.Storage().Update(func(tx kv.Tx) error {
		pairs = nil
		var inner error
		tx.Range(bucketKeys, kv.Range{}, func(_, v []byte) bool {
			var kr keyRow
			if json.Unmarshal(v, &kr) != nil {
				inner = errors.New("a collection key row could not be read")
				return false
			}
			ck, err := unwrap(oldRK, kr.Wrapped, keyWrapAAD(kr.Collection, kr.Version))
			if err != nil {
				inner = fmt.Errorf("a collection key of version %d could not be unwrapped: %w", kr.Version, err)
				return false
			}
			pairs = append(pairs, pair{kr.Collection, kr.Version, ck})
			return true
		})
		if inner != nil {
			return inner
		}
		for _, p := range pairs {
			w, err := wrap(newRK, p.ck, keyWrapAAD(p.collection, p.version))
			if err != nil {
				return err
			}
			if err := putKeyRow(tx, keyRow{Collection: p.collection, Version: p.version, Wrapped: w,
				At: s.now(), Actor: "beacon", Reason: "root key rotated"}); err != nil {
				return err
			}
		}
		if err := resignPolicyRowsTx(tx, oldSK, newSK); err != nil {
			return err
		}
		check, err := wrap(newRK, []byte(adKeyCheckV1), keyCheckAAD())
		if err != nil {
			return err
		}
		return metaPutTx(tx, metaKeyCheck, check)
	})
	return err
}

// resignPolicyRowsTx re-signs every policy row that verifies under old onto new, inside a
// transaction. A row that verifies under neither is left alone: reload reports it, as it should.
func resignPolicyRowsTx(tx kv.Tx, oldSK, newSK []byte) error {
	// the rows are read whole first, for a range cannot write while it walks
	type pair struct {
		key  []byte
		body []byte
	}
	doresign := func(bucket []byte, parts func([]byte) (row any, sig []byte, p []string, re func() error)) error {
		var pairs []pair
		tx.Range(bucket, kv.Range{}, func(k, v []byte) bool {
			pairs = append(pairs, pair{key: append([]byte(nil), k...), body: append([]byte(nil), v...)})
			return true
		})
		for _, pr := range pairs {
			row, sig, parts, rewrite := parts(pr.body)
			if row == nil || !verifyPolicy(oldSK, sig, parts...) {
				continue
			}
			if err := rewrite(); err != nil {
				return err
			}
			b, err := json.Marshal(row)
			if err != nil {
				return err
			}
			if err := tx.Put(bucket, pr.key, b); err != nil {
				return err
			}
		}
		return nil
	}
	collection := func(body []byte) (any, []byte, []string, func() error) {
		var r signedCollection
		if json.Unmarshal(body, &r) != nil {
			return nil, nil, nil, nil
		}
		return &r, r.Sig, r.signedParts(), func() error { r.Sig = signRow(newSK, r.signedParts()...); return nil }
	}
	item := func(body []byte) (any, []byte, []string, func() error) {
		var r signedItem
		if json.Unmarshal(body, &r) != nil {
			return nil, nil, nil, nil
		}
		return &r, r.Sig, r.signedParts(), func() error { r.Sig = signRow(newSK, r.signedParts()...); return nil }
	}
	tag := func(body []byte) (any, []byte, []string, func() error) {
		var r signedTag
		if json.Unmarshal(body, &r) != nil {
			return nil, nil, nil, nil
		}
		return &r, r.Sig, r.signedParts(), func() error { r.Sig = signRow(newSK, r.signedParts()...); return nil }
	}
	perm := func(body []byte) (any, []byte, []string, func() error) {
		var r Permission
		if json.Unmarshal(body, &r) != nil {
			return nil, nil, nil, nil
		}
		return &r, r.Sig, r.signedParts(), func() error { r.Sig = signRow(newSK, r.signedParts()...); return nil }
	}
	decoders := []struct {
		bucket []byte
		decode func([]byte) (any, []byte, []string, func() error)
	}{
		{bucketCollections, collection}, {bucketItems, item}, {bucketTags, tag}, {bucketPerms, perm},
	}
	for _, d := range decoders {
		if err := doresign(d.bucket, d.decode); err != nil {
			return err
		}
	}
	return writePolicyHeadTx(tx, newSK)
}

// RotateCollectionKey gives a collection a new key version and re-encrypts its items' details under
// it in the background, for when a key is suspected; writes to the collection wait until it is
// done. Removing a member does not rotate anything — members never hold the key.
func (s *Service) RotateCollectionKey(collection string) error {
	if s.keys.sealed() {
		return ErrSealed
	}
	col, ok := s.Collection(collection)
	if !ok {
		return fmt.Errorf("%w: no collection %q", ErrNotFound, collection)
	}
	s.rotMu.Lock()
	if s.rotating[col.ID] {
		s.rotMu.Unlock()
		return fmt.Errorf("%w: this collection's key is being rotated already", ErrConflict)
	}
	s.rotating[col.ID] = true
	s.rotMu.Unlock()
	go func() {
		defer func() {
			s.rotMu.Lock()
			delete(s.rotating, col.ID)
			s.rotMu.Unlock()
		}()
		if err := s.rotateCollectionKeyNow(col); err != nil {
			s.d.Log.Error("the collection key rotation failed", "collection", col.Name, "err", err)
			_ = s.alarm("a collection key rotation failed", col.Name+": "+err.Error())
			_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_key_rotate_failed",
				Target: col.Name, Detail: err.Error()})
		}
	}()
	return nil
}

// rotatingNow reports whether a collection's key is being rotated.
func (s *Service) rotatingNow(collectionID string) bool {
	s.rotMu.Lock()
	defer s.rotMu.Unlock()
	return s.rotating[collectionID]
}

// rotateCollectionKeyNow is the rotation itself: a new CK version, every version of every item
// decrypted with the old and encrypted under the new, the collection row re-signed with the new
// key version. One transaction, so a failure leaves the collection as it was.
func (s *Service) rotateCollectionKeyNow(col Collection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rk, _, err := s.keys.keys()
	if err != nil {
		return err
	}
	oldCK, err := s.collectionKey(col.ID, col.KeyVersion)
	if err != nil {
		return err
	}
	defer clear(oldCK)
	newCK, err := randomKey()
	if err != nil {
		return err
	}
	defer clear(newCK)
	newVersion := col.KeyVersion + 1
	sk, err := s.sk()
	if err != nil {
		return err
	}
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		var itemIDs []string
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) == nil && r.Collection == col.ID {
				itemIDs = append(itemIDs, r.ID)
			}
			return true
		})
		for _, id := range itemIDs {
			vprefix := []byte(id + "\x00")
			var versions []ItemVersion
			var files []struct {
				key []byte
				att Attachment
			}
			var readErr error
			tx.Range(bucketVersions, kv.Range{Prefix: vprefix}, func(_, v []byte) bool {
				var ver ItemVersion
				if json.Unmarshal(v, &ver) != nil {
					readErr = fmt.Errorf("a version of item %s could not be read", id)
					return false
				}
				versions = append(versions, ver)
				return true
			})
			if readErr != nil {
				return readErr
			}
			tx.Range(bucketFiles, kv.Range{Prefix: vprefix}, func(k, v []byte) bool {
				var att Attachment
				if json.Unmarshal(v, &att) != nil {
					readErr = fmt.Errorf("an attachment of item %s could not be read", id)
					return false
				}
				files = append(files, struct {
					key []byte
					att Attachment
				}{append([]byte(nil), k...), att})
				return true
			})
			if readErr != nil {
				return readErr
			}
			for _, ver := range versions {
				details, err := unwrap(oldCK, ver.CT, itemAAD(ver.Item, ver.Version, col.ID, ver.KeyVersion))
				if err != nil {
					return fmt.Errorf("version %d of item %s could not be opened: %w", ver.Version, id, err)
				}
				ver.KeyVersion = newVersion
				ct, err := wrap(newCK, details, itemAAD(ver.Item, ver.Version, col.ID, newVersion))
				if err != nil {
					return fmt.Errorf("version %d of item %s could not be wrapped: %w", ver.Version, id, err)
				}
				ver.CT = ct
				b, err := json.Marshal(ver)
				if err != nil {
					return err
				}
				if err := tx.Put(bucketVersions, versionRowKey(ver.Item, ver.Version), b); err != nil {
					return err
				}
			}
			for _, f := range files {
				att := f.att
				plain, err := unwrap(oldCK, att.CT, attachmentAAD(att.Item, att.Version, col.ID, col.KeyVersion, att.Name))
				if err != nil {
					return fmt.Errorf("attachment %s of item %s could not be opened: %w", att.Name, id, err)
				}
				ct, err := wrap(newCK, plain, attachmentAAD(att.Item, att.Version, col.ID, newVersion, att.Name))
				if err != nil {
					return fmt.Errorf("attachment %s of item %s could not be wrapped: %w", att.Name, id, err)
				}
				att.CT = ct
				b, err := json.Marshal(att)
				if err != nil {
					return err
				}
				if err := tx.Put(bucketFiles, f.key, b); err != nil {
					return err
				}
			}
		}
		w, err := wrap(rk, newCK, keyWrapAAD(col.ID, newVersion))
		if err != nil {
			return err
		}
		if err := putKeyRow(tx, keyRow{Collection: col.ID, Version: newVersion, Wrapped: w,
			At: s.now(), Actor: "beacon", Reason: "collection key rotated"}); err != nil {
			return err
		}
		next := col
		next.KeyVersion = newVersion
		if err := putCollectionTx(tx, next, sk); err != nil {
			return err
		}
		return writePolicyHeadTx(tx, sk)
	})
	if err != nil {
		return err
	}
	s.reloadSignedRows()
	_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_key_rotate_collection",
		Target: col.Name, Detail: fmt.Sprintf("the collection key moved to version %d and the items were encrypted again", newVersion)})
	return nil
}

// writeKeyFile writes key material with the permissions of a secret: 0600, in a directory of 0700.
func writeKeyFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// replaceFile moves tmp onto dest, replacing dest when the operating system allows it.
func replaceFile(tmp, dest string) error {
	if err := os.Rename(tmp, dest); err == nil {
		return nil
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmp, dest)
}
