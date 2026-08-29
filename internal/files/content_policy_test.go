package files

import "testing"

func TestValidateContentTypeRejectsActiveContent(t *testing.T) {
	for _, mime := range []string{"text/html", "text/javascript", "application/javascript", "image/svg+xml", "application/x-msdownload"} {
		if err := ValidateContentType(mime, mime); err != ErrDisallowedType {
			t.Errorf("expected %s to be rejected, got %v", mime, err)
		}
	}
}

func TestValidateContentTypeAllowsGenericBinary(t *testing.T) {
	if err := ValidateContentType("application/pdf", "application/octet-stream"); err != nil {
		t.Fatalf("generic binary detection should remain supported: %v", err)
	}
}
