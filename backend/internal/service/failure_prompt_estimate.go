package service

import (
	"strings"
)

// EstimateFailurePromptTokens 在上游未返回 usage 时估算请求侧 token 数,供失败
// 请求计费使用。复用各平台既有估算器;只有请求体为空时才直接返回 0,其余情况
// (含 JSON 不合法、缺 model、内容形状不支持等解析失败)一律退回粗略的文本长度
// 估算,而不是记 0——失败请求本就容易中途被截断而解析失败,记 0 会让这个功能
// 漏掉它主要想覆盖的场景。粗估前会先剥离请求体里内联的 base64 负载,避免把
// 图片二进制当文本按字符计费。
//
// 纯函数:不做 IO、不读时钟、不写日志、不改写 body。任何畸形/空请求体都是
// 预期输入而非错误,统一返回非负值。
func EstimateFailurePromptTokens(platform string, body []byte) int {
	if len(body) == 0 {
		return 0
	}

	estimated := 0
	switch platform {
	case PlatformGemini, PlatformAntigravity:
		// Antigravity 复用 Gemini 的请求形状,共用同一个估算器。
		estimated = estimateGeminiCountTokens(body)
	default:
		if n, err := EstimateGrokCountTokens(body); err == nil {
			estimated = n
		} else {
			// 请求体不是 Anthropic 形状时退回纯文本启发式,仍好过记 0。
			estimated = estimateFallbackText(body)
		}
	}

	if estimated < 0 {
		return 0
	}
	return estimated
}

// maxEstimableBodyBytes 是走粗估路径的请求体上限,超过就直接返回 0(=估算不了),
// 不剥离也不计费。gateway.max_body_size 默认 256MiB(config/config.go:2275),而
// 剥离加 []rune 转换的瞬时分配约是请求体的 6~7 倍,256MiB 的请求体要吃掉 1.5GiB;
// 这条路径又恰好由上游报错触发,而上游报错是成批来的。64MiB 远高于任何真实的
// 带图请求(40MiB 的图 base64 之后约 53MiB),同时把最坏分配压在 450MiB 上下。
const maxEstimableBodyBytes = 64 << 20

// maxEstimableTextBytes 是剥离之后仍要按字符计费的文本上限。剥不掉、又有这么大
// 体量的内容不可能是提示词:上下文窗口最大的模型也就 200 万 token,按 4 字符一个
// token 折算约 8MB,再大上游根本不会受理,也就不会产生账单。真正会落到这里的是
// 二进制上传——非法 UTF-8 每个字节解成一个 U+FFFD,把 estimateTokensForText
// (gemini_messages_compat_service.go:2534)的 ascii 占比压到 0.8 以下,切到
// "一个 rune 一个 token"的分支,32MiB 会估出 3355 万 token。宁可返回 0(少算)
// 也不能把这个数字记到用户账上(多算)。
const maxEstimableTextBytes = 8 << 20

// estimateFallbackText 是纯文本启发式那条路径,带两道体积闸:请求体本身超大就
// 不估,剥离之后仍然超大也不估。两道闸都返回 0 而不是一个大数,与本文件"估算
// 不了就返回 0、绝不猜大数"的口径一致。
//
// 已知局限,不在本函数解决:体积闸之下的二进制上传(几 MiB 量级的 multipart
// 图片/音频)仍会被按字符计价。曾经尝试用 UTF-8 合法性作判据挡它,实测两头都
// 不成立,故撤回:
//
//   - 挡不住。16 位 PCM 低振幅音频的每个字节都落在 ASCII 区,整体是合法 UTF-8,
//     非法字节占比 0.0000,1MiB 照样估出 262144 token。稀疏 PNG 也只有 12.5%。
//   - 会误伤。CJK 请求被截断时,600 个截断点里有 391 个让请求体变成非法 UTF-8;
//     6063 字节的纯英文提示词末尾多一个坏字节,估值从 1516 掉到 0。而"截断导致
//     解析失败"正是本函数 doc comment 自称要覆盖的主要场景。
//
// 换成"非法字节占比"也不行:真实提示词侧的上界(emoji 截断 6.25%)和二进制侧
// 的下界(稀疏 PNG 12.5%)只差两倍,中间还夹着占比为 0 的 PCM。结论是字节层
// 统计无法区分二进制与文本——能区分的是端点:图片/multipart 端点按张计费,
// 本来就不该走 token 估算。这道防线在调用方(端点白名单),不在这里。
func estimateFallbackText(body []byte) int {
	if len(body) > maxEstimableBodyBytes {
		return 0
	}
	// 计费前先剥离内联的 base64 负载,避免把图片二进制当英文文本按 ~4 字符/
	// token 计费,放大成天文数字。
	stripped := stripLongBase64Runs(string(body))
	if len(stripped) > maxEstimableTextBytes {
		return 0
	}
	return estimateTokensForText(stripped)
}

