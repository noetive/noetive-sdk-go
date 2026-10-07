package bud_test

import (
	"os"
	"strings"
	"testing"

	"go.noetive.io/noetive-sdk-go/bud"
)

// goldenPath is the service's own description of its shapes, vendored. A
// maintainer refreshes it from the service; see contract.go.
const goldenPath = "testdata/contract/contract.golden"

// TestContractMatchesTheServer is the reason this package can be hand-written.
//
// Two repositories declare the same eighteen shapes and cannot import each other.
// This is the check that keeps them the same: the server emits a description of
// its own encoding, and this compares that description against one built by
// reflection over the structs in this package.
//
// A field renamed, retyped, re-tagged or dropped on either side fails here. What
// fails *without* this is a field that silently stops arriving in every program
// built on this SDK, discovered in production and attributed to anything but a
// wire change.
func TestContractMatchesTheServer(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading %s: %v\n\t"+
			"vendor the service's contract description there; see contract.go",
			goldenPath, err)
	}
	got := bud.ContractShape()

	if string(got) == string(want) {
		return
	}

	// Show the first line that differs rather than two hundred that do not.
	wantLines := strings.Split(string(want), "\n")
	gotLines := strings.Split(string(got), "\n")
	for i := range max(len(wantLines), len(gotLines)) {
		w, g := at(wantLines, i), at(gotLines, i)
		if w == g {
			continue
		}
		t.Fatalf("the wire shape differs from the server's at line %d.\n\n"+
			"  server: %q\n"+
			"  sdk:    %q\n\n"+
			"Decide which side is right before changing either. If the server moved, refresh\n"+
			"the golden and match it here. If this package is wrong, fix it — a client that\n"+
			"encodes a field the server does not read loses it silently.\n\n"+
			"  Refresh the golden from the service's contract description; see contract.go.",
			i+1, w, g)
	}
}

func at(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(absent)"
}

// TestTheVendoredContractIsTheGenerationThisPackageImplements catches the other
// way the two can disagree.
//
// The shapes can match line for line while the generation does not: a client built
// against bud/v1 talking to a server that has moved to bud/v2 would pass the
// comparison above right up to the first field v2 added.
func TestTheVendoredContractIsTheGenerationThisPackageImplements(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading %s: %v", goldenPath, err)
	}
	first, _, _ := strings.Cut(string(raw), "\n")
	if first != bud.ContractVersion {
		t.Errorf("the vendored contract is %q and this package implements %q", first, bud.ContractVersion)
	}
}

// TestTheContractDescribesTheWireNotTheGoTypes pins the three translations that
// exist because Go's types and the wire's disagree.
//
// Each one was a real bug before it was a rule. An identifier is a struct in Go
// and a string on the wire. A raw JSON body is a byte slice in Go and an object on
// the wire. And an identifier field must never be omitempty, because the server's
// zero identifier encodes as "" — so omitting the field produces a document the
// server never sends.
//
// A description that got any of these wrong would certify agreement between two
// sides that do not agree, which is worse than having no check.
func TestTheContractDescribesTheWireNotTheGoTypes(t *testing.T) {
	t.Parallel()

	shape := string(bud.ContractShape())

	for _, want := range []string{
		"  mailbox string",  // an identifier is a string
		"  body json",       // a raw body is an object, not bytes
		"  current json",    // the object a conflict carries
		"  cursor string\n", // required on the way out, refusal or not
	} {
		if !strings.Contains(shape, want) {
			t.Errorf("the shape does not describe %q\n%s", want, shape)
		}
	}

	// The identifier fields that must not be omitempty. Checked here rather than
	// generically because the shape renders an identifier and a plain string
	// identically — correctly, since they are the same bytes — so only the fields
	// known to be identifiers can be asserted about.
	for _, forbidden := range []string{
		"  mailbox string,omitempty\n  timeout_s", // WaitInput.Mailbox
	} {
		if strings.Contains(shape, forbidden) {
			t.Errorf("the shape contains %q; an identifier is never omitempty", forbidden)
		}
	}
}
