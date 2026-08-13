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
# 注意：必须带 -tags fts5（go-sqlite3 的 FTS5 模块在 fts5 build tag 下启用），否则运行时报 no such module: fts5。
$buildDir = "build"
$qtBin    = "C:\msys64\ucrt64\bin"
$qtPlugin = "C:\msys64\ucrt64\share\qt6\plugins"

New-Item -ItemType Directory -Force -Path $buildDir | Out-Null

# 语义检索 DLL：确保 llama.cpp 静态库（缺则构建，优先 Vulkan backend，自动回退 CPU）、
# 编译 llama_bridge.dll 并复制到 build/。失败仅警告（未配置模型时不加载 DLL，不影响主程序）。
function Invoke-BuildBridge {
    $llama = Join-Path $PSScriptRoot "third_party\llama.cpp"
    $libA  = Join-Path $llama "build\src\libllama.a"
    if (-not (Test-Path $libA)) {
        Write-Output "==> building llama.cpp static lib (first time, takes minutes)"
        $cmake = "C:\Program Files\Python311\Lib\site-packages\cmake\data\bin\cmake.exe"
        if (-not (Test-Path $cmake)) {
            Write-Warning "cmake not found; skip llama_bridge.dll"
            return $false
        }
        $env:CMAKE_PREFIX_PATH = "C:\msys64\ucrt64"
        & $cmake -S $llama -B (Join-Path $llama "build") -G Ninja -DCMAKE_BUILD_TYPE=Release `
            -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_CURL=OFF -DGGML_VULKAN=ON
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "Vulkan configure failed; retry CPU-only"
            & $cmake -S $llama -B (Join-Path $llama "build") -G Ninja -DCMAKE_BUILD_TYPE=Release `
                -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_CURL=OFF
            if ($LASTEXITCODE -ne 0) {
                Write-Warning "llama static lib build failed; skip llama_bridge.dll"
                return $false
            }
        }
        & $cmake --build (Join-Path $llama "build") --target llama
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "llama static lib build failed; skip llama_bridge.dll"
            return $false
        }
    }
    Write-Output "==> building llama_bridge.dll (optional)"
    & powershell -File (Join-Path $PSScriptRoot "third_party\llama-bridge\build.ps1")
    if ($LASTEXITCODE -ne 0) {
        Write-Warning "llama_bridge.dll build failed; skip"
        return $false
    }
    $dll = Join-Path $PSScriptRoot "third_party\llama-bridge\llama_bridge.dll"
    if (Test-Path $dll) {
        Copy-Item $dll $buildDir -Force
        Write-Output "==> llama_bridge.dll copied to build/"
    }
    return $true
}
Invoke-BuildBridge

Write-Output "==> go build -tags fts5 -o build/agent-gui.exe"
go build -tags fts5 -o (Join-Path $buildDir "agent-gui.exe") ./cmd/agent-gui
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
