package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
)

type openAIImageOutputCounter struct {
	seen         map[string]struct{}
	seenSizes    map[string]string
	seenOrder    []string
	dataSizes    []string
	count        int
	maxDataCount int
	// requireValidImageData is enabled only for native Codex Images.  Generic
	// Responses fixtures and third-party providers may carry opaque result
	// strings, but a native image response must not become billable merely
	// because b64_json is non-empty.
	requireValidImageData bool
}

func newOpenAIImageOutputCounter() *openAIImageOutputCounter {
	return &openAIImageOutputCounter{
		seen:      make(map[string]struct{}),
		seenSizes: make(map[string]string),
	}
}

func (c *openAIImageOutputCounter) Count() int {
	if c == nil {
		return 0
	}
	if c.maxDataCount > c.count {
		return c.maxDataCount
	}
	return c.count
}

func (c *openAIImageOutputCounter) Sizes() []string {
	if c == nil {
		return nil
	}
	sizes := make([]string, 0, len(c.seenOrder)+len(c.dataSizes))
	for _, key := range c.seenOrder {
		if size := strings.TrimSpace(c.seenSizes[key]); size != "" {
			sizes = append(sizes, size)
		}
	}
	if len(sizes) == 0 && len(c.dataSizes) > 0 {
		sizes = append(sizes, c.dataSizes...)
	}
	if len(sizes) == 0 {
		return nil
	}
	return sizes
}

func (c *openAIImageOutputCounter) AddJSONResponse(body []byte) {
	if c == nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return
	}
	c.addDataArray(gjson.GetBytes(body, "data"))
	c.addOutputArray(gjson.GetBytes(body, "output"))
	c.addOutputArray(gjson.GetBytes(body, "response.output"))
}

func (c *openAIImageOutputCounter) AddSSEData(data []byte) {
	c.addResponsesEvent(data, nil)
}

func (c *openAIImageOutputCounter) addResponsesEvent(data []byte, searches *openAINativeSearchCounter) {
	c.observeResponsesEvent(data, searches, "", false)
}

// A WS client already decoded the envelope. Use its event type and the
// validated item used for image/search accounting to classify the same frame.
func (c *openAIImageOutputCounter) observeResponsesEvent(data []byte, searches *openAINativeSearchCounter, eventType string, inspectContent bool) bool {
	if c == nil || len(data) == 0 || strings.TrimSpace(string(data)) == "[DONE]" || !gjson.ValidBytes(data) {
		return false
	}
	return c.observeParsedResponsesEvent(gjson.ParseBytes(data), data, searches, eventType, inspectContent)
}

func (c *openAIImageOutputCounter) observeParsedResponsesEvent(root gjson.Result, data []byte, searches *openAINativeSearchCounter, eventType string, inspectContent bool) bool {
	wrapped := root.Get("data")
	c.addDataArray(wrapped)
	if eventType == "" {
		eventType = strings.TrimSpace(root.Get("type").String())
	}
	var clientContent bool
	switch eventType {
	case "response.output_item.added":
		if searches != nil && !searches.seenNative && mayContainNativeSearch(data) {
			searches.seenNative = root.Get("item.type").Str == "web_search_call"
		}
	case "response.output_item.done":
		item := root.Get("item")
		if inspectContent {
			clientContent = openAIOutputItemHasContent(rawJSONView(item.Raw))
		}
		if c.addImageOutputItem(item) && searches != nil {
			searches.observeItem(item, root.Get("output_index"), -1, true)
		}
	case "response.completed", "response.done":
		c.addResponseOutput(root.Get("response.output"), searches, root, "response.")
		if inspectContent && c.Count() == 0 {
			clientContent = openAIResponseObjectHasContent(rawJSONView(root.Get("response").Raw))
		}
		if searches != nil {
			if mayContainNativeSearch(data) {
				searches.observeOutput(root)
				searches.observeDataOutput(wrapped)
			}
			searches.observeTerminalReport(root)
		}
	case "image_generation.completed", "image_edit.completed":
		if item := root.Get("item"); item.Exists() {
			c.addImageOutputItem(item)
			return false
		}
		if output := root.Get("output"); output.Exists() {
			c.addImageOutputItem(output)
			return false
		}
		c.addImageOutputItem(root)
	default:
		if inspectContent {
			clientContent = openAIWSValidatedFrameHasContent(eventType, root.Raw)
		}
	}
	return clientContent
}

func (c *openAIImageOutputCounter) AddSSEBody(body string) {
	if c == nil || strings.TrimSpace(body) == "" {
		return
	}
	forEachOpenAISSEDataPayload(body, c.AddSSEData)
}

