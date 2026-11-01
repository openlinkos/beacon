package config

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestExample keeps config.example.yaml in step with the configuration: every key in it is known, and
// what it sets is valid (its secret files aside, which exist only in a deployment).
func TestExample(t *testing.T) {
	b, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c := &Config{}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		t.Fatal(err)
	}
	c.defaults()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if len(c.Agents) != 1 || c.Agents[0].Kind != "webhook" || c.Changes.Keep.D().Hours() != 720 {
		t.Errorf("agents %+v, changes %+v", c.Agents, c.Changes)
	}
}