// minStrippedBase64Run 是会被当作内联二进制负载整段丢弃的 base64 连续串的最小
// 长度。取 128 的理由是两头都留足了余量:
//
//   - 下界(不能吃掉正常内容):base64 字母表含 '-' 和 '_',所以 UUID(36 字符)、
//     SHA-256 十六进制摘要(64 字符)、超长 snake_case 工具名和模型 ID 在扫描
//     器眼里都是"一整段连续串"。128 是 UUID 的 3.5 倍、英语最长常用词
//     antidisestablishmentarianism(28 字符)的 4.5 倍。自然语言里的空格和标点
//     都不在字母表内,单词天然被切断,连不成 128 字符。
//   - 上界(必须吃掉真实图片):128 个 base64 字符只解码出 96 字节,比任何真实
//     图片都小。真实内联图片起步就是几 KB,base64 之后是上千到上百万字符,离
//     128 有三到四个数量级。
//
// 误剥的代价不小。下面四个是实测的损失比例(剥离后估值相对未剥离的降幅),
// 输入构造一并记下,阈值改动后可以重新推导——只写百分比的话没人能验证它是否
// 已经失真:
//
//   - 52%:`{"messages":[{"role":"user","content":"这个 token 为什么过期了:<JWT>"}]}`,
//     JWT 为标准三段式、payload 约 180 字符。三段各自超过 128,整段被丢。
//   - 55%:同上形状,内容是一个含 129 字符路径段的预签名 S3 URL。
//   - 77%:`"帮我解码这段 base64:" + strings.Repeat("SGVsbG8g", 25)`(负载 200 字符)。
//   - 84%:`"校验这几个摘要:\n" + 4 个 SHA-256 摘要以 \n 分隔`。摘要各 64 字符,
//     达到 minWrappedLineLen,换行被当作透明分隔符,四条合并超过 128 一起被丢。
//
// 这些都是提示词里相当常见的东西,所以这不是"偶尔多剥一段",而是一类稳定的少算。
// 之所以仍然接受:少算的代价是运营方少收一点钱,多算的代价是把上游根本没计费的
// token 记到用户账上。本分支的原则是宁可少算不可多算,所以阈值宁可取小。
const minStrippedBase64Run = 128

// minWrappedLineLen 是"这一段像是被折行的 base64"的最小段长。标准 base64 折行
// 宽度只有两个:PEM 的 64 和 MIME 的 76,取下限 64 就都覆盖到了。
//
// 加这道门槛是为了把"折行的负载"和"本来就分行的正常内容"分开。没有门槛的话,
// 一份一行一个 ID 的清单、或者用户粘贴的多行日志,会被换行符串成一整段而整体
// 超过 minStrippedBase64Run 被删掉——那是用户真实的提示词。段长低于 64 的换行
// 更可能是自然内容的换行,不当作透明分隔符。
const minWrappedLineLen = 64

