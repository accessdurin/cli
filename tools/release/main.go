// Command release builds the durin CLI's distributions for the machine it runs on, and the
// cross-platform metadata that ties a release together.
//
//	go run ./tools/release package   [-out release]   # this OS/arch: archive (+ .deb on Linux) + npm platform package
//	go run ./tools/release metadata  [-out release] [-write-formula] [-check-formula]
//	                                                  # Homebrew formula, npm launcher package, Windows installer, SHA256SUMS
//	go run ./tools/release licenses  [-goos G -goarch A] # print the third-party notices for a build
//
// The version comes from internal/version. Packages are built natively on each release runner
// (macOS needs cgo for the Keychain); nothing here publishes anything.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Targets are the platforms a release ships, as <os>-<arch> in npm/Node naming.
var targets = []target{
	{"linux", "amd64", "linux", "x64"},
	{"linux", "arm64", "linux", "arm64"},
	{"darwin", "amd64", "darwin", "x64"},
	{"darwin", "arm64", "darwin", "arm64"},
	{"windows", "amd64", "win32", "x64"},
}

type target struct{ goos, goarch, nodeOS, nodeArch string }

// name is the archive suffix, e.g. linux-x64, darwin-arm64, windows-x64.
func (t target) name() string {
	osName := t.goos
	return osName + "-" + t.nodeArch
}

func (t target) exe() string {
	if t.goos == "windows" {
		return "durin.exe"
	}
	return "durin"
}

func main() {
	if len(os.Args) < 2 {
		fail("usage: release package|metadata|licenses [flags]")
	}
	switch os.Args[1] {
	case "package":
		flags := flag.NewFlagSet("package", flag.ExitOnError)
		out := flags.String("out", "release", "output directory")
		_ = flags.Parse(os.Args[2:])
		packageHost(*out)
	case "metadata":
		flags := flag.NewFlagSet("metadata", flag.ExitOnError)
		out := flags.String("out", "release", "directory holding every platform's packages")
		write := flags.Bool("write-formula", false, "also write Formula/durin.rb")
		check := flags.Bool("check-formula", false, "fail unless Formula/durin.rb matches")
		_ = flags.Parse(os.Args[2:])
		metadata(*out, *write, *check)
	case "licenses":
		flags := flag.NewFlagSet("licenses", flag.ExitOnError)
		goos := flags.String("goos", runtime.GOOS, "target OS")
		goarch := flags.String("goarch", runtime.GOARCH, "target architecture")
		_ = flags.Parse(os.Args[2:])
		fmt.Print(thirdPartyNotices(*goos, *goarch))
	default:
		fail("unknown subcommand " + os.Args[1])
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func run(dir string, env []string, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), env...)
	check(cmd.Run())
}

var versionPattern = regexp.MustCompile(`var Version = "(\d+\.\d+\.\d+)"`)

func version() string {
	source, err := os.ReadFile(filepath.Join("internal", "version", "version.go"))
	check(err)
	match := versionPattern.FindSubmatch(source)
	if match == nil {
		fail("internal/version/version.go has no release version")
	}
	return string(match[1])
}

func hostTarget() target {
	for _, t := range targets {
		if t.goos == runtime.GOOS && t.goarch == runtime.GOARCH {
			return t
		}
	}
	fail("unsupported release platform " + runtime.GOOS + "/" + runtime.GOARCH)
	return target{}
}

