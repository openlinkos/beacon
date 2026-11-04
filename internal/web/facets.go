package web

import (
	"maps"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Lists with facets, as the design system lays them out (.ol-layout-filters): every filter is a link to
// the same list with one parameter changed, so the list needs no script and every state has a URL. A
// facet counts what choosing it would leave, under every other filter but its own. Lists show the
// newest first, 50 to a page.

const perPage = 50

// facet is one way to narrow a list of T: the query parameter, the heading, each item's value, and how
// a value reads (nil: as it is). Order, when set, fixes the order of the values; otherwise the most
// common come first.
type facet[T any] struct {
	Param, Title string
	Value        func(T) string
	Label        func(string) string
	Order        []string
}

type facetValue struct {
	Label, Href string
	Count       string
	On          bool
}

type facetView struct {
	Title, Clear string
	Values       []facetValue
	More         string
}

type filterTag struct{ Label, Href string }

type pager struct{ Summary, Prev, Next string }

// list is a page of a list with its facets.
type list struct {
	Path, Query, SearchLabel, SearchHint string
	Hidden                               []kv // the filters in force, kept when searching
	Facets                               []facetView
	Tags                                 []filterTag
	On                                   int // filters in force
	Pager                                pager
	Total                                string
	Empty                                string
}

// href is path with q, the page parameter dropped.
func href(path string, q url.Values) string {
	q = cloneValues(q)
	q.Del("page")
	if e := q.Encode(); e != "" {
		return path + "?" + e
	}
	return path
}

func cloneValues(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		if len(v) > 0 && v[0] != "" {
			out[k] = []string{v[0]}
		}
	}
	return out
}

// with is q with param set to value ("" removes it).
func with(q url.Values, param, value string) url.Values {
	q = cloneValues(q)
	if value == "" {
		q.Del(param)
	} else {
		q.Set(param, value)
	}
	return q
}

// filter narrows items by the query's facets and search, and lays out the facets, the tags of the
// filters in force and the page of the result; it returns the items of that page.
func filter[T any](s *Server, path string, q url.Values, items []T, facets []facet[T], match func(T, string) bool) ([]T, list) {
	q = cloneValues(q)
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	l := list{Path: path, Query: q.Get("q"), SearchLabel: s.t("Search"), Empty: s.t("Nothing matches.")}
	chosen := make([]string, len(facets))
	for i, f := range facets {
		chosen[i] = q.Get(f.Param)
	}
	passes := func(it T, skip int) bool {
		if search != "" && match != nil && !match(it, search) {
			return false
		}
		for i, f := range facets {
			if i != skip && chosen[i] != "" && f.Value(it) != chosen[i] {
				return false
			}
		}
		return true
	}
	var kept []T
	for _, it := range items {
		if passes(it, -1) {
			kept = append(kept, it)
		}
	}
	for i, f := range facets {
		counts := map[string]int{}
		for _, it := range items {
			if passes(it, i) {
				if v := f.Value(it); v != "" {
					counts[v]++
				}
			}
		}
		values := make([]string, 0, len(counts))
		for v := range counts {
			values = append(values, v)
		}
		if len(f.Order) > 0 {
			sort.SliceStable(values, func(a, b int) bool { return index(f.Order, values[a]) < index(f.Order, values[b]) })
		} else {
			sort.Slice(values, func(a, b int) bool {
				if counts[values[a]] != counts[values[b]] {
					return counts[values[a]] > counts[values[b]]
				}
				return values[a] < values[b]
			})
		}
		if chosen[i] != "" && counts[chosen[i]] == 0 {
			values = append([]string{chosen[i]}, values...)
		}
		label := f.Label
		if label == nil {
			label = func(v string) string { return v }
		}
		view := facetView{Title: f.Title}
		const shown = 8
		for j, v := range values {
			on := v == chosen[i]
			if j >= shown && !on {
				continue
			}
			next := v
			if on {
				next = ""
			}
			view.Values = append(view.Values, facetValue{Label: label(v), Href: href(path, with(q, f.Param, next)), Count: num(counts[v]), On: on})
		}
		if n := len(values) - len(view.Values); n > 0 {
			view.More = s.t("%d more", n)
		}
		if chosen[i] != "" {
			view.Clear = href(path, with(q, f.Param, ""))
			l.Tags = append(l.Tags, filterTag{Label: f.Title + ": " + label(chosen[i]), Href: view.Clear})
			l.On++
		}
		if len(view.Values) > 0 {
			l.Facets = append(l.Facets, view)
		}
	}
	if search != "" {
		l.Tags = append(l.Tags, filterTag{Label: s.t("Search: %s", q.Get("q")), Href: href(path, with(q, "q", ""))})
		l.On++
	}
	for _, k := range slices.Sorted(maps.Keys(q)) {
		if k != "q" && k != "page" {
			l.Hidden = append(l.Hidden, kv{k, q.Get(k)})
		}
	}
	_, from, to := s.page(len(kept), q, path, &l)
	l.Total = num(len(kept))
	return kept[from:to], l
}

func index(order []string, v string) int {
	if i := slices.Index(order, v); i >= 0 {
		return i
	}
	return len(order)
}

// page works out the page of n items the query asks for, and the pager under the list.
func (s *Server) page(n int, q url.Values, path string, l *list) (page, from, to int) {
	page, _ = strconv.Atoi(q.Get("page"))
	pages := max(1, (n+perPage-1)/perPage)
	page = min(max(page, 1), pages)
	from, to = (page-1)*perPage, min(page*perPage, n)
	if n == 0 {
		return 1, 0, 0
	}
	l.Pager.Summary = s.t("%s to %s of %s", num(from+1), num(to), num(n))
	if page > 1 {
		l.Pager.Prev = pageHref(path, q, page-1)
	}
	if page < pages {
		l.Pager.Next = pageHref(path, q, page+1)
	}
	return page, from, to
}

func pageHref(path string, q url.Values, page int) string {
	q = cloneValues(q)
	q.Del("page")
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if e := q.Encode(); e != "" {
		return path + "?" + e
	}
	return path
}

// contains reports whether any of the texts holds the search, already in lower case.
func contains(search string, texts ...string) bool {
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), search) {
			return true
		}
	}
	return false
}
