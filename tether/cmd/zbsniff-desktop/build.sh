#!/bin/sh
# Build the Zigbee Sniffer desktop app (macOS): the Wails shell plus the
# zbsniff host it runs, bundled together in build/bin/zbsniff-desktop.app.
#   ./build.sh            # this Mac's architecture
#   ./build.sh -platform darwin/universal
set -e
cd "$(dirname "$0")"
WAILS=$(command -v wails || echo "$(go env GOPATH)/bin/wails")

"$WAILS" build -clean -skipbindings "$@"

APP=build/bin/zbsniff-desktop.app
# Same target as the shell, so the bundled host matches its architecture.
case "$*" in
  *darwin/universal*)
    GOOS=darwin GOARCH=arm64 go build -o /tmp/zbsniff-arm64 ../zbsniff
    GOOS=darwin GOARCH=amd64 go build -o /tmp/zbsniff-amd64 ../zbsniff
    lipo -create -output "$APP/Contents/MacOS/zbsniff" /tmp/zbsniff-arm64 /tmp/zbsniff-amd64
    rm -f /tmp/zbsniff-arm64 /tmp/zbsniff-amd64 ;;
  *) go build -o "$APP/Contents/MacOS/zbsniff" ../zbsniff ;;
esac

# Adding a binary invalidates the bundle's ad-hoc signature — re-sign it.
codesign --force --deep --sign - "$APP"
echo "built $APP"
