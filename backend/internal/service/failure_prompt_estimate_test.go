package service

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateFailurePromptTokens(t *testing.T) {
	t.Run("gemini body uses gemini estimator", func(t *testing.T) {
		body := []byte(`{"contents":[{"parts":[{"text":"hello world"}]}]}`)
		assert.Positive(t, EstimateFailurePromptTokens(PlatformGemini, body))
	})

	t.Run("unparseable body estimates zero rather than guessing", func(t *testing.T) {
		assert.Zero(t, EstimateFailurePromptTokens(PlatformGemini, []byte("not json")))
	})

	t.Run("empty body estimates zero", func(t *testing.T) {
		assert.Zero(t, EstimateFailurePromptTokens("", nil))
	})

	t.Run("never returns negative", func(t *testing.T) {
		assert.GreaterOrEqual(t, EstimateFailurePromptTokens("", []byte(`{}`)), 0)
	})
}

// grokFixtureProse 是两个 viaGrok 用例共用的消息内容。刻意取到 40 词以上:
// 用 "hello world" 时正确值只有 7,而"只数 model 名和 role、一个 content 字都
// 不数"的退化值是 5~6,两者挤在一起,下界断言无论取多少都分不开。加长之后
// 正确值升到 52,与元数据退化值(6)之间隔了 8 倍。
const grokFixtureProse = "Please review the token counting helper in the billing package and " +
	"explain why the fallback path floors the completion count at one token " +
	"instead of zero when the upstream stream delivered output but never sent " +
	"a terminal usage frame. Include the reasoning about partial settlement."

const (
	viaGemini = "gemini"
	viaGrok   = "grok"
	viaText   = "text"
)

