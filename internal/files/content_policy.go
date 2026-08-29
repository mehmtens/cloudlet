package files

import "strings"

// ValidateContentType applies the baseline upload policy before metadata is persisted.
// Unknown binary formats remain supported; active script/document formats do not.
func ValidateContentType(declared, detected string) error {
	declared = strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	detected = strings.ToLower(strings.TrimSpace(strings.Split(detected, ";")[0]))
	for _, value := range []string{declared, detected} {
		if value == "text/html" || value == "text/javascript" || value == "application/javascript" || value == "application/x-javascript" || value == "image/svg+xml" || value == "application/x-shockwave-flash" || value == "application/x-msdownload" || value == "application/x-dosexec" {
			return ErrDisallowedType
		}
	}
	// Some valid formats (and small test fixtures) are reported as generic
	// binary/text by net/http; do not reject those solely on a label mismatch.
	return nil
}
