package tool

import (
	"errors"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

func TestWrapDBErrorPreservesCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("driver failed")
	err := WrapDBError(cause)
	if !errors.Is(err, ErrDatabase) || !errors.Is(err, cause) || errors.Unwrap(err) != cause {
		t.Fatalf("wrapped error = %v", err)
	}
	if WrapDBError(nil) != nil || WrapDBError(err) != err {
		t.Fatal("WrapDBError should preserve nil and existing wrappers")
	}
}

func TestArgsKeyNoCollision(t *testing.T) {
	t.Parallel()
	if argsKey([]any{"a b", "c"}) == argsKey([]any{"a", "b c"}) {
		t.Fatal("string-boundary collision in cache key")
	}
	if argsKey([]any{"1", 2}) == argsKey([]any{1, "2"}) {
		t.Fatal("string/number collision in cache key")
	}
}

func TestDecodeInputPreservesLargeInteger(t *testing.T) {
	var input readInput
	err := decodeInput(
		[]byte(`{"entity":"users","filter":[{"field":"id","op":"eq","value":9007199254740993}]}`),
		&input,
	)
	if err != nil {
		t.Fatal(err)
	}
	predicate, err := filterToPredicate(input.Filter)
	if err != nil {
		t.Fatal(err)
	}
	condition, ok := predicate.(relalg.Condition)
	if !ok || condition.Value != int64(9007199254740993) {
		t.Fatalf("condition = %#v", predicate)
	}
}
