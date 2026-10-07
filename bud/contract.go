package bud

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sort"
	"strings"
)

// The contract: these types' encoding, described so it can be compared with the
// server's.
//
// # The problem this solves
//
// The service declares the same eighteen shapes in its own code, which is not
// published as a Go module, so this package cannot import it, and two
// hand-written definitions of one contract drift. The drift is the bad kind: both sides compile, both
// encode, and the difference surfaces as a field that silently stopped arriving —
// in production, months later, in somebody else's program.
//
// So the *encoding* is compared rather than the types. Both sides walk their own
// structs by reflection and write down, per exported field, the JSON name, the
// wire kind and whether it is omitempty. The service's description is vendored
// under testdata, and this package fails its own build when the two disagree.
//
// # Why vendored rather than fetched
//
// A check that reaches the network passes when the network is down, and a check
// that reads a sibling checkout passes only on the machine that has one.
// Refreshing it is a copy a maintainer makes from the service's own description,
// and a change a reviewer sees in the diff.
//
// # What it deliberately ignores
//
// Doc comments, Go type names, field order, and anything unexported. None of them
// cross the wire. Including them would make the hash move for edits that cannot
// break a client, which is how a drift check stops being read.

// ContractVersion is the shape this package implements.
//
// Compared against the first line of the vendored description, so a client that
// has been built against the wrong generation of the server fails at test time
// rather than at the first field that is missing.
const ContractVersion = "bud/v1"

// contractTypes are the envelope types, in the order the server lists them.
//
// Listed rather than discovered, and in the server's order, because the
// description is compared line for line: a set assembled differently would differ
// for a reason that is not a contract change.
func contractTypes() []struct {
	Name string
	Zero any
} {
	return []struct {
		Name string
		Zero any
	}{
		{"WaitInput", WaitInput{}},
		{"WaitOutput", WaitOutput{}},
		{"ReadInput", ReadInput{}},
		{"ReadOutput", ReadOutput{}},
		{"PutInput", PutInput{}},
		{"PutOutput", PutOutput{}},
		{"SendInput", SendInput{}},
		{"SendOutput", SendOutput{}},
		{"Error", Error{}},
		{"Provenance", Provenance{}},
		{"Effect", Effect{}},
		{"JournalEvent", JournalEvent{}},
		{"EventData", EventData{}},
		{"Actor", Actor{}},
		{"Me", Me{}},
		{"Listing", Listing{}},
		{"Conversation", Conversation{}},
		{"Summary", Summary{}},
	}
}

// ContractShape describes every envelope type's encoding, one field per line.
//
// Plain text rather than JSON, because a diff in a review has to be legible
// without tooling — and this is read by a person deciding whether a wire change
// was intended at least as often as by a test.
func ContractShape() []byte {
	var b strings.Builder
	b.WriteString(ContractVersion)
	b.WriteByte('\n')

	for _, t := range contractTypes() {
		b.WriteString(t.Name)
		b.WriteByte('\n')
		for _, line := range fieldsOf(reflect.TypeOf(t.Zero)) {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

// ContractHash identifies a shape by its content.
func ContractHash() string {
	sum := sha256.Sum256(ContractShape())
	return hex.EncodeToString(sum[:])[:16]
}

// fieldsOf describes one struct's exported fields, sorted by JSON name.
//
// Sorted rather than in declaration order: a JSON object's keys are unordered and
// both decoders accept them in any order, so moving a field within a struct is not
// a wire change and must not read as one.
func fieldsOf(t reflect.Type) []string {
	if t.Kind() != reflect.Struct {
		panic("bud: contract type " + t.String() + " is not a struct")
	}

	out := make([]string, 0, t.NumField())
	// Indexed rather than the Fields iterator: this module supports older Go
	// releases, and a public SDK should not raise its floor to borrow a loop.
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, omit := jsonName(f)
		if name == "-" {
			continue
		}
		line := name + " " + wireKind(f.Type)
		if omit {
			line += ",omitempty"
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

func jsonName(f reflect.StructField) (name string, omitempty bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return f.Name, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = f.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty
}

// wireKind names a type the way the wire sees it, not the way Go does.
//
// The two that matter: an identifier is a string even where the server declares a
// struct for it, because MarshalText makes it one; and a raw JSON field is "json"
// rather than an array of numbers, which is what reflection would otherwise call a
// byte slice. Those are precisely the two the server's own schemas had to work
// around, and describing them wrongly here would certify agreement between two
// sides that do not agree.
func wireKind(t reflect.Type) string {
	switch {
	case t.Kind() == reflect.Pointer:
		return wireKind(t.Elem())

	case t.Name() == "RawMessage":
		return "json"

	case implementsTextMarshaler(t):
		return "string"

	case t.Kind() == reflect.Slice, t.Kind() == reflect.Array:
		return "[]" + wireKind(t.Elem())

	case t.Kind() == reflect.Map:
		return "map[" + wireKind(t.Key()) + "]" + wireKind(t.Elem())

	case t.Kind() == reflect.Struct:
		return "object"

	case t.Kind() == reflect.String:
		return "string"

	case t.Kind() == reflect.Bool:
		return "bool"

	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Float64:
		return "number"

	default:
		return t.Kind().String()
	}
}

// implementsTextMarshaler reports whether a value of this type encodes as a string.
//
// The pointer covers both cases: a method set on *T includes everything declared
// on T, so asking about T as well would be a second question with one answer.
func implementsTextMarshaler(t reflect.Type) bool {
	type textMarshaler interface{ MarshalText() ([]byte, error) }
	return reflect.PointerTo(t).Implements(reflect.TypeFor[textMarshaler]())
}
