package deploy_test

import (
	"os"
	"strings"
	"testing"
)

func TestComposeLitestreamDataVolumeIsWritable(t *testing.T) {
	b, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "beacon-data:/var/lib/beacon:ro") {
		t.Fatal("the Litestream data volume is mounted read-only")
	}
}
