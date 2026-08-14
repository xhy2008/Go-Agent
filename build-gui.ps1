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

Write-Output "==> go build -tags fts5 -ldflags `"-s -w`" -o build/agent-gui.exe"
# -s -w：剥离 Go/调试符号。miqt 的 C++ 绑定按 -g 编译，不带此标志的 exe 会携带
# ~300MB DWARF 调试信息（实测 353MB → 60MB）。
go build -tags fts5 -ldflags "-s -w" -o (Join-Path $buildDir "agent-gui.exe") ./cmd/agent-gui
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Output "==> agent-gui.exe built OK"

# ---- Qt 运行库部署：只拷贝 exe 实际依赖的 DLL 与平台插件，不再整目录拷贝 ----
# 递归解析导入表：从 exe/插件出发，凡在 MSYS2 bin 下能找到的 DLL 一并拷贝并继续解析其
# 依赖；找不到的（系统 DLL）跳过。旧版本残留的无关 DLL（Qt6Network、libpython 等）最后清除。
function Copy-WithDeps {
    param(
        [string]$File,    # 起始文件（exe 或插件）
        [string]$Dst,     # 目标目录
        [string]$Bin,     # MSYS2 bin 目录（DLL 来源）
        [hashtable]$Seen  # 已处理集合（键 = 小写文件名）
    )
    $imports = & objdump -p $File 2>$null |
        Select-String 'DLL Name: (\S+)' |
        ForEach-Object { $_.Matches[0].Groups[1].Value }
    foreach ($dll in $imports) {
        $key = $dll.ToLower()
        if ($Seen.ContainsKey($key)) { continue }
        $Seen[$key] = $true
        $src = Join-Path $Bin $dll
        if (-not (Test-Path $src)) { continue } # 系统 DLL → 跳过
        Copy-Item $src (Join-Path $Dst $dll) -Force
        Copy-WithDeps $src $Dst $Bin $Seen
    }
}

Write-Output "==> deploy Qt runtime to build/ (dependency-resolved)"
# 清掉旧插件目录（platforms 重建），保留运行数据目录（models/ sessions/ skills/ 等）。
foreach ($cat in @("platforms", "styles", "imageformats", "tls", "networkinformation", "generic")) {
    Remove-Item (Join-Path $buildDir $cat) -Recurse -Force -ErrorAction SilentlyContinue
}
$seen = @{}
Copy-WithDeps (Join-Path $buildDir "agent-gui.exe") $buildDir $qtBin $seen
# 平台插件 qwindows.dll（Qt6Gui 加载 Windows 平台必需），并解析其依赖。
$platDir = Join-Path $buildDir "platforms"
New-Item -ItemType Directory -Force -Path $platDir | Out-Null
$qw = Join-Path $qtPlugin "platforms\qwindows.dll"
Copy-Item $qw (Join-Path $platDir "qwindows.dll") -Force
Copy-WithDeps $qw $buildDir $qtBin $seen
# 样式插件 qmodernwindowsstyle.dll：Qt 6.7+ 在 Windows 上默认采用 modernwindows 圆角风格，
# 缺少该插件会回退为原生样式，造成 UI 风格不一致。
$stylesDir = Join-Path $buildDir "styles"
New-Item -ItemType Directory -Force -Path $stylesDir | Out-Null
Get-ChildItem (Join-Path $qtPlugin "styles") -Filter *.dll -ErrorAction SilentlyContinue | ForEach-Object {
    Copy-Item $_.FullName (Join-Path $stylesDir $_.Name) -Force
    Copy-WithDeps $_.FullName $buildDir $qtBin $seen
}
# 清除解析集之外的多余 DLL（保留 llama_bridge.dll）。
$resolved = New-Object 'System.Collections.Generic.HashSet[string]'
foreach ($k in $seen.Keys) { [void]$resolved.Add($k) }
[void]$resolved.Add("llama_bridge.dll")
Get-ChildItem $buildDir -Filter *.dll | ForEach-Object {
    if (-not $resolved.Contains($_.Name.ToLower())) {
        Remove-Item $_.FullName -Force
    }
}
Write-Output "==> build/agent-gui.exe ready"

if ($Run) {
    Write-Output "==> launching build/agent-gui.exe"
    & (Join-Path $buildDir "agent-gui.exe")
}