// TestEstimateFailurePromptTokensDispatch 固定各平台的分发去向与边界取值。
//
// viaGemini / viaText 钉死精确值:这两条路径都是仓库内的确定性算术
// (estimateGeminiCountTokens 数字符、estimateTokensForText 是 (n+3)/4),
// 值变了就说明分发变了或计费口径变了,都该让测试红。
//
// viaGrok 不钉死具体数值——tiktoken 词表升级会合法地改变它——而是断言结果
// 严格小于同一请求体走纯文本启发式的结果。tiktoken 只数消息内容,纯文本启发式
// 把整个 JSON 骨架都算进去,两者差着三倍以上;一旦分发退化到纯文本,两个值会
// 相等,断言立刻失败。基线在测试里现算,不写死,所以不会随请求体改动而失真。
func TestEstimateFailurePromptTokensDispatch(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		body     []byte
		via      string
		want     int
	}{
		{
			name:     "gemini contents parts",
			platform: PlatformGemini,
			body:     []byte(`{"contents":[{"parts":[{"text":"hello world"}]}]}`),
			via:      viaGemini,
			want:     3,
		},
		{
			name:     "gemini system instruction is counted",
			platform: PlatformGemini,
			body:     []byte(`{"systemInstruction":{"parts":[{"text":"hello world"}]}}`),
			via:      viaGemini,
			want:     3,
		},
		{
			name:     "gemini json without text parts estimates zero",
			platform: PlatformGemini,
			body:     []byte(`{"contents":[]}`),
			via:      viaGemini,
			want:     0,
		},
		{
			// Antigravity 复用 Gemini 的请求形状,必须走同一个估算器。
			name:     "antigravity shares the gemini estimator",
			platform: PlatformAntigravity,
			body:     []byte(`{"contents":[{"parts":[{"text":"hello world"}]}]}`),
			via:      viaGemini,
			want:     3,
		},
		{
			// Anthropic 形状的请求体走 Grok/tiktoken 估算器。
			name:     "anthropic shaped body uses the grok estimator",
			platform: PlatformAnthropic,
			body:     []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"` + grokFixtureProse + `"}]}`),
			via:      viaGrok,
		},
		{
			name:     "grok platform uses the grok estimator",
			platform: PlatformGrok,
			body:     []byte(`{"model":"grok-4","max_tokens":1024,"messages":[{"role":"user","content":"` + grokFixtureProse + `"}]}`),
			via:      viaGrok,
		},
		{
			// 非 Anthropic 形状:Grok 估算器报错,退回纯文本启发式,仍好过记 0。
			name:     "openai shaped body falls back to the text heuristic",
			platform: PlatformOpenAI,
			body:     []byte(`{"messages":[{"role":"user","content":"hello world"}]}`),
			via:      viaText,
			want:     14,
		},
		{
			// 未知平台不特判,走 default 分支而非直接记 0。
			name:     "unknown platform still estimates",
			platform: "totally-unknown-platform",
			body:     []byte(`{"messages":[{"role":"user","content":"hello world"}]}`),
			via:      viaText,
			want:     14,
		},
		{
			name:     "unknown platform malformed body falls back to text heuristic",
			platform: "",
			body:     []byte("not json"),
			via:      viaText,
			want:     2,
		},
		{
			name:     "empty json object falls back to text heuristic",
			platform: "",
			body:     []byte(`{}`),
			via:      viaText,
			want:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got int
			require.NotPanics(t, func() {
				got = EstimateFailurePromptTokens(tt.platform, tt.body)
			}, "估算器对任何请求体都不得 panic")

			assert.GreaterOrEqual(t, got, 0, "估算值不得为负")

			if tt.via == viaGrok {
				// 上下界一起夹。只有上界是不够的:tiktoken 路径退化的自然方向是
				// 变小(分发错了估算器、估算器返回 0 再被 floor 拉到 1),而任何
				// 变小的值都满足"小于纯文本启发式"。实测把 grok 分支换成
				// estimateGeminiCountTokens——正是这张表自称锁定的那件事——会返回
				// 0,上界断言照样通过。
				//
				// 下界取 25:实测正确值 52,"只数元数据不数内容"的退化值是 6,
				// 25 在两者之间,对上留了两倍余量(容得下词表升级),对下留了
				// 四倍余量,不会 flaky。
				//
				// 这两条夹不住"按半个请求体算字符"那类退化(实测 47,正好落在
				// 25 和上界之间),那一类由
				// TestEstimateFailurePromptTokensCountsOnlyMessageContent
				// 用骨架不变性杀掉。
				assert.Greater(t, got, 25,
					"结果过小,说明没有真的数到消息内容(分发退化或估算器返回空)")
				textFallback := estimateTokensForText(string(tt.body))
				assert.Less(t, got, textFallback,
					"结果与纯文本启发式持平,说明没走 tiktoken 估算器")
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestEstimateFailurePromptTokensCountsOnlyMessageContent 用差分性质锁定
// "真的走了 tiktoken",覆盖上下界夹不住的那一类退化。
//
// tiktoken 只数消息内容,对请求体骨架的大小完全不敏感;而任何"按整个(或部分)
// 请求体算字符"的退化都会随骨架变大而变大。所以同样的 content 配一个膨胀过的
// 骨架,正确实现必须给出完全相同的值。
//
// 实测:骨架从 371B 膨胀到 912B,tiktoken 恒为 52,而纯文本启发式 93→228、
// 半截启发式 47→114。这条断言不是靠阈值,而是靠恒等,所以不会因词表升级而失真。
func TestEstimateFailurePromptTokensCountsOnlyMessageContent(t *testing.T) {
	lean := `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"` +
		grokFixtureProse + `"}]}`

	// 膨胀骨架:塞进一批 tiktoken 不该数的元数据字段,消息内容一个字不改。
	pad := `,"metadata":{` + strings.TrimSuffix(
		strings.Repeat(`"trace_field_with_a_long_name":"550e8400e29b41d4a716446655440000",`, 8), ",") + `}`
	fat := `{"model":"claude-sonnet-4-6","max_tokens":1024` + pad +
		`,"messages":[{"role":"user","content":"` + grokFixtureProse + `"}]}`

	require.Greater(t, len(fat), 2*len(lean), "膨胀骨架必须显著更大,否则这条测试没有区分力")

	leanGot := EstimateFailurePromptTokens(PlatformAnthropic, []byte(lean))
	fatGot := EstimateFailurePromptTokens(PlatformAnthropic, []byte(fat))

	require.Positive(t, leanGot, "基准用例必须估出正值,否则下面的恒等断言是空转的")
	assert.Equal(t, leanGot, fatGot,
		"骨架膨胀改变了估值,说明数的是整个请求体的字符,而不是消息内容")
}

