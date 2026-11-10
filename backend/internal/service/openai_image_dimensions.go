package service

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strconv"
	"strings"
)

const maxOpenAIImageDimensionProbeBytes int64 = 1 << 20

func isValidOpenAIImageResult(encoded string) bool {
	return detectOpenAIImageResultSize(encoded) != ""
}

func detectOpenAIImageResultSize(encoded string) string {
	payload := strings.TrimSpace(encoded)
	if strings.HasPrefix(strings.ToLower(payload), "data:") {
		comma := strings.IndexByte(payload, ',')
		if comma < 0 || comma+1 >= len(payload) {
			return ""
		}
		payload = strings.TrimSpace(payload[comma+1:])
	}
	if payload == "" {
		return ""
	}

	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		decoded := base64.NewDecoder(encoding, strings.NewReader(payload))
		buffered := bufio.NewReader(io.LimitReader(decoded, maxOpenAIImageDimensionProbeBytes))
		prefix, _ := buffered.Peek(30)
		if width, height, ok := detectOpenAIWebPDimensions(prefix); ok {
			return fmt.Sprintf("%dx%d", width, height)
		}
		cfg, _, err := image.DecodeConfig(buffered)
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			svgReader := io.LimitReader(base64.NewDecoder(encoding, strings.NewReader(payload)), maxOpenAIImageDimensionProbeBytes)
			if width, height, ok := detectOpenAISVGDimensions(svgReader); ok {
				return fmt.Sprintf("%dx%d", width, height)
			}
			continue
		}
		return fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
	}
	return ""
}

// SVG is a native output of GPT Image 2.6. Read only its bounded XML header;
// image.DecodeConfig supports raster formats, so it cannot identify SVG.
func detectOpenAISVGDimensions(reader io.Reader) (int, int, bool) {
	decoder := xml.NewDecoder(reader)
	for {
		token, err := decoder.Token()
		if err != nil {
			return 0, 0, false
		}
		switch value := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return 0, 0, false
			}
		case xml.Directive:
			return 0, 0, false
		case xml.StartElement:
			if value.Name.Local != "svg" || (value.Name.Space != "" && value.Name.Space != "http://www.w3.org/2000/svg") {
				return 0, 0, false
			}
			var width, height, viewWidth, viewHeight float64
			for _, attr := range value.Attr {
				if attr.Name.Space != "" {
					continue
				}
				switch attr.Name.Local {
				case "width":
					width = openAISVGLength(attr.Value)
				case "height":
					height = openAISVGLength(attr.Value)
				case "viewBox":
					parts := strings.Fields(strings.ReplaceAll(attr.Value, ",", " "))
					if len(parts) == 4 {
						viewWidth, viewHeight = openAISVGLength(parts[2]), openAISVGLength(parts[3])
					}
				}
			}
			if viewWidth > 0 && viewHeight > 0 {
				if width <= 0 && height <= 0 {
					width, height = viewWidth, viewHeight
				} else if width <= 0 {
					width = height * viewWidth / viewHeight
				} else if height <= 0 {
					height = width * viewHeight / viewWidth
				}
			}
			// Bound conversion before turning floating-point SVG lengths into ints.
			limit := float64(int(^uint(0)>>1) / 2)
			if width <= 0 || height <= 0 || math.IsNaN(width) || math.IsNaN(height) || width > limit || height > limit {
				return 0, 0, false
			}
			return int(math.Ceil(width)), int(math.Ceil(height)), true
		}
	}
}

func openAISVGLength(value string) float64 {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "px"))
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	return n
}

func detectOpenAIWebPDimensions(header []byte) (int, int, bool) {
	if len(header) < 16 || string(header[:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		return 0, 0, false
	}

	switch string(header[12:16]) {
	case "VP8X":
		if len(header) < 30 {
			return 0, 0, false
		}
		width := 1 + int(header[24]) + int(header[25])<<8 + int(header[26])<<16
		height := 1 + int(header[27]) + int(header[28])<<8 + int(header[29])<<16
		return width, height, width > 0 && height > 0
	case "VP8 ":
		if len(header) < 30 || string(header[23:26]) != "\x9d\x01\x2a" {
			return 0, 0, false
		}
		width := int(binary.LittleEndian.Uint16(header[26:28]) & 0x3fff)
		height := int(binary.LittleEndian.Uint16(header[28:30]) & 0x3fff)
		return width, height, width > 0 && height > 0
	case "VP8L":
		if len(header) < 25 || header[20] != 0x2f {
			return 0, 0, false
		}
		width := 1 + int(header[21]) + int(header[22]&0x3f)<<8
		height := 1 + int(header[22]>>6) + int(header[23])<<2 + int(header[24]&0x0f)<<10
		return width, height, width > 0 && height > 0
	default:
		return 0, 0, false
	}
}

func reconcileOpenAIResponsesImageResultSizes(results []openAIResponsesImageResult, firstMeta *openAIResponsesImageResult) {
	for i := range results {
		// ChatGPT OAuth can normalize requested controls to "auto". The final
		// image bytes are authoritative for response metadata and tier billing.
		if actualSize := detectOpenAIImageResultSize(results[i].Result); actualSize != "" {
			results[i].Size = actualSize
		}
	}
	if firstMeta == nil || len(results) == 0 {
		return
	}
	if size := strings.TrimSpace(results[0].Size); size != "" {
		firstMeta.Size = size
	}
}
