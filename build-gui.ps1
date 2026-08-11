# 编译 agent-gui（miqt + Qt6，CGO）并把 Qt 运行库部署到 build/ 目录。
# 用法：
#   powershell -File build-gui.ps1          # 仅编译+部署
#   powershell -File build-gui.ps1 -Run     # 编译部署后启动 build/agent-gui.exe
param([switch]$Run)
$ErrorActionPreference = "Stop"

$env:Path = "C:\msys64\ucrt64\bin;C:\Program Files\Go\bin;" + $env:Path
$env:PKG_CONFIG_PATH = "C:\msys64\ucrt64\lib\pkgconfig"
$env:CGO_ENABLED = "1"

# build/ 是部署目录：exe、Qt DLL、插件、config.json、sessions/、memory/ 都在这里（与 exe 同目录，程序按 exe 目录定位数据文件）。
# 脚本约定在仓库根目录运行（README 中即为 powershell -File build-gui.ps1）。
$buildDir = "build"
$qtBin    = "C:\msys64\ucrt64\bin"
$qtPlugin = "C:\msys64\ucrt64\share\qt6\plugins"

New-Item -ItemType Directory -Force -Path $buildDir | Out-Null

Write-Output "==> go build -o build/agent-gui.exe"
go build -o (Join-Path $buildDir "agent-gui.exe") ./cmd/agent-gui
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Output "==> agent-gui.exe built OK"

Write-Output "==> deploy Qt runtime to build/"
Copy-Item (Join-Path $qtBin "Qt6*.dll") $buildDir -Force
Copy-Item (Join-Path $qtBin "lib*.dll") $buildDir -Force
Copy-Item (Join-Path $qtBin "zlib1.dll") $buildDir -Force -ErrorAction SilentlyContinue
foreach ($cat in @("platforms", "styles", "imageformats", "tls", "networkinformation", "generic")) {
    $src = Join-Path $qtPlugin $cat
    if (Test-Path $src) {
        Copy-Item $src $buildDir -Recurse -Force
    }
}
Write-Output "==> build/agent-gui.exe ready"

if ($Run) {
    Write-Output "==> launching build/agent-gui.exe"
    & (Join-Path $buildDir "agent-gui.exe")
}
