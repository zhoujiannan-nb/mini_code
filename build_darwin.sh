#!/bin/sh
# Build the macOS desktop shell (WKWebView, no browser) + optional .app bundle.
# Run this ON a Mac (cgo + Xcode Command Line Tools required).
#
#   sh build_darwin.sh            # binary only: build/darwin-arm64/mini_code
#   sh build_darwin.sh app        # + mini_code.app bundle (ad-hoc codesigned)
set -e

APP_NAME=mini_code
OUT=build/darwin-arm64

export CGO_ENABLED=1
mkdir -p "$OUT"

echo "Building $APP_NAME (darwin/$(uname -m), desktop shell)..."
go build -tags desktop_darwin,desktop_default -ldflags="-s -w" -o "$OUT/$APP_NAME" .
echo "  Success: $OUT/$APP_NAME"

if [ "$1" = "app" ]; then
    APPDIR="$OUT/${APP_NAME}.app"
    echo "Creating ${APP_NAME}.app bundle..."
    rm -rf "$APPDIR"
    mkdir -p "$APPDIR/Contents/MacOS" "$APPDIR/Contents/Resources"
    cp "$OUT/$APP_NAME" "$APPDIR/Contents/MacOS/$APP_NAME"

    cat > "$APPDIR/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>            <string>Mini Code</string>
    <key>CFBundleDisplayName</key>     <string>Mini Code</string>
    <key>CFBundleIdentifier</key>      <string>com.user.minicode</string>
    <key>CFBundleVersion</key>         <string>1.0.0</string>
    <key>CFBundleShortVersionString</key><string>1.0.0</string>
    <key>CFBundleExecutable</key>      <string>mini_code</string>
    <key>CFBundlePackageType</key>     <string>APPL</string>
    <key>NSHighResolutionCapable</key> <true/>
    <key>LSMinimumSystemVersion</key>  <string>10.15</string>
</dict>
</plist>
PLIST

    # Best-effort .icns from the bundled PNG (needs sips/iconutil, always
    # present on macOS). Skip silently if anything fails.
    if [ -f mini_code.png ] && command -v sips >/dev/null 2>&1; then
        ICONSET="$OUT/_mini_code.iconset"
        rm -rf "$ICONSET"
        mkdir -p "$ICONSET"
        for s in 16 32 64 128 256 512; do
            sips -z $s $s mini_code.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null 2>&1 || true
        done
        for s in 16 32 64 128 256; do
            ss=$((s * 2))
            sips -z $ss $ss mini_code.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null 2>&1 || true
        done
        if iconutil -c icns "$ICONSET" -o "$APPDIR/Contents/Resources/mini_code.icns" >/dev/null 2>&1; then
            /usr/libexec/PlistBuddy -c "Add :CFBundleIconFile string mini_code" "$APPDIR/Contents/Info.plist" 2>/dev/null || true
            rm -rf "$ICONSET"
        fi
    fi

    codesign --force --deep -s - "$APPDIR" >/dev/null 2>&1 || \
        echo "  (codesign failed or unavailable; Gatekeeper may ask on first open)"
    echo "  Success: $APPDIR"
fi

echo
echo "Done."
echo "  Double-click $OUT/$APP_NAME (or the .app) to launch the desktop shell."
echo "  Plain web mode is still available: $OUT/$APP_NAME web"
