// Package schemas exposes the canonical clawctl JSON schemas as embedded
// bytes. The .json files in this directory are the single source of truth —
// they are also read directly by the bash wrapper. This Go package exists
// only so the typed binary can pull them in via go:embed without keeping a
// duplicate copy under internal/.
package schemas

import _ "embed"

//go:embed envelope.v1.json
var EnvelopeV1 []byte
