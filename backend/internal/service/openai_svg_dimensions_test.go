package service

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAISVGDimensions(t *testing.T) {
	for _, tc := range []struct{ name, svg, want string }{
		{"pixels", `<svg xmlns="http://www.w3.org/2000/svg" width="64px" height="32"/>`, "64x32"},
		{"viewbox", `<?xml version="1.0"?><svg viewBox="0 0 640 360"/>`, "640x360"},
		{"aspect-ratio", `<svg width="128" viewBox="0,0,64,32"/>`, "128x64"},
		{"fraction", `<svg width="64.5" height="32.2"/>`, "65x33"},
		{"wrong-root", `<html width="64" height="32"/>`, ""},
		{"wrong-namespace", `<svg xmlns="urn:other" width="64" height="32"/>`, ""},
		{"missing-size", `<svg/>`, ""},
		{"negative", `<svg width="-1" height="32"/>`, ""},
		{"non-finite", `<svg width="NaN" height="32"/>`, ""},
		{"overflow", `<svg width="1e100" height="32"/>`, ""},
		{"doctype", `<!DOCTYPE svg SYSTEM "file:///fixture"><svg width="64" height="32"/>`, ""},
		{"truncated", `<svg width="64" height="32"`, ""},
		{"header-budget", "<!--" + strings.Repeat(" ", int(maxOpenAIImageDimensionProbeBytes)) + `--><svg width="64" height="32"/>`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded := base64.StdEncoding.EncodeToString([]byte(tc.svg))
			require.Equal(t, tc.want, detectOpenAIImageResultSize("data:image/svg+xml;base64,"+encoded))
		})
	}
}