// packageHost builds this machine's binary, starts it, and writes the archive, the Debian
// package (Linux) and the npm platform package into out.
func packageHost(out string) {
	t := hostTarget()
	v := version()
	stage := filepath.Join(".release-stage", t.name())
	check(os.RemoveAll(stage))
	check(os.MkdirAll(stage, 0o755))
	check(os.MkdirAll(out, 0o755))
	binary := filepath.Join(stage, t.exe())
	cgo := "CGO_ENABLED=0"
	if t.goos == "darwin" {
		cgo = "CGO_ENABLED=1" // the Keychain store needs the Security framework
	}
	run(".", []string{cgo}, "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", binary, "./cmd/durin")
	run(".", nil, binary, "--help")
	copyFile("LICENSE", filepath.Join(stage, "LICENSE"), 0o644)
	copyFile("README.md", filepath.Join(stage, "README.md"), 0o644)
	check(os.WriteFile(filepath.Join(stage, "THIRD_PARTY_LICENSES.md"), []byte(thirdPartyNotices(t.goos, t.goarch)), 0o644))

	files := []string{t.exe(), "LICENSE", "README.md", "THIRD_PARTY_LICENSES.md"}
	if t.goos == "windows" {
		writeZip(filepath.Join(out, fmt.Sprintf("durin-%s-%s.zip", v, t.name())), stage, files)
	} else {
		writeTarGz(filepath.Join(out, fmt.Sprintf("durin-%s-%s.tar.gz", v, t.name())), stage, files)
	}
	if t.goos == "linux" {
		debian(out, stage, t, v)
	}
	npmPlatform(out, stage, t, v)
	fmt.Printf("Release packages for %s created in %s\n", t.name(), out)
}

func copyFile(from, to string, mode fs.FileMode) {
	data, err := os.ReadFile(from)
	check(err)
	check(os.MkdirAll(filepath.Dir(to), 0o755))
	check(os.WriteFile(to, data, mode))
}

func fileMode(name string) int64 {
	if name == "durin" || name == "durin.exe" || strings.HasSuffix(name, "/durin") {
		return 0o755
	}
	return 0o644
}

func writeTarGz(path, root string, names []string) {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		check(err)
		check(tw.WriteHeader(&tar.Header{Name: name, Mode: fileMode(name), Size: int64(len(data)), Format: tar.FormatPAX}))
		_, err = tw.Write(data)
		check(err)
	}
	check(tw.Close())
	check(gz.Close())
	check(os.WriteFile(path, buffer.Bytes(), 0o644))
}

func writeZip(path, root string, names []string) {
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		check(err)
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(fs.FileMode(fileMode(name)))
		w, err := zw.CreateHeader(header)
		check(err)
		_, err = w.Write(data)
		check(err)
	}
	check(zw.Close())
	check(os.WriteFile(path, buffer.Bytes(), 0o644))
}

// debian builds durin-cli_<version>_<arch>.deb with dpkg-deb. The binary is static; the
// OS credential store is the Secret Service on the user's D-Bus session.
func debian(out, stage string, t target, v string) {
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[t.goarch]
	root := filepath.Join(".release-stage", "deb-"+t.name())
	check(os.RemoveAll(root))
	copyFile(filepath.Join(stage, "durin"), filepath.Join(root, "usr", "bin", "durin"), 0o755)
	copyFile("LICENSE", filepath.Join(root, "usr", "share", "doc", "durin-cli", "copyright"), 0o644)
	copyFile(filepath.Join(stage, "THIRD_PARTY_LICENSES.md"), filepath.Join(root, "usr", "share", "doc", "durin-cli", "THIRD_PARTY_LICENSES.md"), 0o644)
	control := fmt.Sprintf(`Package: durin-cli
Version: %s
Architecture: %s
Maintainer: Durin <support@getdurin.com>
Depends: dbus-user-session
Suggests: gnome-keyring
Section: utils
Priority: optional
Homepage: https://github.com/accessdurin/cli
Description: Durin organization tools and coding-agent setup
 Requires an unlocked Secret Service credential store (for example GNOME Keyring).
`, v, arch)
	check(os.MkdirAll(filepath.Join(root, "DEBIAN"), 0o755))
	check(os.WriteFile(filepath.Join(root, "DEBIAN", "control"), []byte(control), 0o644))
	run(".", nil, "dpkg-deb", "--root-owner-group", "--build", root, filepath.Join(out, fmt.Sprintf("durin-cli_%s_%s.deb", v, arch)))
	check(os.RemoveAll(root))
}

