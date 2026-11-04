package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openlinkos/beacon/internal/settings"
)

// The settings of the secrets work ride the configuration tools: get_config with kind "settings"
// shows them, put_config with kind "settings" changes one, delete_config resets one, and the
// history tools answer for them — the same operations the REST routes and the console's settings
// page offer, through the same service.

// SettingsInstructions follow Instructions when the configuration tools have the settings.
const SettingsInstructions = `The configuration tools also carry the settings of the secrets work (kind "settings"): the limits
requests and grants run by, the review chain, rate limits and anomalies, retention, import, backup
and storage leases — each with a registered default, changed values kept as versions with who
changed them and why, revertible. get_config with kind "settings" and a key shows one resolved for
a collection, tag, entitlement or request: the more specific level wins, tags take the stricter
value, and a request is clamped to the bound the level above gives. Changing one needs the
secrets:admin scope and a reason; rows that fail their signature are served as absent and
reported, so a change made straight in the database never takes effect.`

// settingsKind is the configuration kind that addresses the settings.
const settingsKind = "settings"

// errSettingsInput says the call named something there is not; the MCP answers carry plain words.
var errSettingsInput = errors.New("no such setting")

// WithSettings adds the settings to the configuration tools (kind "settings").
func WithSettings(s *settings.Service) Option { return func(o *options) { o.settings = s } }

// settingsActor checks that the caller may change the settings.
func settingsActor(ctx context.Context) (settings.Actor, error) {
	who, err := need(ctx, "secrets:admin")
	return settings.Actor{Name: who.Name, Via: who.Via}, err
}

// getSettings is get_config's settings kind: everything, or one resolved for a selector.
func getSettings(ctx context.Context, svc *settings.Service, in configIn) (any, error) {
	if _, err := need(ctx, "status:read", "secrets:admin"); err != nil {
		return nil, err
	}
	if in.Key == "" {
		if in.Collection != "" || in.Entitlement != "" || in.Request != "" || len(in.Tag) > 0 {
			return nil, errors.New("name the setting too (key), when you name where it applies")
		}
		return svc.List(), nil
	}
	d, ok := settings.Get(in.Key)
	if !ok {
		return nil, fmt.Errorf("no setting called %s; get_config with kind settings and no key lists what there is", in.Key)
	}
	sel := settings.Selector{Collection: in.Collection, Tags: in.Tag, Entitlement: in.Entitlement, Request: in.Request}
	res, levels, err := svc.Value(d.Key, sel)
	if err != nil {
		return nil, err
	}
	v := settings.ViewOf(d, res)
	if len(levels) > 1 {
		v.Levels = []settings.Level{}
		for _, l := range levels {
			v.Levels = append(v.Levels, settings.Level{From: l.From, Target: l.Target, Value: l.Value})
		}
	}
	return v, nil
}

// putSettings is put_config's settings kind.
func putSettings(ctx context.Context, svc *settings.Service, in putConfigIn) (any, error) {
	who, err := settingsActor(ctx)
	if err != nil {
		return nil, err
	}
	scope := in.Scope
	if scope == "" {
		scope = settings.ScopeSystem
	}
	return svc.Set(ctx, who, scope, in.Target, in.Key, json.RawMessage(mustJSON(in.Value)), in.Note, in.IfVersion)
}

// deleteSettings is delete_config's settings kind: reset to what the level above gives.
func deleteSettings(ctx context.Context, svc *settings.Service, in deleteConfigIn) (any, error) {
	who, err := settingsActor(ctx)
	if err != nil {
		return nil, err
	}
	scope := in.Scope
	if scope == "" {
		scope = settings.ScopeSystem
	}
	return svc.Reset(ctx, who, scope, in.Target, in.Key, in.Note, in.IfVersion)
}

// settingsChanges is list_config_changes' settings kind.
func settingsChanges(ctx context.Context, svc *settings.Service, in configChangesIn) (any, error) {
	if _, err := need(ctx, "status:read", "secrets:admin"); err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	return struct {
		Changes []settings.Change `json:"changes"`
	}{Changes: svc.History(in.Key, in.Scope, in.Target, in.Before, min(limit, 200))}, nil
}

// settingChange is get_config_change's settings kind.
func settingChange(ctx context.Context, svc *settings.Service, in versionIn) (any, error) {
	if _, err := need(ctx, "status:read", "secrets:admin"); err != nil {
		return nil, err
	}
	ch, ok := svc.ChangeAt(in.Version)
	if !ok {
		return nil, fmt.Errorf("no settings change with version %d", in.Version)
	}
	return ch, nil
}

// revertSetting is revert_config_change's settings kind.
func revertSetting(ctx context.Context, svc *settings.Service, in revertIn) (any, error) {
	who, err := settingsActor(ctx)
	if err != nil {
		return nil, err
	}
	return svc.Revert(ctx, who, in.Version, in.Note)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// settingChangeIfSettings answers get_config_change for a settings change: settings and
// configuration versions are their own sequences, so the version is looked for among the settings
// first, and a caller who may read the status may read either.
func settingChangeIfSettings(ctx context.Context, svc *settings.Service, in versionIn) (any, bool, error) {
	if _, err := need(ctx, "status:read"); err != nil {
		return nil, false, nil // let the configuration's own scope check answer
	}
	ch, ok := svc.ChangeAt(in.Version)
	if !ok {
		return nil, false, nil
	}
	return ch, true, nil
}

// revertSettingIfSettings answers revert_config_change for a settings change, when the caller may
// change the settings; otherwise the configuration's own path answers.
func revertSettingIfSettings(ctx context.Context, svc *settings.Service, in revertIn) (any, bool, error) {
	if _, err := need(ctx, "secrets:admin"); err != nil {
		return nil, false, nil
	}
	if _, ok := svc.ChangeAt(in.Version); !ok {
		return nil, false, nil
	}
	who, err := settingsActor(ctx)
	if err != nil {
		return nil, false, nil
	}
	out, err := svc.Revert(ctx, who, in.Version, in.Note)
	return out, true, err
}
