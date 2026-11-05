package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"regexp"
	"slices"
	"strings"
)

// The icons the console draws, all from the design system's set (static/ol/icons.json, Lucide). Every
// page carries them as one hidden sprite of symbols, so that an icon is <use href="#i-name"> and nothing
// is fetched for it; TestIcons checks that each exists in the set and is plain shapes.
var iconNames = []string{
	"activity", "ban", "bot", "check", "chevron-left", "chevron-right", "chevrons-up-down", "circle-alert",
	"circle-check", "clock", "download", "file-cog", "history", "key-round", "layout-dashboard", "list-filter",
	"log-in", "log-out", "menu", "message-square", "pencil", "power", "rotate-ccw", "scroll-text", "search",
	"server", "settings", "shield-alert", "terminal", "timer", "triangle-alert", "x", "zap", "route",
	"users", "user", "user-plus", "user-x", "user-check", "plus", "trash-2", "link", "plug", "shield-check",
}

// iconShape is what a symbol may hold: the plain shapes Lucide draws with, and their geometry.
var iconShape = regexp.MustCompile(`^(\s*<(path|circle|rect|line|polyline|polygon|ellipse)(\s+[a-z][a-z0-9-]*="[-0-9a-zA-Z .,]*")*\s*/>)*\s*$`)

// sprite is the hidden <svg> with a <symbol> for each icon, made once from the vendored set.
var sprite = func() template.HTML {
	b, err := static.ReadFile("static/ol/icons.json")
	if err != nil {
		panic(err)
	}
	var set struct {
		ViewBox string            `json:"viewBox"`
		Icons   map[string]string `json:"icons"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		panic(err)
	}
	if !regexp.MustCompile(`^[0-9 ]+$`).MatchString(set.ViewBox) {
		panic("icons.json: an unexpected viewBox")
	}
	var sb strings.Builder
	sb.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" hidden aria-hidden="true">`)
	for _, name := range iconNames {
		shape, ok := set.Icons[name]
		if !ok || !iconShape.MatchString(shape) {
			panic(fmt.Sprintf("icons.json: icon %q is missing or not plain shapes", name))
		}
		fmt.Fprintf(&sb, `<symbol id="i-%s" viewBox="%s">%s</symbol>`, name, set.ViewBox, shape)
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String()) //nolint:gosec // G203: built from the vendored icon set, checked above to be plain shapes
}()

// icon draws an icon from the sprite; size is "", "sm" or "lg".
func icon(name, size string) (template.HTML, error) {
	if !slices.Contains(iconNames, name) {
		return "", fmt.Errorf("no icon %q in the console's set", name)
	}
	cls := "ol-icon"
	if size == "sm" || size == "lg" {
		cls += " ol-icon-" + size
	}
	return template.HTML(`<svg class="` + cls + `" aria-hidden="true"><use href="#i-` + name + `"/></svg>`), nil //nolint:gosec // G203: a known name and class
}

// funcs are the console templates' functions: icons, console (the path of a page of the console)
// and join (a list to a readable line).
var funcs = template.FuncMap{
	"icon":    func(name string) (template.HTML, error) { return icon(name, "") },
	"iconSm":  func(name string) (template.HTML, error) { return icon(name, "sm") },
	"console": consolePath,
	"join":    strings.Join,
}
