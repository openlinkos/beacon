package feishu

// Builders for card JSON 2.0 elements. They return plain maps, so a card can be inspected in tests and
// sent as JSON.

// Header colours (Feishu "template" values).
const (
	Red    = "red"
	Orange = "orange"
	Yellow = "yellow"
	Green  = "green"
	Blue   = "blue"
	Grey   = "grey"
)

// NewCard makes a card that every recipient sees the same way and that can be updated in place.
func NewCard(header map[string]any, elements ...map[string]any) Card {
	els := make([]any, 0, len(elements))
	for _, e := range elements {
		if e != nil {
			els = append(els, e)
		}
	}
	return Card{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true, "width_mode": "fill", "enable_forward": true},
		"header": header,
		"body":   map[string]any{"direction": "vertical", "elements": els},
	}
}

// Text is a plain text object.
func Text(s string) map[string]any { return map[string]any{"tag": "plain_text", "content": s} }

// Header is the card header; template is one of the colour constants.
func Header(title, subtitle, template string, tags ...map[string]any) map[string]any {
	h := map[string]any{"title": Text(title), "template": template}
	if subtitle != "" {
		h["subtitle"] = Text(subtitle)
	}
	if len(tags) > 0 {
		list := make([]any, len(tags))
		for i, t := range tags {
			list[i] = t
		}
		h["text_tag_list"] = list
	}
	return h
}

// Tag is a coloured label for the header.
func Tag(text, color string) map[string]any {
	return map[string]any{"tag": "text_tag", "text": Text(text), "color": color}
}

// Markdown is a block of card markdown (lark_md).
func Markdown(s string) map[string]any { return map[string]any{"tag": "markdown", "content": s} }

// Hr is a divider.
func Hr() map[string]any { return map[string]any{"tag": "hr"} }

// Button sends value back to the service when clicked. kind is default, primary_filled or danger.
func Button(label, kind string, value map[string]any) map[string]any {
	return map[string]any{
		"tag": "button", "text": Text(label), "type": kind, "size": "small",
		"behaviors": []any{map[string]any{"type": "callback", "value": value}},
	}
}

// LinkButton opens url.
func LinkButton(label, url string) map[string]any {
	return map[string]any{
		"tag": "button", "text": Text(label), "type": "default", "size": "small",
		"behaviors": []any{map[string]any{"type": "open_url", "default_url": url}},
	}
}

// Row lays elements out side by side, each as wide as it needs.
func Row(elements ...map[string]any) map[string]any {
	cols := make([]any, 0, len(elements))
	for _, e := range elements {
		if e == nil {
			continue
		}
		cols = append(cols, map[string]any{"tag": "column", "width": "auto", "elements": []any{e}})
	}
	if len(cols) == 0 {
		return nil
	}
	return map[string]any{"tag": "column_set", "horizontal_spacing": "8px", "columns": cols}
}

// NoteForm is a text input with a submit button; the input's value comes back as form value "note".
func NoteForm(name, placeholder, label string, value map[string]any) map[string]any {
	submit := Button(label, "default", value)
	submit["form_action_type"] = "submit"
	submit["name"] = name + "_submit"
	return map[string]any{
		"tag": "form", "name": name,
		"elements": []any{
			map[string]any{"tag": "input", "name": "note", "placeholder": Text(placeholder), "max_length": 200},
			submit,
		},
	}
}
