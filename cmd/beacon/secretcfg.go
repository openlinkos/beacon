package main

import (
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/secrets"
)

// sealConfigOf maps the configuration's vault section onto the vault's own seal configuration: the
// unseal mode, the directory the key material lives in, and whatever the mode's key keeper needs.
// The credentials stay in their files, as every Beacon secret does.
func sealConfigOf(cfg *config.Config) secrets.SealConfig {
	out := secrets.SealConfig{Mode: cfg.Vault.Mode, Dir: cfg.Vault.Dir}
	out.AWS.Region = cfg.Vault.AWS.Region
	out.AWS.KeyID = cfg.Vault.AWS.KeyID
	out.AWS.Endpoint = cfg.Vault.AWS.Endpoint
	out.AWS.AccessKeyFile = cfg.Vault.AWS.AccessKeyFile
	out.AWS.SecretKeyFile = cfg.Vault.AWS.SecretKeyFile
	out.AWS.SessionTokenFile = cfg.Vault.AWS.SessionTokenFile
	out.GCP.KeyName = cfg.Vault.GCP.KeyName
	out.GCP.Endpoint = cfg.Vault.GCP.Endpoint
	out.GCP.TokenFile = cfg.Vault.GCP.TokenFile
	out.Transit.Address = cfg.Vault.Transit.Address
	out.Transit.KeyName = cfg.Vault.Transit.KeyName
	out.Transit.TokenFile = cfg.Vault.Transit.TokenFile
	return out
}