// npmPlatform packs @accessdurin/cli-<os>-<arch>, which holds only the binary; the launcher
// package selects it through optionalDependencies.
func npmPlatform(out, stage string, t target, v string) {
	dir := filepath.Join(".release-stage", "npm-"+t.name())
	check(os.RemoveAll(dir))
	copyFile(filepath.Join(stage, t.exe()), filepath.Join(dir, "bin", t.exe()), 0o755)
	copyFile("LICENSE", filepath.Join(dir, "LICENSE"), 0o644)
	copyFile(filepath.Join(stage, "THIRD_PARTY_LICENSES.md"), filepath.Join(dir, "THIRD_PARTY_LICENSES.md"), 0o644)
	manifest := map[string]any{
		"name":        "@accessdurin/cli-" + t.nodeOS + "-" + t.nodeArch,
		"version":     v,
		"description": "The durin CLI executable for " + t.nodeOS + " " + t.nodeArch + ". Install @accessdurin/cli instead.",
		"license":     "MIT",
		"repository":  map[string]string{"type": "git", "url": "git+https://github.com/accessdurin/cli.git"},
		"os":          []string{t.nodeOS},
		"cpu":         []string{t.nodeArch},
		"files":       []string{"bin", "LICENSE", "THIRD_PARTY_LICENSES.md"},
		"publishConfig": map[string]string{
			"access": "public", "registry": "https://registry.npmjs.org/",
		},
	}
	writeJSON(filepath.Join(dir, "package.json"), manifest)
	npmPack(dir, out)
}

func writeJSON(path string, value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	check(err)
	check(os.MkdirAll(filepath.Dir(path), 0o755))
	check(os.WriteFile(path, append(data, '\n'), 0o644))
}

func npmPack(dir, out string) {
	absolute, err := filepath.Abs(out)
	check(err)
	cmd := exec.Command("npm", "pack", "--ignore-scripts", "--pack-destination", absolute)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "npm", "pack", "--ignore-scripts", "--pack-destination", absolute)
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
	}
	check(cmd.Run())
}

// metadata writes what spans platforms: the npm launcher package, the Homebrew formula,
// the Windows installer and SHA256SUMS over every asset.
func metadata(out string, writeFormula, checkFormula bool) {
	v := version()
	npmLauncher(out, v)
	formula := homebrewFormula(out, v)
	check(os.WriteFile(filepath.Join(out, "durin.rb"), []byte(formula), 0o644))
	if writeFormula {
		check(os.WriteFile(filepath.Join("Formula", "durin.rb"), []byte(formula), 0o644))
	}
	if checkFormula {
		committed, err := os.ReadFile(filepath.Join("Formula", "durin.rb"))
		check(err)
		if string(committed) != formula {
			fail("Homebrew formula differs from this release. Regenerate it before tagging.")
		}
	}
	copyFile(filepath.Join("packaging", "install-windows.ps1"), filepath.Join(out, "install-windows.ps1"), 0o644)
	entries, err := os.ReadDir(out)
	check(err)
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != "SHA256SUMS" && entry.Name() != "SHA256SUMS.asc" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, name := range names {
		fmt.Fprintf(&sums, "%s  %s\n", digestFile(filepath.Join(out, name)), name)
	}
	check(os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sums.String()), 0o644))
	fmt.Printf("Generated release metadata for %d assets.\n", len(names))
}

func digestFile(path string) string {
	file, err := os.Open(path)
	check(err)
	defer file.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	check(err)
	return hex.EncodeToString(hash.Sum(nil))
}

