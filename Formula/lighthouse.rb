class Lighthouse < Formula
  desc "Local memory for coding agents"
  homepage "https://lighthouse.ovenplatform.com"
  url "https://github.com/paulwritescode/lighthouse.git",
      tag:      "v0.4.4",
      revision: "d763fadcd5761d21e5cd78e96117396e1b28abe4"
  license "MIT"

  depends_on "go" => :build

  def install
    ENV["GOBIN"] = bin.to_s
    system "go", "install", "github.com/paulwritescode/lighthouse/cmd/lighthouse@v#{version}"
  end

  test do
    assert_match "Lighthouse v0.4.4", shell_output("#{bin}/lighthouse version")
    assert_match "Commands:", shell_output("#{bin}/lighthouse --help")
  end
end
