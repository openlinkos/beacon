package secrets

import (
	"sort"
	"strings"

	"encoding/json"

	"github.com/openlinkos/beacon/internal/kv"
)

// The describe action: what an agent sees of the items its entitlements reach — the reference,
// the title, the category, the tags, the URLs, the field names and types, the approval a request
// would need, and whether it already holds a grant. Nothing outside the entitlements is there at
// all: the design's default deny makes the rest not exist, in the search as everywhere.

// AgentSearchResult is one item as the agent's search shows it.
type AgentSearchResult struct {
	Ref        string   `json:"ref"`
	Collection string   `json:"collection"`
	Item       Item     `json:"item"`
	Fields     []Field  `json:"fields,omitempty"`
	Level      string   `json:"level"`   // the approval a request would need
	Actions    []string `json:"actions"` // what the agent may do with it
	HasGrant   bool     `json:"has_grant"`
}

// SearchForAgent looks for items the agent may describe: a word in the title, category or URLs,
// the tags narrowing, one collection when it names one. What the entitlements do not reach is not
// in the answer, and the trash and the archive stay out, as the people's search keeps them.
func (s *Service) SearchForAgent(agentName, query, collection string, tags []string) []AgentSearchResult {
	var out []AgentSearchResult
	if s.keys.sealed() {
		return out
	}
	agent, ok := s.agentInfoOf(agentName)
	if !ok {
		return out
	}
	q := strings.ToLower(strings.TrimSpace(query))
	now := s.now()
	var denies, allows []Entitlement
	for _, e := range s.Entitlements() {
		if !e.coversPrincipal(agent, now) {
			continue
		}
		if e.Deny {
			denies = append(denies, e)
		} else if e.allowsAction(ActDescribe) {
			allows = append(allows, e)
		}
	}
	if len(allows) == 0 {
		return out
	}
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil || !s.itemVerifies(r) || r.State != StateNormal {
				return true
			}
			col, ok := s.collectionByIDTx(tx, r.Collection)
			if !ok {
				return true
			}
			if collection != "" && col.Name != collection && col.ID != collection {
				return true
			}
			if len(tags) > 0 {
				for _, t := range tags {
					if !containsString(r.Tags, t) {
						return true
					}
				}
			}
			if q != "" {
				hay := strings.ToLower(r.Title + " " + r.Category + " " + strings.Join(r.URLs, " ") + " " + strings.Join(r.Tags, " "))
				if !strings.Contains(hay, q) {
					return true
				}
			}
			covered := false
			for _, e := range allows {
				if e.coversResource(r.Item, col) {
					covered = true
					break
				}
			}
			if !covered {
				return true
			}
			for _, e := range denies {
				if e.coversResource(r.Item, col) {
					return true // a deny matching keeps it out of sight
				}
			}
			res := AgentSearchResult{Ref: RefOfItem(col.Name, r.Title), Collection: col.Name, Item: r.Item,
				Level: s.EffectiveLevel(r.Item), HasGrant: s.holdsGrant(agentName, r.ID)}
			if ver, ok := getVersionTx(tx, r.ID, r.Version); ok {
				res.Fields = ver.Fields
			}
			pol := s.decidePolicy(agent, r.Item, col, ActRead, "")
			res.Actions = pol.Actions
			out = append(out, res)
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Item.Title < out[j].Item.Title })
	return out
}

// ScopePreview is what the owner sees before an agent is handed entitlements: how many items the
// agent could see, split by the approval each would need. Never items are invisible to the agent
// by definition; they are counted apart, for the owner's eye only, and not in the total.
type ScopePreview struct {
	Total  int `json:"total"`
	Auto   int `json:"auto"`
	Review int `json:"review"`
	Person int `json:"person"`
	Never  int `json:"never"`
}

// PreviewForAgent counts what the agent's present entitlements reach, by the approval each item
// would need (never counts too: those stay out of the agent's sight whatever covers them).
func (s *Service) PreviewForAgent(agentName string) ScopePreview {
	return s.preview(func(e Entitlement) bool {
		a, ok := s.agentInfoOf(agentName)
		return ok && e.coversPrincipal(a, s.now()) && !e.Deny && e.allowsAction(ActDescribe)
	})
}

// PreviewForTemplates counts what the template entitlements named would add to an agent's reach,
// for the onboarding page's "this is what it will see".
func (s *Service) PreviewForTemplates(names []string) ScopePreview {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return s.preview(func(e Entitlement) bool {
		return set[e.Name] && !e.Deny && e.allowsAction(ActDescribe)
	})
}

func (s *Service) preview(take func(Entitlement) bool) ScopePreview {
	var out ScopePreview
	if s.keys.sealed() {
		return out
	}
	var allows []Entitlement
	for _, e := range s.Entitlements() {
		if take(e) {
			allows = append(allows, e)
		}
	}
	if len(allows) == 0 {
		return out
	}
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil || !s.itemVerifies(r) || r.State != StateNormal {
				return true
			}
			col, ok := s.collectionByIDTx(tx, r.Collection)
			if !ok {
				return true
			}
			for _, e := range allows {
				if e.coversResource(r.Item, col) {
					switch s.EffectiveLevel(r.Item) {
					case LevelAuto:
						out.Total++
						out.Auto++
					case LevelReview:
						out.Total++
						out.Review++
					case LevelPerson:
						out.Total++
						out.Person++
					default:
						out.Never++ // not in the total: the agent never sees it
					}
					return true
				}
			}
			return true
		})
		return nil
	})
	return out
}

// EntitlementsOfAgent lists the entitlements that take in the agent, for the agent page.
func (s *Service) EntitlementsOfAgent(agentName string) []Entitlement {
	var out []Entitlement
	agent, ok := s.agentInfoOf(agentName)
	if !ok {
		return out
	}
	for _, e := range s.Entitlements() {
		if e.coversPrincipal(agent, s.now()) {
			out = append(out, e)
		}
	}
	return out
}

// TemplateEntitlements lists the entitlements an owner may hand a new agent.
func (s *Service) TemplateEntitlements() []Entitlement {
	var out []Entitlement
	for _, e := range s.Entitlements() {
		if e.Template && !e.Deny {
			out = append(out, e)
		}
	}
	return out
}

// ApplyTemplates hands an agent the template entitlements named (adding the agent to each one's
// principals), and takes it back out of the templates it held that are not named. A template the
// agent was alone in keeps its row — an entitlement with no principal is not a shape the checker
// takes — and shows in the console until someone clears it by hand.
func (s *Service) ApplyTemplates(who Actor, agent string, names []string) error {
	if s.keys.sealed() {
		return ErrSealed
	}
	keep := map[string]bool{}
	for _, n := range names {
		keep[n] = true
	}
	for _, e := range s.Entitlements() {
		if !e.Template {
			continue
		}
		held := containsString(e.Principals.Agents, agent)
		switch {
		case keep[e.Name] && !held:
			e.Principals.Agents = append(e.Principals.Agents, agent)
			if _, err := s.SetEntitlement(who, e); err != nil {
				return err
			}
		case !keep[e.Name] && held && len(e.Principals.Agents) > 1:
			var rest []string
			for _, a := range e.Principals.Agents {
				if a != agent {
					rest = append(rest, a)
				}
			}
			e.Principals.Agents = rest
			if _, err := s.SetEntitlement(who, e); err != nil {
				return err
			}
		}
	}
	return nil
}
