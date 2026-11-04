package daemon

import (
	"fmt"
	"strings"
)

func resultString(v any) string { return strings.TrimSpace(fmt.Sprint(v)) }
