package ipc

import "fmt"

// ValidateResponseKey keeps receipt lookup commands unambiguous and bounds
// storage per acceptance. An empty key is supported for legacy IPC callers.
func ValidateResponseKey(key string) error {
	if len(key) > 128 {
		return fmt.Errorf("idempotency key must be at most 128 bytes")
	}
	for _, c := range key {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			continue
		}
		return fmt.Errorf("idempotency key may contain only letters, digits, dot, underscore and hyphen")
	}
	return nil
}
