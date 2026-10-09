package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionCmd(t *testing.T) {
	cmd := newRootCmd("1.2.3", "abc123", "2026-10-09")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"version"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, buf.String(), "1.2.3")
	assert.Contains(t, buf.String(), "abc123")
	assert.Contains(t, buf.String(), "2026-10-09")
}
