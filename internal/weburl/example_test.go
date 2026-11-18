package weburl_test

import (
	"fmt"

	"github.com/accessdurin/cli/internal/weburl"
)

// Every spelling of a loopback address parses to the one hostname the CLI's loopback patterns
// match, exactly as Node writes it.
func ExampleURL_Hostname() {
	for _, in := range []string{
		"http://0x7f.1/", "http://2130706433/", "http://0177.0.0.1/", "http://LOCALHOST./",
		"http://[0:0:0:0:0:0:0:1]/", "http://[::ffff:127.0.0.1]/", "https://MÜNCHEN.de/",
	} {
		u, _ := weburl.Parse(in)
		fmt.Println(u.Hostname())
	}
	// Output:
	// 127.0.0.1
	// 127.0.0.1
	// 127.0.0.1
	// localhost.
	// [::1]
	// [::ffff:7f00:1]
	// xn--mnchen-3ya.de
}

// canonicalOrigin in src/cli-options.ts strips one trailing dot through the hostname setter.
func ExampleURL_SetHostname() {
	u, _ := weburl.Parse("https://app.getdurin.com.:8443")
	u.SetHostname(u.Hostname()[:len(u.Hostname())-1])
	fmt.Println(u.Origin(), u.Href() == "https://app.getdurin.com:8443/")
	// Output: https://app.getdurin.com:8443 true
}

// The callback server reads its query the way Object.fromEntries(url.searchParams) does.
func ExampleSearchParams_Object() {
	u, _ := weburl.ParseRef("/oauth/callback?code=a&code=b&state=x+y", "http://127.0.0.1:53682/oauth/callback")
	fmt.Println(len(u.SearchParams().GetAll("code")), u.SearchParams().Object())
	fmt.Println(weburl.NewSearchParams([2]string{"provider", "authkit"}, [2]string{"q", "a b&c"}))
	// Output:
	// 2 map[code:b state:x y]
	// provider=authkit&q=a+b%26c
}