func TestEstimateFailurePromptTokensDoesNotMutateBody(t *testing.T) {
	original := `{"contents":[{"parts":[{"text":"hello world"}]}]}`
	body := []byte(original)

	first := EstimateFailurePromptTokens(PlatformGemini, body)
	second := EstimateFailurePromptTokens(PlatformGemini, body)

	assert.Equal(t, original, string(body), "请求体不得被改写")
	assert.Equal(t, first, second, "相同入参必须得到相同结果")
}

// b64 生成 n 个 base64 字母表字符,用作内联负载。
func b64(n int) string { return strings.Repeat("A", n) }

// unicodeNewlineEscape 是换行符的六字符 Unicode 转义拼法(反斜杠 u 000a)。
// 分两截拼出来,免得源码里出现真正的转义序列被编辑器或工具链解释掉。
var unicodeNewlineEscape = `\` + "u000a"

// wrapAt 把 payload 按 width 列折行,行间插入 sep。用来复现 base64 负载被
// PEM(64 列)或 MIME(76 列)折行之后的各种拼法。
func wrapAt(payload, sep string, width int) string {
	var b strings.Builder
	for i := 0; i < len(payload); i += width {
		if i > 0 {
			_, _ = b.WriteString(sep)
		}
		end := i + width
		if end > len(payload) {
			end = len(payload)
		}
		_, _ = b.WriteString(payload[i:end])
	}
	return b.String()
}

// TestEstimateFailurePromptTokensStripsInlineBase64Shapes 覆盖 re-review 实测出来
// 的全部内联 base64 写法。上一版修复只认"小写 data: 且紧跟在引号后面"这一种,
// 下面除最后一行外的每一行当时都仍然估出 26 万 token。
//
// 最关键的是 Anthropic 原生图片块:它根本没有 data: scheme,base64 直接放在
// source.data 里,而这是 Claude 中转最主要的请求形状。
func TestEstimateFailurePromptTokensStripsInlineBase64Shapes(t *testing.T) {
	payload := b64(1024 * 1024)

	tests := []struct {
		name string
		body string
	}{
		{
			// 失败请求最常见的形态:写到一半被切断,没有闭合引号也没有结构。
			name: "anthropic native source.data truncated mid payload",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + payload,
		},
		{
			// 结构完整但缺 model,Grok 估算器报错,一样落到纯文本启发式。
			name: "anthropic native source.data without model",
			body: `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + payload + `"}}]}]}`,
		},
		{
			name: "uppercase data url scheme",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"DATA:image/png;base64,` + payload + `"}}]}]}`,
		},
		{
			name: "data url scheme in the middle of a string",
			body: `{"messages":[{"role":"user","content":"here is my image: data:image/png;base64,` + payload + `"}]}`,
		},
		{
			name: "gemini inline data on a non gemini platform",
			body: `{"messages":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` + payload + `"}}]}]}`,
		},
		{
			// MIME 76 列折行,JSON 转义拼法。若把转义当作普通分隔符,负载会被
			// 切成一串长度 77 的片段全部低于阈值而漏网。
			name: "mime line wrapped payload, escaped newline",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` +
				wrapAt(b64(76*1024), `\n`, 76) + `"}}]}]}`,
		},
		{
			// 字面换行字节。这才是 base64(1) 和 Python base64.encodebytes() 的
			// 原始输出,也是 shell 里把 $(base64 img.png) 直接插进 JSON 模板得到
			// 的东西。这种请求体本身就不是合法 JSON——而解析失败正是本函数被
			// 调用的前提,所以它比转义那一支更容易走到这里。
			name: "mime line wrapped payload, literal LF byte",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` +
				wrapAt(b64(76*1024), "\n", 76) + `"}}]}]}`,
		},
		{
			name: "mime line wrapped payload, literal CRLF bytes",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` +
				wrapAt(b64(76*1024), "\r\n", 76) + `"}}]}]}`,
		},
		{
			// 六字符 Unicode 转义拼法,合法 JSON 的等价写法。
			name: "mime line wrapped payload, unicode escaped newline",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` +
				wrapAt(b64(76*1024), unicodeNewlineEscape, 76) + `"}}]}]}`,
		},
		{
			// PEM 的 64 列折行,正好压在 minWrappedLineLen 上。
			name: "pem width wrapped payload",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` +
				wrapAt(b64(64*1024), "\n", 64) + `"}}]}]}`,
		},
		{
			name: "lowercase data url at string start",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + payload + `"}}]}]}`,
		},
		{
			// 原始回归用例:data URL 在负载中途被硬切断,既没有闭合引号,也没有
			// 任何后续 JSON 结构。
			name: "lowercase data url truncated mid payload",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + payload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateFailurePromptTokens(PlatformOpenAI, []byte(tt.body))

			// 剥离后只剩 JSON 结构本身,量级是两位数 token;不剥离是 26 万起步。
			// 用一个远低于"按字符计费"的上限锁定量级差,不钉死换算公式。
			assert.Less(t, got, 200,
				"内联 base64 负载没有被剥离,会把上游没收的钱算到用户头上")
		})
	}
}

