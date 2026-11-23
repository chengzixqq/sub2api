package claude

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKnownBeta_AllBetaConstants(t *testing.T) {
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "constants.go", nil, 0)
	require.NoError(t, err)
	// Only type-check constants so package helpers in other files are irrelevant.
	decls := file.Decls[:0]
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			decls = append(decls, decl)
		}
	}
	file.Decls = decls
	config := types.Config{}
	pkg, err := config.Check("claude", files, []*ast.File{file}, nil)
	require.NoError(t, err)
	for _, name := range pkg.Scope().Names() {
		if strings.HasPrefix(name, "Beta") {
			c, ok := pkg.Scope().Lookup(name).(*types.Const)
			require.True(t, ok, name)
			require.True(t, IsKnownBeta(constant.StringVal(c.Val())), name)
		}
	}
}

func TestKnownBeta_LegacyAndClientHeaders(t *testing.T) {
	for _, name := range []string{
		"ccr-byoc-2025-07-29",
		"environments-2025-11-01",
		"mcp-servers-2025-12-04",
		"program-use-case-ids-2026-09-17",
		"afk-mode-2026-01-31",
		"auto-mode-classifier-2026-07-16",
		"cache-keepalive-2026-09-03",
		"claude-code-side-query-2026-09-29",
		"context-hint-2026-04-09",
		"prompt-caching-evict-2026-05-12",
		"resumption-2026-10-08",
		"structured-outputs-2025-12-15",
		"thinking-resumption-2026-07-17",
		"timing-2026-09-09",
		"tool-provenance-2026-10-26",
		"web-search-results-only-2026-11-15",
		"dangerous-tool-use-2026-09-03", "web-search-2025-03-05", "web-fetch-2025-09-10",
		"code-execution-2025-08-25", "search-results-2025-06-09", "memory-2025-08-18",
		"structured-outputs-2025-09-17", "tools-2024-04-04", "tools-2024-05-16",
		"max-tokens-3-5-sonnet-2024-07-15",
	} {
		t.Run(name, func(t *testing.T) { require.True(t, IsKnownBeta(name)) })
	}
}

func TestKnownBeta_ProductionLiterals(t *testing.T) {
	// Parse Go string literals only. Comments, tests and dates embedded in JSON
	// or URLs are not standalone beta names. Include raw strings as well.
	datedName := regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]*-[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	files := token.NewFileSet()
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(lit.Value)
			require.NoError(t, err)
			// Bedrock and Vertex protocol versions and OpenAI image model IDs.
			if datedName.MatchString(name) && name != "bedrock-2023-05-31" && name != "vertex-2023-10-16" &&
				!strings.HasPrefix(name, "gpt-image-") && !IsKnownBeta(name) {
				t.Errorf("%s: unrecognized dated literal %q", files.Position(n.Pos()), name)
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
}

func TestFallbacksJuneBeta(t *testing.T) {
	require.Empty(t, UnknownTopLevelRequestFields([]byte(`{"fallbacks":[]}`), "server-side-fallback-2026-06-01"))
}
