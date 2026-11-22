package discovery

import "testing"

// TENG-3921: a value `telara scan` took a credential out of is a placeholder,
// not an inline secret.
func TestQuarantinedPlaceholderIsNotAnInlineSecret(t *testing.T) {
	class, _ := ClassifyCredential(nil, map[string]string{"GOOGLE_REFRESH_TOKEN": QuarantinedCredentialPrefix + "0123456789abcdef"})
	if class == CredentialInline {
		t.Fatal("placeholder classified as an inline secret")
	}
	if class, _ := ClassifyCredential(nil, map[string]string{"GOOGLE_REFRESH_TOKEN": "1//0gRefreshTokenValue-abc123"}); class != CredentialInline {
		t.Fatalf("a real token is %v", class)
	}
}