// TestEstimateFailurePromptTokensPreservesOrdinaryContent 是上面那条测试的反向
// 约束:阈值必须低到能吃掉图片,又要高到不吃正常内容。base64 字母表含 '-' 和
// '_',所以 UUID、SHA-256 摘要、URL 路径、超长 snake_case 工具名在扫描器眼里
// 都是"连续串",都必须原样保留。
func TestEstimateFailurePromptTokensPreservesOrdinaryContent(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"uuid and trace id", `{"request_id":"550e8400-e29b-41d4-a716-446655440000","trace":"0af7651916cd43dd8448eb211c80319c"}`},
		{"sha256 digest", `{"sha":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`},
		{"long url path", `{"content":"see https://example.com/api/v1/organizations/acme/projects/widgets/deployments/latest for details"}`},
		{"long snake case tool name", `{"tool":{"name":"search_customer_billing_records_by_account_identifier_and_date_range"}}`},
		{"english prose", `{"content":"Please refactor the token counting helper and explain the antidisestablishmentarianism of its naming."}`},
		{"cjk prose", `{"text":"人工智能正在改变软件工程的方式,这是一段中文文本用于验证 CJK 分支不受影响。"}`},
		{
			// 一行一个 ID 的清单。换行是透明的,但只在两侧段长 >= minWrappedLineLen
			// 时才透明,所以这些短行不会被串成一整段删掉——它们是用户真实的提示词。
			"newline separated short ids",
			`{"c":"` + strings.Join([]string{"SKU00123", "SKU00124", "SKU00125", "SKU00126"}, `\n`) + `"}`,
		},
		{
			// UUID 36 字符,低于 minWrappedLineLen,多行也不会合并。
			"newline separated uuids",
			`{"c":"` + strings.Join([]string{
				"550e8400-e29b-41d4-a716-446655440000",
				"6ba7b810-9dad-11d1-80b4-00c04fd430c8",
				"6ba7b811-9dad-11d1-80b4-00c04fd430c8",
			}, `\n`) + `"}`,
		},
		{
			// 单段 120 字符的合法内容,压在 minStrippedBase64Run(128)下面一点。
			// 这一行把阈值钉在它自称的下界附近:阈值若被调到 120 以下就会红。
			"single legitimate run just under the threshold",
			`{"model":"` + b64(120) + `"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.body, stripLongBase64Runs(tt.body),
				"阈值太低,吃掉了正常内容")
		})
	}
}

// TestEstimateFailurePromptTokensRefusesOversizedBodies 锁定粗估路径上的两道闸。
//
// 没有闸的时候,32MiB 的二进制上传会估出 3355 万 token:非法 UTF-8 每个字节解成
// 一个 U+FFFD,把 estimateTokensForText 的 ascii 占比压到 0.8 以下,切到"一个
// rune 一个 token"的分支。base64 剥离对它无效——二进制不在 base64 字母表里。
// 这是本分支最不能接受的方向(多算),所以宁可返回 0(少算)。
func TestEstimateFailurePromptTokensRefusesOversizedBodies(t *testing.T) {
	t.Run("body over the size cap is rejected before it is copied", func(t *testing.T) {
		// 这一闸的作用是"不分配",不是"返回 0"——两道闸都在,单看返回值分不出
		// 是哪一道拦下的(去掉体积闸,内容还会被文本闸拦住,返回值不变)。所以
		// 这里量的是分配量:体积闸在 string(body) 之前返回,分配约等于 0;拿掉
		// 它就至少要复制一份 64MiB。两者差着一个数量级以上,方向是确定的。
		body := make([]byte, maxEstimableBodyBytes+1)
		for i := range body {
			body[i] = 0xFF
		}

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got := EstimateFailurePromptTokens(PlatformOpenAI, body)
		runtime.ReadMemStats(&after)

		assert.Zero(t, got, "超大请求体必须返回 0,而不是按字符估出一个天文数字")
		// TotalAlloc 只增不减,GC 不影响差值。阈值取 8MiB:比体积闸放行时必然
		// 产生的 64MiB 低一个数量级,又给同包其它 goroutine 的零星分配留足余量。
		assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(8<<20),
			"体积闸没有生效:请求体被复制了一份,这条路径正是上游成批报错时走的")
	})

	t.Run("binary body that survives stripping estimates zero", func(t *testing.T) {
		// 0xFF 不是合法 UTF-8 起始字节,也不在 base64 字母表里:剥不掉,
		// 且每个字节都会解成 U+FFFD。
		body := make([]byte, maxEstimableTextBytes+1)
		for i := range body {
			body[i] = 0xFF
		}
		assert.Zero(t, EstimateFailurePromptTokens(PlatformOpenAI, body),
			"剥离后仍然超大的内容只可能是二进制,不得按字符计费")
	})

	t.Run("a huge image body still estimates its small text prompt", func(t *testing.T) {
		// 闸是给"剥不掉的超大内容"准备的,不是给"带大图的正常请求"准备的:
		// 12MiB 的内联图片超过 maxEstimableTextBytes,但剥离之后只剩 JSON
		// 骨架,必须照常估出两位数。
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` +
			b64(12<<20) + `"}}]}]}`)

		got := EstimateFailurePromptTokens(PlatformOpenAI, body)

		assert.Positive(t, got, "带大图的正常请求不该被闸掉")
		assert.Less(t, got, 200, "剥离之后只剩 JSON 骨架")
	})

	t.Run("non ascii text is not mistaken for binary", func(t *testing.T) {
		// 反向约束:两道闸都只看体积,不看内容形态。中文、emoji 是用户真实的
		// 提示词,任何"看起来不像 ASCII 就不估"的判据都会误伤它们——曾经加过
		// 的 UTF-8 合法性闸就是这样被撤掉的,理由见 estimateFallbackText 的注释。
		for _, body := range []string{
			`{"messages":[{"role":"user","content":"人工智能正在改变软件工程的方式"}]}`,
			`{"messages":[{"role":"user","content":"ship it 🚀 looks good 👍"}]}`,
		} {
			assert.Positive(t, EstimateFailurePromptTokens(PlatformOpenAI, []byte(body)),
				"合法 UTF-8 的非 ASCII 文本被当成二进制丢掉了")
		}
	})

	t.Run("a truncated cjk prompt is still estimated", func(t *testing.T) {
		// 截断导致解析失败正是本函数 doc comment 自称要覆盖的主要场景,而 CJK
		// 被截在多字节序列中间是它最常见的形态。这条钉死"不因为字节层不合法就
		// 放弃估算":实测 600 个截断点里有 391 个会让请求体变成非法 UTF-8。
		full := []byte(`{"messages":[{"role":"user","content":"` +
			strings.Repeat("这是一段会议记录的文字稿,需要总结要点。", 8))
		for _, cut := range []int{len(full) - 1, len(full) - 2, len(full) - 37} {
			assert.Positive(t, EstimateFailurePromptTokens(PlatformOpenAI, full[:cut]),
				"截断点 %d:截断的 CJK 提示词被整体放弃估算了", cut)
		}
	})
}