func (c *openAIImageOutputCounter) addDataArray(data gjson.Result) {
	if !data.IsArray() {
		return
	}
	items := data.Array()
	imageCount := 0
	sizes := make([]string, 0, len(items))
	for _, item := range items {
		if !item.IsObject() {
			continue
		}
		url := strings.TrimSpace(item.Get("url").String())
		b64 := strings.TrimSpace(item.Get("b64_json").String())
		hasImageOutput := url != "" || b64 != ""
		if c.requireValidImageData && b64 != "" && !isValidOpenAIImageResult(b64) {
			hasImageOutput = false
		}
		if !hasImageOutput {
			continue
		}
		imageCount++
		if size := strings.TrimSpace(item.Get("size").String()); size != "" {
			sizes = append(sizes, size)
		}
	}
	if imageCount > c.maxDataCount {
		c.maxDataCount = imageCount
	}
	if len(sizes) > 0 {
		c.dataSizes = sizes
	}
}

func (c *openAIImageOutputCounter) addOutputArray(output gjson.Result) {
	c.addResponseOutput(output, nil, gjson.Result{}, "")
}

func (c *openAIImageOutputCounter) addResponseOutput(output gjson.Result, searches *openAINativeSearchCounter, root gjson.Result, prefix string) {
	if !output.IsArray() {
		return
	}
	output.ForEach(func(index, item gjson.Result) bool {
		if c.addImageOutputItem(item) && searches != nil {
			status := root.Get(prefix + "status")
			e := root.Get(prefix + "error")
			if (!status.Exists() || status.Str == "completed") && e.Type == gjson.Null {
				searches.observeItem(item, gjson.Result{}, int(index.Int()), false)
			}
		}
		return true
	})
}

func (c *openAIImageOutputCounter) addImageOutputItem(item gjson.Result) bool {
	if !item.Exists() || !item.IsObject() {
		return false
	}
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType == "web_search_call" {
		return true
	}
	if itemType != "" && itemType != "image_generation_call" && itemType != "image_generation.completed" && itemType != "image_edit.completed" {
		return false
	}
	if strings.Contains(strings.ToLower(item.Raw), "partial_image") {
		return false
	}
	result := strings.TrimSpace(item.Get("result").String())
	resultIsEncoded := result != ""
	if result == "" {
		result = strings.TrimSpace(item.Get("b64_json").String())
		resultIsEncoded = result != ""
	}
	if result == "" {
		result = strings.TrimSpace(item.Get("url").String())
		resultIsEncoded = false
	}
	if result == "" {
		return false
	}
	if c.requireValidImageData && resultIsEncoded && !isValidOpenAIImageResult(result) {
		return false
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		key = hashOpenAIImageOutputResult(result)
	}
	if key == "" {
		return false
	}
	size := strings.TrimSpace(item.Get("size").String())
	if _, exists := c.seen[key]; exists {
		if size != "" && strings.TrimSpace(c.seenSizes[key]) == "" {
			c.seenSizes[key] = size
		}
		return false
	}
	if c.seen == nil {
		c.seen = make(map[string]struct{})
		c.seenSizes = make(map[string]string)
	}
	c.seen[key] = struct{}{}
	c.seenOrder = append(c.seenOrder, key)
	if size != "" {
		c.seenSizes[key] = size
	}
	c.count++
	return false
}

func hashOpenAIImageOutputResult(result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(result))
	return hex.EncodeToString(sum[:])
}

func countOpenAIResponseImageOutputsFromJSONBytes(body []byte) int {
	counter := newOpenAIImageOutputCounter()
	counter.AddJSONResponse(body)
	return counter.Count()
}

func collectOpenAIResponseImageOutputSizesFromJSONBytes(body []byte) []string {
	counter := newOpenAIImageOutputCounter()
	counter.AddJSONResponse(body)
	return counter.Sizes()
}

func countOpenAIImageOutputsFromSSEBody(body string) int {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(body)
	return counter.Count()
}

func collectOpenAIImageOutputSizesFromSSEBody(body string) []string {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(body)
	return counter.Sizes()
}

func responseImageAndSearchCounts(body []byte) (int, int) {
	counter := newOpenAIImageOutputCounter()
	var searches openAINativeSearchCounter
	if gjson.ValidBytes(body) {
		root := gjson.Parse(openAIWSPayloadStringView(body))
		wrapped := root.Get("data")
		counter.addDataArray(wrapped)
		counter.addResponseOutput(root.Get("output"), &searches, root, "")
		counter.addResponseOutput(root.Get("response.output"), &searches, root, "response.")
		if wrapped.IsObject() && mayContainNativeSearch(body) {
			searches.observeDataOutput(wrapped)
		}
		searches.observeTerminalReport(root)
	}
	return counter.Count(), searches.count()
}

func bufferedImageAndSearchCounts(body string, priced bool) (int, int) {
	if !priced {
		return countOpenAIImageOutputsFromSSEBody(body), 0
	}
	counter := newOpenAIImageOutputCounter()
	var searches openAINativeSearchCounter
	forEachOpenAISSEDataPayload(body, func(data []byte) { counter.addResponsesEvent(data, &searches) })
	return counter.Count(), searches.count()
}