// stripLongBase64Runs 丢弃 raw 里每一段长度 >= minStrippedBase64Run 的 base64
// 连续串(字母表 A-Z a-z 0-9 + / - _ =),不论它出现在哪里、前面是什么字符。
// 这一条规则同时覆盖了内联二进制负载的所有写法:
//
//   - 任意大小写、任意位置的 data URL(`data:image/png;base64,<负载>`)。MIME
//     前缀本身被标点切成 data / image/png / base64 几段短串,会原样保留;只有
//     逗号之后的负载够长才被丢掉,效果与 openai_gateway_count_tokens.go 的
//     estimateOpenAIInputImageText 截断到逗号一致。
//   - Anthropic 原生图片块 `"source":{"type":"base64","data":"<负载>"}`。它根本
//     没有 data: scheme——data URI 是走通顺解析路径时才由
//     apicompat.anthropicImageToDataURI 拼出来的,而本函数只在解析失败时被调用,
//     那条路径恰好没走。对 Claude 中转来说这是最主要的请求形状。
//   - Gemini 的 `inlineData.data`,以及以上各种在 base64 中途被截断的变体
//     (没有闭合引号、甚至没有逗号):连续串在字符串末尾自然结束,同样丢弃。
//   - 以上各种写法被折行之后的变体:够长的段之间的换行被当作透明分隔符,两侧
//     合并计长,理由见 base64SeparatorWidth 与 minWrappedLineLen。
//
// 单趟线性扫描:每个字节只被检查一次、最多被写出一次,没有回溯,没有对剩余
// 部分的重复搜索。
//
// 只按字节判断。base64 字母表全是 ASCII(< 0x80),而 UTF-8 多字节序列的每个
// 字节都 >= 0x80,所以不会把一个字符切开;非法 UTF-8 字节只是原样写出。
// 纯函数,不改写入参,只构造新字符串返回。
func stripLongBase64Runs(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))

	// runStart 是当前逻辑连续串在 raw 中的起点,runLen 是其中真正属于 base64
	// 字母表的字符数(不含被跳过的折行符)。两者分开是因为一段负载可能被折行
	// 切成多截,长度要合起来算,但要丢就得连折行符一起丢。
	runStart, runLen, i := 0, 0, 0
	for i < len(raw) {
		if isBase64RunByte(raw[i]) {
			runLen++
			i++
			continue
		}
		if runLen >= minWrappedLineLen {
			if w := base64SeparatorWidth(raw, i); w > 0 {
				i += w
				continue
			}
		}
		if runLen < minStrippedBase64Run {
			_, _ = b.WriteString(raw[runStart:i])
		}
		_ = b.WriteByte(raw[i])
		i++
		runStart, runLen = i, 0
	}
	// 收尾:请求体在 base64 中途被截断时,最后一段直到末尾都没有结束。
	if runLen < minStrippedBase64Run {
		_, _ = b.WriteString(raw[runStart:])
	}
	return b.String()
}

// base64SeparatorWidth 返回 raw[i:] 处换行分隔符的字节宽度;不是换行则返回 0。
//
// base64 负载常被按 PEM 的 64 列或 MIME 的 76 列折行。三种拼法都要认,因为请求体
// 到底有没有经过 JSON 编码器是客户端说了算:
//
//   - 字面字节 \n \r:GNU coreutils 的 base64(1) 和 Python 的
//     base64.encodebytes() 的原始输出,以及 shell 里把 $(base64 img.png) 直接
//     插进 JSON 模板的常见写法。这种请求体本身就不是合法 JSON,而解析失败正是
//     本函数被调用的前提,所以这一支的可达性比转义那一支只高不低。
//   - JSON 转义 \n \r:同样的内容被正确编码进 JSON 字符串之后的样子。
//   - 六字符的 Unicode 转义拼法(反斜杠 u 000a / 000d):合法但少见的等价写法,
//     一并认掉,免得只堵一半。
//
// 字面空格和制表符不算换行:它们是自然语言的词分隔符,一旦当作透明分隔符,
// 整段英文散文都会连成一个超长"连续串"被吃掉。
func base64SeparatorWidth(raw string, i int) int {
	switch raw[i] {
	case '\n', '\r':
		return 1
	case '\\':
		if i+1 < len(raw) && (raw[i+1] == 'n' || raw[i+1] == 'r') {
			return 2
		}
		if i+5 < len(raw) && raw[i+1] == 'u' &&
			raw[i+2] == '0' && raw[i+3] == '0' && raw[i+4] == '0' &&
			isNewlineHexDigit(raw[i+5]) {
			return 6
		}
	}
	return 0
}

// isNewlineHexDigit 判定 \u000X 里的末位十六进制数字是否指向 LF(a)或 CR(d)。
// 大小写都认,JSON 对转义里的十六进制数字不区分大小写。
func isNewlineHexDigit(c byte) bool {
	return c == 'a' || c == 'A' || c == 'd' || c == 'D'
}

// isBase64RunByte 判定 c 是否属于 base64 字母表:标准表(A-Z a-z 0-9 + /)、
// URL 安全表的两个替换字符(- _)、以及填充符 =。三种表一起认,因为请求体里
// 到底用哪一种由客户端决定。
func isBase64RunByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z',
		c >= 'a' && c <= 'z',
		c >= '0' && c <= '9':
		return true
	}
	return c == '+' || c == '/' || c == '-' || c == '_' || c == '='
}
