class Clawctl < Formula
  desc "kubectl-style wrapper for the openclaw gateway"
  homepage "https://github.com/tomstagl/clawctl"
  url "https://github.com/tomstagl/clawctl/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "REPLACE_ON_RELEASE"
  license "MIT"
  version "0.1.0"

  depends_on "curl"
  depends_on "openssl@3"
  depends_on "jq" => :recommended

  def install
    bin.install "oc"
  end

  def caveats
    <<~EOS
      Set the gateway URL once:
        export CLAWCTL_HOST=http://your-openclaw-host:18789

      Store the bearer token in Keychain:
        security add-generic-password -s openclaw-gateway-token -a "$USER" -w '<token>'

      Verify:
        clawctl health
    EOS
  end

  test do
    output = shell_output("#{bin}/oc 2>&1", 2)
    assert_match "openclaw client", output.downcase
  end
end
