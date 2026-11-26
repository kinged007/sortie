//go:build !windows

package usagesource

// geminiCredentialManagerSecret has no Credential Manager to ask off Windows.
func geminiCredentialManagerSecret(string) string { return "" }
