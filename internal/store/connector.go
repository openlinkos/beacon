package store

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

// ConnectorAddress is the address of a machine behind NAT: Beacon cannot connect to it, so the machine
// connects out to the SSH channel (ssh -R) with its connector key, and Beacon reaches its sshd through
// that connection.
const ConnectorAddress = "connector"

// Connector reports whether the machine connects out to the channel rather than being dialled.
func (m Machine) Connector() bool { return m.Address == ConnectorAddress }

// MachineByConnectorKey returns the connector machine whose key has this SHA256 fingerprint.
func (s *Store) MachineByConnectorKey(fingerprint string) (Machine, bool) {
	var out Machine
	found := false
	if fingerprint == "" {
		return out, false
	}
	_ = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMachines).ForEach(func(_, v []byte) error {
			var m Machine
			if json.Unmarshal(v, &m) == nil && m.Connector() && m.ConnectorFingerprint == fingerprint {
				out, found = m, true
			}
			return nil
		})
	})
	return out, found
}