func TestStripLongBase64Runs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no base64 run is unchanged",
			in:   `{"messages":[{"role":"user","content":"hello world"}]}`,
			want: `{"messages":[{"role":"user","content":"hello world"}]}`,
		},
		{
			// 负载够长才丢;MIME 前缀被标点切成 data / image/png / base64
			// 几段短串,全部保留。
			name: "long payload is dropped and the mime prefix is kept",
			in:   `{"url":"data:image/png;base64,` + b64(200) + `"} tail`,
			want: `{"url":"data:image/png;base64,"} tail`,
		},
		{
			name: "short payload is kept",
			in:   `{"url":"data:image/png;base64,AAAA1234=="}`,
			want: `{"url":"data:image/png;base64,AAAA1234=="}`,
		},
		{
			name: "one byte below the threshold is kept",
			in:   `{"d":"` + b64(minStrippedBase64Run-1) + `"}`,
			want: `{"d":"` + b64(minStrippedBase64Run-1) + `"}`,
		},
		{
			name: "exactly at the threshold is dropped",
			in:   `{"d":"` + b64(minStrippedBase64Run) + `"}`,
			want: `{"d":""}`,
		},
		{
			name: "multiple long runs are all dropped",
			in:   `[{"d":"` + b64(200) + `"},{"d":"` + b64(200) + `"}]`,
			want: `[{"d":""},{"d":""}]`,
		},
		{
			name: "truncated at the end of the body drops the tail",
			in:   `{"d":"` + b64(200),
			want: `{"d":"`,
		},
		{
			// 折行转义被当作透明分隔符:两侧片段合并计长,要丢就连转义一起丢。
			name: "line wrapped payload is dropped together with its escapes",
			in:   `{"d":"` + b64(76) + `\n` + b64(76) + `"}`,
			want: `{"d":""}`,
		},
		{
			// 字面换行字节同样透明。这是 base64(1) 的原始输出形态。
			name: "literal LF wrapped payload is dropped",
			in:   `{"d":"` + b64(76) + "\n" + b64(76) + `"}`,
			want: `{"d":""}`,
		},
		{
			name: "literal CRLF wrapped payload is dropped",
			in:   `{"d":"` + b64(76) + "\r\n" + b64(76) + `"}`,
			want: `{"d":""}`,
		},
		{
			name: "unicode escaped newline wrapped payload is dropped",
			in:   `{"d":"` + b64(76) + unicodeNewlineEscape + b64(76) + `"}`,
			want: `{"d":""}`,
		},
		{
			// 换行只在段长 >= minWrappedLineLen 时才透明。短行是自然内容的换行
			// (清单、日志、一行一个 ID),必须原样保留,否则会把用户真实的
			// 提示词整段吃掉。
			name: "short lines are not merged across newlines",
			in:   `{"d":"` + strings.TrimSuffix(strings.Repeat(b64(40)+"\n", 8), "\n") + `"}`,
			want: `{"d":"` + strings.TrimSuffix(strings.Repeat(b64(40)+"\n", 8), "\n") + `"}`,
		},
		{
			// 短串里的转义必须原样保留,不能被当成分隔符吞掉。
			name: "escapes inside a short run are preserved",
			in:   `{"d":"ab\ncd"}`,
			want: `{"d":"ab\ncd"}`,
		},
		{
			// 字面空格绝不能当作透明分隔符,否则整段英文散文会连成一个超长串。
			name: "literal spaces still break runs",
			in:   `{"note":"` + strings.TrimSpace(strings.Repeat("word ", 60)) + `"}`,
			want: `{"note":"` + strings.TrimSpace(strings.Repeat("word ", 60)) + `"}`,
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripLongBase64Runs(tt.in))
		})
	}
}

