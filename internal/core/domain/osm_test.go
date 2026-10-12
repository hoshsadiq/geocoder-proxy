package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalOsmType(t *testing.T) {
	is := assert.New(t)
	for raw, want := range map[string]string{
		"node": "N", "way": "W", "relation": "R",
		"N": "N", "W": "W", "R": "R",
		"": "", "bogus": "",
	} {
		is.Equal(want, CanonicalOsmType(raw), "raw=%q", raw)
	}
}
