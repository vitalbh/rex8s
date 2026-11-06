#!/bin/sh
# Prints the Homebrew formula for a release, from its checksums.txt.
#
#   scripts/brew-formula.sh v0.1.1 dist/checksums.txt > Formula/rex8s.rb
set -eu
tag=$1
sums=$2
version=${tag#v}
sha() { awk -v f="rex8s_$1.tar.gz" '$2 == f { print $1 }' "$sums"; }
url() { echo "https://github.com/vitalbh/rex8s/releases/download/$tag/rex8s_$1.tar.gz"; }
for p in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
  [ -n "$(sha $p)" ] || { echo "no checksum for $p in $sums" >&2; exit 1; }
done
cat <<RUBY
class Rex8s < Formula
  desc "Kubernetes in the Rex terminal: one cluster per pane, Flux built in"
  homepage "https://github.com/vitalbh/rex8s"
  version "$version"
  license "MIT"

  on_macos do
    on_arm do
      url "$(url darwin_arm64)"
      sha256 "$(sha darwin_arm64)"
    end
    on_intel do
      url "$(url darwin_amd64)"
      sha256 "$(sha darwin_amd64)"
    end
  end

  on_linux do
    on_arm do
      url "$(url linux_arm64)"
      sha256 "$(sha linux_arm64)"
    end
    on_intel do
      url "$(url linux_amd64)"
      sha256 "$(sha linux_amd64)"
    end
  end

  def install
    bin.install "rex8s", "rex8s-guard"
  end

  def caveats
    <<~EOS
      Install it into Rex (the k* commands, rex8s.lua and require("rex8s")):
        rex8s setup
      Then rex8s doctor checks everything. After brew upgrade rex8s, run
      rex8s setup again when rex8s doctor says rex8s.lua is outdated.
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/rex8s version")
  end
end
RUBY