// TestStripLongBase64RunsScalesLinearly 锁定单趟扫描。上一版在"有 data: 但整段
// 没有逗号"的请求体上是 O(n²)(实测 262KB→61ms, 524KB→233ms, 1MiB→935ms),而
// 请求体和"这个请求会不会失败"都由客户端控制,等于把一个放大器摆在失败结算
// 路径上。
//
// 断言用绝对耗时上限而不是倍率:倍率要在两个都只有几十微秒的测量值之间比较,
// 在 Windows 和共享 CI 上会被时钟粒度直接打成 0。这里用 4MiB 的病态输入配一个
// 极宽松的上限——单趟扫描实测约 15ms,而 O(n²) 版本按 1MiB 935ms 外推是十几秒,
// 中间隔着两个数量级,既不会误报也漏不掉真正的退化。
func TestStripLongBase64RunsScalesLinearly(t *testing.T) {
	const size = 4 * 1024 * 1024
	const budget = 3 * time.Second

	// 病态形状取自该 finding:大量引号开头的 `data:`,整段没有逗号,旧扫描器
	// 每遇到一个就把剩余部分全扫一遍找 ',',而 rest 只前进 5 字节。
	pathological := strings.Repeat(`"data:`, size/6)

	start := time.Now()
	got := stripLongBase64Runs(pathological)
	elapsed := time.Since(start)

	require.NotEmpty(t, got, "结果不得为空,否则说明测量的不是真实调用")
	assert.Less(t, elapsed, budget,
		"4MiB 病态输入耗时超过 %s,扫描很可能退化成了 O(n²)", budget)
}
