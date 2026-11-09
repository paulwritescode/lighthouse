class Lighthouse < Formula
  desc "Local memory for coding agents"
  homepage "https://lighthouse.ovenplatform.com"
  url "https://github.com/paulwritescode/lighthouse.git",
      tag:      "v0.4.2",
      revision: "f5e5420d81230a392b844c14546acee792a9521a"
  license "MIT"

  depends_on "go" => :build

  def install
    ENV["GOBIN"] = bin.to_s
    system "go", "install", "github.com/paulwritescode/lighthouse/cmd/lighthouse@v#{version}"
  end

  test do
    assert_match "Lighthouse v0.4.2", shell_output("#{bin}/lighthouse version")
    assert_match "Commands:", shell_output("#{bin}/lighthouse --help")
  end
end
