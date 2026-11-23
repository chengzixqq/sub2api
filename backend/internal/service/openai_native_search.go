package service

import (
	"bytes"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Count completed output items within one upstream attempt. A terminal array
// can restate item.done evidence. Use the ID, then the output position; done
// events lacking both only establish a lower bound on the turn's total.
type openAINativeSearchCounter struct {
	all, completedItems nativeSearchIdentities
	unindexedDone       int
	reported            int
	seenNative          bool
	hasReport           bool
}

// Positions with no ID can corroborate earlier named calls for which an
// item.done supplied no position. Distinct named calls remain distinct.
type nativeSearchIdentities struct {
	ids                            map[string]*nativeSearchCall
	positions                      map[int]bool // true once an identified call occupies this position
	named, unpositioned, anonymous int
}
type nativeSearchCall struct{ positioned bool }

func openAINativeSearchPriced(c *gin.Context, account *Account) bool {
	if c == nil || account == nil || !account.IsOpenAI() {
		return false
	}
	p := groupSearchPricePer1kFromAPIKey(getAPIKeyFromContext(c))
	return p != nil && *p > 0
}

func (h *OpenAIWSIngressHooks) nativeSearchPriced(c *gin.Context, account *Account, turn int) bool {
	if h == nil || h.NativeSearchPrice == nil {
		return openAINativeSearchPriced(c, account)
	}
	if account == nil || !account.IsOpenAI() {
		return false
	}
	price := h.NativeSearchPrice(turn)
	return price != nil && *price > 0
}

func (s *nativeSearchIdentities) add(id, callID string, pos int) {
	if s.positions == nil {
		s.positions = make(map[int]bool)
		s.ids = make(map[string]*nativeSearchCall)
	}
	positionNamed, seenPosition := s.positions[pos]
	if id == "" && callID == "" {
		if pos >= 0 && !seenPosition {
			s.positions[pos] = false
			s.anonymous++
		}
		return
	}
	call := s.ids[id]
	if call == nil {
		call = s.ids[callID]
	}
	if call == nil {
		call = &nativeSearchCall{}
		s.named++
		s.unpositioned++
	}
	if id != "" && s.ids[id] == nil {
		s.ids[strings.Clone(id)] = call
	}
	if callID != "" && s.ids[callID] == nil {
		s.ids[strings.Clone(callID)] = call
	}
	if pos >= 0 {
		if !call.positioned {
			call.positioned = true
			s.unpositioned--
		}
		if seenPosition && !positionNamed {
			s.anonymous--
		}
		s.positions[pos] = true
	}
}

func (s *openAINativeSearchCounter) count() int {
	if s.hasReport {
		return s.reported
	}
	if s.all.ids == nil {
		return s.unindexedDone
	}
	done := AccumulateSearchCount(s.completedItems.named, s.completedItems.anonymous, "native_search_done")
	done = AccumulateSearchCount(done, s.unindexedDone, "native_search_done")
	identified := AccumulateSearchCount(s.all.named, max(s.all.anonymous-s.all.unpositioned, 0), "native_search")
	return max(done, identified)
}

func (s *openAINativeSearchCounter) observeItem(item gjson.Result, position gjson.Result, index int, done bool) {
	if item.Get("type").Str != "web_search_call" {
		return
	}
	s.seenNative = true
	if v := item.Get("status"); v.Exists() && v.Str != "completed" {
		return
	}
	switch item.Get("action.type").Str {
	case "open_page", "find_in_page", "find":
		return
	}
	if done {
		if n, ok := boundedJSONNonNegativeInt(position); ok {
			index = n
		}
	}
	id, callID := item.Get("id").Str, item.Get("call_id").Str
	if done && index < 0 && id == "" && callID == "" {
		s.unindexedDone = AccumulateSearchCount(s.unindexedDone, 1, "native_search_done")
		return
	}
	s.all.add(id, callID, index)
	if done {
		s.completedItems.add(id, callID, index)
	}
}

func (s *openAINativeSearchCounter) observeOutput(response gjson.Result) {
	if status := response.Get("status"); status.Exists() && status.Str != "completed" {
		return
	}
	if e := response.Get("error"); e.Exists() && e.Type != gjson.Null {
		return
	}
	output := response.Get("output")
	if !output.IsArray() {
		return
	}
	output.ForEach(func(index, item gjson.Result) bool {
		s.observeItem(item, gjson.Result{}, int(index.Int()), false)
		return true
	})
}

// Only a successful terminal with actual native item evidence can set the
// authoritative billable count. Zero is a meaningful reported value.
func (s *openAINativeSearchCounter) observeTerminalReport(root gjson.Result) {
	if !s.seenNative {
		return
	}
	// An explicit failure at any enclosing response layer invalidates a
	// terminal discount, even when the count itself sits outside that layer.
	nodes := [...]gjson.Result{root, root.Get("response"), root.Get("data"), root.Get("data.response")}
	for _, node := range nodes {
		if !node.IsObject() {
			continue
		}
		switch node.Get("type").Str {
		case "", "response", "response.completed", "response.done":
		default:
			return
		}
		if !nativeSearchResponseSucceeded(node) {
			return
		}
	}
	// Follow the same envelope order as extractOpenAIUsageFromJSONBytes.
	for _, node := range nodes {
		if n, ok := boundedJSONNonNegativeInt(node.Get("tool_usage.web_search.num_requests")); ok {
			s.reported, s.hasReport = n, true
			return
		}
	}
}

func nativeSearchResponseSucceeded(node gjson.Result) bool {
	status := node.Get("status")
	return (!status.Exists() || status.Str == "completed") && node.Get("error").Type == gjson.Null
}

func (s *openAINativeSearchCounter) observeDataOutput(data gjson.Result) {
	if data.IsObject() && nativeSearchResponseSucceeded(data) {
		s.observeOutput(data)
		if response := data.Get("response"); response.IsObject() {
			s.observeOutput(response)
		}
	}
}

func mayContainDataWrapper(payload string) bool {
	for rest := payload; ; {
		_, suffix, found := strings.Cut(rest, `"data"`)
		if !found {
			break
		}
		if strings.HasPrefix(strings.TrimLeft(suffix, " \t\r\n"), ":") {
			return true
		}
		rest = suffix
	}
	// Match the prefix up to the first escaped letter of a JSON data key.
	// Escaped characters in ordinary output text do not require a raw scan.
	return strings.Contains(payload, `\u`) && (strings.Contains(payload, `"\u0064`) ||
		strings.Contains(payload, `"d\u0061`) || strings.Contains(payload, `"da\u0074`) ||
		strings.Contains(payload, `"dat\u0061`))
}

// A JSON escape can spell any part of the protocol type name.
func mayContainNativeSearch(payload []byte) bool {
	return bytes.Contains(payload, []byte("web_search_call")) || bytes.Contains(payload, []byte(`\u`))
}

// Check the item discriminator before allocating passthrough turn state.
func openAIFrameHasNativeSearchItem(payload []byte, typ string) bool {
	if !gjson.ValidBytes(payload) {
		return false
	}
	root := gjson.Parse(openAIWSPayloadStringView(payload))
	if typ == "response.output_item.added" || typ == "response.output_item.done" {
		return root.Get("item.type").Str == "web_search_call"
	}
	for _, path := range [...]string{"output", "response.output", "data.output", "data.response.output"} {
		if path == "data.output" && !mayContainDataWrapper(openAIWSPayloadStringView(payload)) {
			break
		}
		output := root.Get(path)
		if !output.IsArray() {
			continue
		}
		found := false
		output.ForEach(func(_, item gjson.Result) bool {
			found = item.Get("type").Str == "web_search_call"
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func (s *openAINativeSearchCounter) observeRaw(payload []byte, typ string) {
	terminal := typ == "response.done" || typ == "response.completed"
	if (!terminal || !s.seenNative) && !mayContainNativeSearch(payload) {
		return
	}
	if !gjson.ValidBytes(payload) {
		return
	}
	switch typ {
	case "response.output_item.added":
		s.seenNative = s.seenNative || gjson.GetBytes(payload, "item.type").Str == "web_search_call"
	case "response.output_item.done":
		s.observeItem(gjson.GetBytes(payload, "item"), gjson.GetBytes(payload, "output_index"), -1, true)
	case "response.completed", "response.done", "":
		s.observeTerminal(openAIWSPayloadStringView(payload))
	}
}

// The caller has validated or decoded the event. Inspect its original string
// so false positives in ordinary output never require a copy of the frame.
func (s *openAINativeSearchCounter) observeTerminal(payload string) {
	root := gjson.Parse(payload)
	s.observeOutput(root)
	if response := root.Get("response"); response.IsObject() {
		s.observeOutput(response)
	}
	if mayContainDataWrapper(payload) {
		s.observeDataOutput(root.Get("data"))
	}
	s.observeTerminalReport(root)
}

// Bridge decoders already materialize the output items. Consult the raw index
// only for a search completion, where an omitted index differs from zero.
func (s *openAINativeSearchCounter) observeConverted(payload string, event *apicompat.ResponsesStreamEvent) {
	switch event.Type {
	case "response.output_item.added":
		s.seenNative = s.seenNative || (event.Item != nil && event.Item.Type == "web_search_call")
	case "response.output_item.done":
		if event.Item != nil && event.Item.Type == "web_search_call" {
			s.observeRaw([]byte(payload), event.Type)
		}
	case "response.done", "response.completed":
		if s.seenNative {
			s.observeTerminal(payload)
			return
		}
		if event.Response != nil {
			for _, item := range event.Response.Output {
				if item.Type == "web_search_call" {
					s.observeTerminal(payload)
					return
				}
			}
		}
		// Canonical output was already decoded above. Only compatible envelopes
		// need the raw scanner. Keep the string intact on the ordinary path.
		if (event.Response == nil || mayContainDataWrapper(payload)) &&
			(strings.Contains(payload, "web_search_call") || strings.Contains(payload, `\u`)) {
			s.observeTerminal(payload)
		}
	}
}

func countOpenAINativeSearchJSON(body []byte) int {
	var s openAINativeSearchCounter
	s.observeRaw(body, "")
	return s.count()
}

func countOpenAINativeSearchSSE(body string) int {
	return countBufferedOpenAISearch(body, false)
}

func countBufferedOpenAISearch(body string, truncated bool) int {
	var s openAINativeSearchCounter
	visitOpenAISSEDataPayloads(body, truncated, func(data []byte) {
		if s.seenNative || mayContainNativeSearch(data) {
			s.observeRaw(data, strings.TrimSpace(gjson.GetBytes(data, "type").Str))
		}
	})
	return s.count()
}
