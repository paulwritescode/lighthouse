class Lighthouse < Formula
  desc "Local memory for coding agents"
  homepage "https://lighthouse.ovenplatform.com"
  url "https://github.com/paulwritescode/lighthouse.git",
      tag:      "v0.5.0",
      revision: "77e8603a68e9d7e752e3ab0525c3a96627511ae2"
  license "MIT"

  depends_on "go" => :build

  def install
    ENV["GOBIN"] = bin.to_s
    system "go", "install", "github.com/paulwritescode/lighthouse/cmd/lighthouse@v#{version}"
  end

  test do
    assert_match "Lighthouse v0.5.0", shell_output("#{bin}/lighthouse version")
    assert_match "Commands:", shell_output("#{bin}/lighthouse --help")
  end
end