// npmLauncher packs @accessdurin/cli: a small Node launcher (npm/cli/bin/durin.js) that runs
// the platform package's binary, so `npx @accessdurin/cli` keeps working.
func npmLauncher(out, v string) {
	dir := filepath.Join(".release-stage", "npm-launcher")
	check(os.RemoveAll(dir))
	copyFile(filepath.Join("npm", "cli", "bin", "durin.js"), filepath.Join(dir, "bin", "durin.js"), 0o755)
	copyFile("README.md", filepath.Join(dir, "README.md"), 0o644)
	copyFile("LICENSE", filepath.Join(dir, "LICENSE"), 0o644)
	optional := map[string]string{}
	for _, t := range targets {
		optional["@accessdurin/cli-"+t.nodeOS+"-"+t.nodeArch] = v
	}
	manifest := map[string]any{
		"name":                 "@accessdurin/cli",
		"version":              v,
		"description":          "Durin organization onboarding, governed tools, and MCP bridge CLI",
		"license":              "MIT",
		"bin":                  map[string]string{"durin": "bin/durin.js"},
		"files":                []string{"bin", "README.md", "LICENSE"},
		"engines":              map[string]string{"node": ">=18"},
		"optionalDependencies": optional,
		"repository":           map[string]string{"type": "git", "url": "git+https://github.com/accessdurin/cli.git"},
		"homepage":             "https://github.com/accessdurin/cli#readme",
		"bugs":                 map[string]string{"url": "https://github.com/accessdurin/cli/issues"},
		"publishConfig":        map[string]string{"access": "public", "registry": "https://registry.npmjs.org/"},
	}
	writeJSON(filepath.Join(dir, "package.json"), manifest)
	npmPack(dir, out)
}

// homebrewFormula pins each macOS and Linux archive by SHA-256.
func homebrewFormula(out, v string) string {
	sum := func(t target) string {
		return digestFile(filepath.Join(out, fmt.Sprintf("durin-%s-%s.tar.gz", v, t.name())))
	}
	url := func(t target) string {
		return fmt.Sprintf("https://github.com/accessdurin/cli/releases/download/v%s/durin-%s-%s.tar.gz", v, v, t.name())
	}
	byName := map[string]target{}
	for _, t := range targets {
		byName[t.name()] = t
	}
	block := func(name, indent string) string {
		t := byName[name]
		return fmt.Sprintf("%surl \"%s\"\n%ssha256 \"%s\"\n", indent, url(t), indent, sum(t))
	}
	return fmt.Sprintf(`class Durin < Formula
  desc "Organization tools and coding-agent setup through Durin"
  homepage "https://github.com/accessdurin/cli"
  version "%s"
  license "MIT"

  on_macos do
    on_arm do
%s    end
    on_intel do
%s    end
  end

  on_linux do
    on_arm do
%s    end
    on_intel do
%s    end
  end

  def install
    bin.install "durin"
    doc.install "THIRD_PARTY_LICENSES.md"
  end

  test do
    assert_match "durin", shell_output("#{bin}/durin --help")
  end
end
`, v, block("darwin-arm64", "      "), block("darwin-x64", "      "), block("linux-arm64", "      "), block("linux-x64", "      "))
}

// thirdPartyNotices concatenates the license files of every non-standard-library module the
// binary links for goos/goarch, plus the notices of code ported into this repository.
func thirdPartyNotices(goos, goarch string) string {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}", "./cmd/durin")
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
	if goos == "darwin" {
		cmd.Env = append(cmd.Env, "CGO_ENABLED=1")
	}
	output, err := cmd.Output()
	check(err)
	seen := map[string]bool{}
	var modules [][3]string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[0] == "github.com/accessdurin/cli" || seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		modules = append(modules, [3]string{fields[0], fields[1], fields[2]})
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i][0] < modules[j][0] })
	var b strings.Builder
	b.WriteString("# Third-party notices\n\nThe durin executable includes the following open-source software.\n")
	for _, module := range modules {
		fmt.Fprintf(&b, "\n## %s %s\n\n", module[0], module[1])
		b.WriteString(licenseText(module[2]))
	}
	ported, _ := filepath.Glob(filepath.Join("internal", "*", "LICENSE-*"))
	sort.Strings(ported)
	for _, path := range ported {
		data, err := os.ReadFile(path)
		check(err)
		fmt.Fprintf(&b, "\n## Ported code: %s\n\n%s", filepath.ToSlash(path), ensureNewline(string(data)))
	}
	return b.String()
}

var licenseFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|patents)(\.(md|txt))?$`)

func licenseText(dir string) string {
	entries, err := os.ReadDir(dir)
	check(err)
	var b strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !licenseFile.MatchString(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		check(err)
		fmt.Fprintf(&b, "```text\n%s```\n", ensureNewline(string(data)))
	}
	if b.Len() == 0 {
		fail("no license file found in " + dir)
	}
	return b.String()
}

func ensureNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
