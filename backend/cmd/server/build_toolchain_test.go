//go:build unit

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// 官方 golang 镜像默认 GOTOOLCHAIN=local：构建镜像的 Go 版本低于 go.mod 时，
// `go mod download` 直接失败。CI 用 setup-go，不走 Dockerfile，测不出来，所以在这里锁住。
func TestDockerfilesUseGoModToolchain(t *testing.T) {
	goMod := readRepoFile(t, "backend/go.mod")
	goVersion := regexp.MustCompile(`(?m)^go (\d+\.\d+\.\d+)\s*$`).FindStringSubmatch(goMod)
	require.Len(t, goVersion, 2, "backend/go.mod must declare a full go version")
	want := "golang:" + goVersion[1] + "-alpine"

	for path, pattern := range map[string]string{
		"Dockerfile":         `(?m)^ARG GOLANG_IMAGE=(\S+)\s*$`,
		"deploy/Dockerfile":  `(?m)^ARG GOLANG_IMAGE=(\S+)\s*$`,
		"backend/Dockerfile": `(?m)^FROM (golang:\S+)`,
	} {
		match := regexp.MustCompile(pattern).FindStringSubmatch(readRepoFile(t, path))
		require.Len(t, match, 2, "%s must declare a Go builder image", path)
		require.Equal(t, want, match[1], "%s Go builder image must match backend/go.mod", path)
	}
}

// 根 Dockerfile 的前端构建用与 CI 相同的固定 Node 版本（v0.2.15 升级时确定），
// 避免同一提交在不同日期构建出不同的前端产物。
func TestRootDockerfilePinsCINodeVersion(t *testing.T) {
	nodeVersion := regexp.MustCompile(`node-version: '([^']+)'`)
	var ciVersion string
	for _, workflow := range []string{"backend-ci.yml", "release.yml", "security-scan.yml"} {
		match := nodeVersion.FindStringSubmatch(readRepoFile(t, filepath.Join(".github", "workflows", workflow)))
		require.Len(t, match, 2, "%s must pin node-version", workflow)
		if ciVersion == "" {
			ciVersion = match[1]
		}
		require.Equal(t, ciVersion, match[1], "CI workflows must pin the same Node version")
	}

	match := regexp.MustCompile(`(?m)^ARG NODE_IMAGE=(\S+)\s*$`).FindStringSubmatch(readRepoFile(t, "Dockerfile"))
	require.Len(t, match, 2)
	require.Equal(t, "node:"+ciVersion+"-alpine", match[1])
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(data)
}
