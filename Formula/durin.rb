class Durin < Formula
  desc "Organization tools and coding-agent setup through Durin"
  homepage "https://github.com/accessdurin/cli"
  url "https://github.com/accessdurin/cli/releases/download/v1.1.0/durin-cli-1.1.0.tgz"
  sha256 "feafcf63e5a02c60b01a319841e83618149ccdf8f9df774f3c90112ae1c76ec0"
  license "MIT"

  depends_on "node@24"

  def install
    system "npm", "install", *std_npm_args, "--ignore-scripts"
    bin.write_env_script libexec/"bin/durin", PATH: "#{Formula["node@24"].opt_bin}:$PATH"
  end

  test do
    assert_match "durin", shell_output("#{bin}/durin --help")
  end
end
