package git

import (
	"testing"
)

func TestParseTokenFile(t *testing.T) {
	tokens, err := ParseTokenFile("testdata/tokens.yaml")
	if err != nil {
		t.Fatalf("unexpected error found: %s", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("unexpected %d entries found", len(tokens))
	}
	if tokens["github.com"] != "foo" {
		t.Fatalf("unexpected token for github.com: %s", tokens["github.com"])
	}
	if tokens["gitlab.com"] != "bar" {
		t.Fatalf("unexpected token for gitlab.com: %s", tokens["gitlab.com"])
	}
}

func TestParseEmptyFile(t *testing.T) {
	tokens, err := ParseTokenFile("testdata/empty.yaml")
	if err != nil {
		t.Fatalf("unexpected error found: %s", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("unexpected %d entries found", len(tokens))
	}
}
