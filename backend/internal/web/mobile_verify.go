package web

import "io/fs"

// MobileVerifyPagePath is the frontend route of the native-app captcha bridge
// (frontend/src/router/index.ts, docs/mobile-verification.md).
const MobileVerifyPagePath = "/mobile/verify"

// mobileVerifyChunkPattern matches the code-split chunk Vite emits for the
// lazily imported bridge view (MobileVerifyView-<hash>.js).
const mobileVerifyChunkPattern = "assets/MobileVerifyView-*.js"

// distHasMobileVerifyPage reports whether a built frontend contains the bridge
// page. A miss only means native apps are told the page is unavailable.
func distHasMobileVerifyPage(dist fs.FS) bool {
	matches, err := fs.Glob(dist, mobileVerifyChunkPattern)
	return err == nil && len(matches) > 0
}
