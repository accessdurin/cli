package weburl

import "strings"

// cannotHaveCredentials is the URL Standard's "cannot have a username/password/port": a URL
// without a host, with an empty host, or with the file scheme.
func (u *URL) cannotHaveCredentials() bool {
	return !u.hasHost || u.host.String() == "" || u.scheme == "file"
}

// SetUsername is the url.username setter.
func (u *URL) SetUsername(value string) {
	if u.cannotHaveCredentials() {
		return
	}
	u.username = percentEncodeString(strings.ToValidUTF8(value, "\uFFFD"), userinfoSet)
}

// SetPassword is the url.password setter.
func (u *URL) SetPassword(value string) {
	if u.cannotHaveCredentials() {
		return
	}
	u.password = percentEncodeString(strings.ToValidUTF8(value, "\uFFFD"), userinfoSet)
}

// SetProtocol is the url.protocol setter: value up to the first ':' must be a scheme; a
// special URL keeps a special scheme and a non-special one a non-special scheme; a URL with
// credentials or a port cannot become file:, and a file: URL with an empty host keeps its
// scheme. A port equal to the new scheme's default port is removed.
func (u *URL) SetProtocol(value string) {
	value = string(removeTabNewline([]rune(value)))
	if i := strings.IndexByte(value, ':'); i >= 0 {
		value = value[:i]
	}
	if value == "" || !isASCIIAlpha(rune(value[0])) {
		return
	}
	for _, c := range value[1:] {
		if !isASCIIAlnum(c) && c != '+' && c != '-' && c != '.' {
			return
		}
	}
	scheme := strings.ToLower(value)
	if isSpecialScheme(u.scheme) != isSpecialScheme(scheme) {
		return
	}
	if scheme == "file" && (u.includesCredentials() || u.port != -1) {
		return
	}
	if u.scheme == "file" && u.host.String() == "" {
		return
	}
	u.scheme = scheme
	if u.port != -1 && u.port == defaultPort(scheme) {
		u.port = -1
	}
}
