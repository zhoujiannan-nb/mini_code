# mini_code 编译说明

## Windows 平台

需要:Go、WebView2 运行时(Win10/Win11 通常自带)

执行:

```bat
build.bat
```

产物:`build\windows\mini_code.exe`(带壳,双击即桌面应用;加 `web` 参数为纯 web 服务)

## macOS 平台

需要:Go;带壳版还需 Xcode Command Line Tools(`xcode-select --install`)

纯 web 版(任意机器上交叉编译即可):

```bat
build.bat mac
```

带壳版(必须在 Mac 上执行):

```sh
sh build_darwin.sh        # 产物:build/darwin-arm64/mini_code
sh build_darwin.sh app    # 另打 mini_code.app(图标 + ad-hoc 签名)
```
