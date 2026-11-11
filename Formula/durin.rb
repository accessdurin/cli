class Durin < Formula
  desc "Organization tools and coding-agent setup through Durin"
  homepage "https://github.com/accessdurin/cli"
  url "https://github.com/accessdurin/cli/releases/download/v1.1.0/durin-cli-1.1.0.tgz"
  sha256 "6188413b56f2f9ea94fe40cfc14ac976b341827f970e8dfa142137af6508631e"
  license "MIT"

  depends_on "node@24"

  def install
    system "npm", "install", *std_npm_args, "--ignore-scripts"
    (bin/"durin").write_env_script libexec/"bin/durin", PATH: "#{Formula["node@24"].opt_bin}:$PATH"
  end

  test do
    assert_match "durin", shell_output("#{bin}/durin --help")
  end
end
