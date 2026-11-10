class Lighthouse < Formula
  desc "Local memory for coding agents"
  homepage "https://lighthouse.ovenplatform.com"
  url "https://github.com/paulwritescode/lighthouse.git",
      tag:      "v0.4.3",
      revision: "38b1cf76b1603683cf1097dbf015ed56d0e2c68e"
  license "MIT"

  depends_on "go" => :build

  def install
    ENV["GOBIN"] = bin.to_s
    system "go", "install", "github.com/paulwritescode/lighthouse/cmd/lighthouse@v#{version}"
  end

  test do
    assert_match "Lighthouse v0.4.3", shell_output("#{bin}/lighthouse version")
    assert_match "Commands:", shell_output("#{bin}/lighthouse --help")
  end
end
