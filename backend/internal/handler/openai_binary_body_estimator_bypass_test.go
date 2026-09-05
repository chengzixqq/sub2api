package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openAIBinaryBodyEndpointFiles 是请求体可能是 multipart / 二进制的 handler 文件。
// 这两个端点的 guardDeps.estimatedPromptTokens 必须接 openAIBinaryBodyEndpointPromptTokens
// （恒 0），绝不能接 service.EstimateFailurePromptTokens。
//
// grok_media.go 之所以在列：它的名字里没有 multipart，全仓 `multipart` grep 也不命中
// （handler 把原始字节透传，没有 parsed.Multipart 字段），但它确实通过
// parseGrokMediaMultipartRequest 接受 multipart/form-data 上传。仅凭 grep 放行会漏掉它。
var openAIBinaryBodyEndpointFiles = []string{
	"openai_images.go",
	"grok_media.go",
}

// TestOpenAIBinaryBodyEndpointsBypassPromptEstimator 是本轮唯一挡住
// 「二进制请求体进入 token 估算器」这条多算缺陷的测试，人类裁决已明确这里没有别的 backstop。
//
// 缺陷长什么样：EstimateFailurePromptTokens 的文本兜底按每个 rune 记 1 token。
// 一个 1 MiB 的图片/音频上传会估出约 1,048,576 个 prompt token；而这些端点按张计费
// （BillingModeImage），根本不按 token 计价。一旦这类请求失败并触发兜底结算，
// 用户会被按一百万 token 扣费——多算方向，最严重级别。
//
// 测试用 AST 而不是跑一次转发：断言的是"结构性存在"（哪个函数被接到 estimatedPromptTokens
// 上），跑转发流程需要伪造完整的账号选择/上游依赖链，且无法阻止日后有人把接线改回去。
func TestOpenAIBinaryBodyEndpointsBypassPromptEstimator(t *testing.T) {
	for _, name := range openAIBinaryBodyEndpointFiles {
		t.Run(name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
			require.NoError(t, err)

			assignments := collectEstimatedPromptTokensAssignments(fset, file)
			require.NotEmpty(t, assignments,
				"%s：没有找到 guardDeps 的 estimatedPromptTokens 字段赋值。"+
					"若本端点的 guard 接线被整条删除，请同时把该文件从 "+
					"openAIBinaryBodyEndpointFiles 里移除并说明原因", name)

			for _, a := range assignments {
				assert.Equal(t, "openAIBinaryBodyEndpointPromptTokens", a.value,
					"%s：%s 处的 estimatedPromptTokens 必须直接接 "+
						"openAIBinaryBodyEndpointPromptTokens（恒返回 0），实际接的是 %q。"+
						"本端点的请求体可能是 multipart/二进制，交给 EstimateFailurePromptTokens "+
						"会把二进制按每 rune 1 token 估算（1 MiB ⇒ 约 1,048,576 token），"+
						"而本端点按张计费，属多算方向且没有别的兜底",
					name, a.pos, a.value)

				assert.NotContains(t, a.value, "EstimateFailurePromptTokens",
					"%s：%s 处的 estimatedPromptTokens 引用了 EstimateFailurePromptTokens", name, a.pos)
			}
		})
	}
}

// TestOpenAIBinaryBodyEndpointPromptTokensIsZero 锁死短路函数自身的返回值。
// 上面的 AST 测试只保证"接的是这个函数"，这条保证"这个函数确实不估算"——
// 若日后有人把它改成真的去调估算器，AST 测试仍会绿，只有本测试会 FAIL。
func TestOpenAIBinaryBodyEndpointPromptTokensIsZero(t *testing.T) {
	assert.Equal(t, 0, openAIBinaryBodyEndpointPromptTokens(),
		"openAIBinaryBodyEndpointPromptTokens 必须恒定返回 0：它存在的唯一目的就是"+
			"让 multipart/二进制端点完全不进入 prompt token 估算")
}

// TestEstimateFailurePromptTokensOverchargesOnBinaryBody 坐实上面两条测试防的到底是什么，
// 即"若二进制体真进了估算器会发生什么"——这条是反向锚点，直接对估算器本身取证。
//
// 它同时是一份可执行的证据：证明短路不是过度谨慎，而是必要的。
func TestEstimateFailurePromptTokensOverchargesOnBinaryBody(t *testing.T) {
	// 构造一个 1 MiB 的真实形状 multipart 体：MIME 边界 + 原始（未 base64 编码的）
	// 低振幅 16 位 PCM 音频负载。这正是 failure_prompt_estimate.go 第 61-74 行
	// doc comment 亲自记下的、它挡不住的那一类输入。
	//
	// 为什么必须用这个构造，而不是「1 MiB 重复的 'A'」：后者是一整段长度上百万的
	// base64 连续串，会被 stripLongBase64Runs 整段剥掉，只估出 2 个 token——那衡量的
	// 是剥离器生效，不是估算器的多算风险。低振幅 PCM 的高位字节恒为 0x00，不在 base64
	// 字母表内，把连续串切成长度 1 的碎片，一个都剥不掉；同时每个字节都落在 ASCII 区、
	// 整体是合法 UTF-8（doc 实测非法字节占比 0.0000），于是走 ~4 字符/token 分支。
	body := make([]byte, 0, 1<<20)
	body = append(body, []byte("------WebKitFormBoundaryAbC123\r\n"+
		"Content-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n"+
		"Content-Type: audio/wav\r\n\r\n")...)
	for i := 0; len(body) < 1<<20; i++ {
		// 低位字节小幅波动，高位字节恒 0x00（低振幅样本）。
		body = append(body, byte(i%64), 0x00)
	}

	got := service.EstimateFailurePromptTokens(service.PlatformOpenAI, body)

	assert.Greater(t, got, 100_000,
		"前提失效：EstimateFailurePromptTokens 对 1 MiB 原始二进制 multipart 体只估出了 "+
			"%d token。若它已经能识别并拒绝这类请求体，"+
			"openAIBinaryBodyEndpointPromptTokens 的短路就不再是必需的，"+
			"请重新评估上面两条测试是否还成立", got)
}

// estimatedPromptTokensAssignment 记录一处 estimatedPromptTokens 字段赋值。
type estimatedPromptTokensAssignment struct {
	value string // 赋值右侧的源码文本（标识符名，或函数字面量的展开）
	pos   string
}

// collectEstimatedPromptTokensAssignments 找出文件里所有复合字面量中
// `estimatedPromptTokens: <expr>` 形式的字段赋值。
func collectEstimatedPromptTokensAssignments(
	fset *token.FileSet, file *ast.File,
) []estimatedPromptTokensAssignment {
	var out []estimatedPromptTokensAssignment

	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "estimatedPromptTokens" {
			return true
		}
		out = append(out, estimatedPromptTokensAssignment{
			value: exprSourceText(kv.Value),
			pos:   fset.Position(kv.Pos()).String(),
		})
		return true
	})

	return out
}

// exprSourceText 把表达式还原成便于断言的文本。裸标识符直接返回名字；
// 其他形态（函数字面量等）收集其中出现的所有标识符名，保证
// EstimateFailurePromptTokens 这类调用不会被漏掉。
func exprSourceText(e ast.Expr) string {
	if ident, ok := e.(*ast.Ident); ok {
		return ident.Name
	}
	var names []string
	ast.Inspect(e, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok {
			names = append(names, ident.Name)
		}
		return true
	})
	return strings.Join(names, ".")
}
