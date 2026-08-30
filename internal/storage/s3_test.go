package storage

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
)

func TestOptionalCORSError(t *testing.T) {
	for _, code := range []string{"NotImplemented", "AccessDenied"} {
		if !optionalCORSError(&smithy.GenericAPIError{Code: code}) {
			t.Fatalf("expected %s to be optional", code)
		}
	}
	if optionalCORSError(errors.New("network failure")) {
		t.Fatal("expected non-API error to remain fatal")
	}
}
