package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeAPIBaseURL(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "https://api.example.com", NormalizeAPIBaseURL("  https://api.example.com/  "))
	assert.Equal(t, "https://api.example.com", NormalizeAPIBaseURL("https://https://api.example.com"))
	assert.Equal(t, "https://api.example.com", NormalizeAPIBaseURL("http://https://api.example.com"))
	assert.Equal(t, "http://api.example.com", NormalizeAPIBaseURL("https://http://api.example.com"))
}
