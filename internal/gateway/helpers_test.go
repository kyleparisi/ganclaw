package gateway

import (
	"io"
	"strings"
)

func jsonBody(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }
