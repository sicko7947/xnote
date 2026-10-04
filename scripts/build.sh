#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
plist_path="$(pwd)/scripts/Info.plist"
CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -extldflags '-Wl,-sectcreate,__TEXT,__info_plist,$plist_path'" -o dist/xnote ./cmd/xnote
codesign --force --sign - --identifier ai.xnote.cli dist/xnote
shasum -a 256 dist/xnote > dist/xnote.sha256
