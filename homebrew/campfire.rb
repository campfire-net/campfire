class Campfire < Formula
  desc "Decentralized coordination protocol for autonomous agents"
  homepage "https://getcampfire.dev"
  version "0.1.0"
  license "Apache-2.0"

  on_macos do
    on_intel do
      url "https://github.com/campfire-net/campfire/releases/download/v0.1.0/cf_darwin_amd64.tar.gz"
      sha256 "760b195f09d39b7ee04c062438e7da21e37ed4f8dfb1ce24fffc2dc023a0a4d4"
    end

    on_arm do
      url "https://github.com/campfire-net/campfire/releases/download/v0.1.0/cf_darwin_arm64.tar.gz"
      sha256 "422b3f9b2dd1999f655e92ff3ec031ef24f1330e4b1281a04ba070cb59861caa"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/campfire-net/campfire/releases/download/v0.1.0/cf_linux_amd64.tar.gz"
      sha256 "36760587259c29b2f027f1fe3d44a17439631e83196e1e8b862aa60c97006879"
    end

    on_arm do
      url "https://github.com/campfire-net/campfire/releases/download/v0.1.0/cf_linux_arm64.tar.gz"
      sha256 "4677ab45b7b6d4366de8bf8d325edeb55c396cdcea3ca526bd361a22041cf9a5"
    end
  end

  def install
    bin.install "cf"
    bin.install "cf-mcp"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/cf --version 2>&1", 0)
    system "#{bin}/cf", "--help"
  end
end
