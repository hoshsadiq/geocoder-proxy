package jsonnum

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInt64(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	is.Equal(int64(5568763423), Int64("5568763423"))
	is.Equal(int64(0), Int64(""))
	is.Equal(int64(0), Int64("abc"))
	is.Equal(int64(5), Int64("5.5"))
	is.Equal(int64(-42), Int64("-42"))

	var probe struct {
		N json.Number `json:"n"`
	}
	must.NoError(json.Unmarshal([]byte(`{"n":123}`), &probe))
	is.Equal(int64(123), Int64(probe.N))
	must.NoError(json.Unmarshal([]byte(`{"n":"123"}`), &probe))
	is.Equal(int64(123), Int64(probe.N))
}
