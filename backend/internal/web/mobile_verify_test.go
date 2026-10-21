package web

import (
	"testing"
	"testing/fstest"
)

func TestDistHasMobileVerifyPage(t *testing.T) {
	cases := []struct {
		name string
		dist fstest.MapFS
		want bool
	}{
		{name: "empty dist", dist: fstest.MapFS{}, want: false},
		{name: "bridge chunk present", dist: fstest.MapFS{
			"index.html":                        {Data: []byte("<html></html>")},
			"assets/MobileVerifyView-AbC123.js": {Data: []byte("export{}")},
		}, want: true},
		{name: "other view chunks only", dist: fstest.MapFS{
			"index.html":                 {Data: []byte("<html></html>")},
			"assets/LoginView-AbC123.js": {Data: []byte("export{}")},
		}, want: false},
		{name: "bridge stylesheet without script", dist: fstest.MapFS{
			"assets/MobileVerifyView-AbC123.css": {Data: []byte("a{}")},
		}, want: false},
		{name: "chunk outside assets", dist: fstest.MapFS{
			"MobileVerifyView-AbC123.js": {Data: []byte("export{}")},
		}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := distHasMobileVerifyPage(tc.dist); got != tc.want {
				t.Fatalf("distHasMobileVerifyPage() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMobileVerifyPagePathMatchesFrontendRoute(t *testing.T) {
	if MobileVerifyPagePath != "/mobile/verify" {
		t.Fatalf("MobileVerifyPagePath = %q, want the frontend route /mobile/verify", MobileVerifyPagePath)
	}
}
