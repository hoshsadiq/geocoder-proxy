package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionCmd(t *testing.T) {
	t.Run("prints build info", func(t *testing.T) {
		is, must := assert.New(t), require.New(t)
		cmd := newRootCmd("1.2.3", "abc123", "2026-10-09")
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetArgs([]string{"version"})

		must.NoError(cmd.Execute())
		is.Contains(buf.String(), "1.2.3")
		is.Contains(buf.String(), "abc123")
		is.Contains(buf.String(), "2026-10-09")
	})

	t.Run("rejects extra arguments", func(t *testing.T) {
		cmd := newRootCmd("1.2.3", "abc123", "2026-10-09")
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetArgs([]string{"version", "extra"})

		require.Error(t, cmd.Execute())
	})
}
