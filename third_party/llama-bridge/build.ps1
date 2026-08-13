# Build the llama.cpp embedding bridge as a shared library (llama_bridge.dll).
# Used only when semantic search (embedding.model in config.json / EMBED_MODEL) is
# configured; the Go side loads this DLL on demand via syscall and skips it
# entirely otherwise (FTS5 fallback).
#
# Prereq: static libs under third_party/llama.cpp/build (see README "build llama.cpp").
#   - if build/ggml/src/ggml-vulkan.a exists (built with -DGGML_VULKAN=ON), the DLL
#     is linked with the Vulkan backend; llama.cpp auto-falls-back to CPU at runtime
#     when Vulkan is unavailable.
# Usage: powershell -File third_party/llama-bridge/build.ps1
$ErrorActionPreference = "Stop"

$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)  # repo root
$llama = Join-Path $root "third_party\llama.cpp"
$src = Join-Path $PSScriptRoot "bridge.c"
$out = Join-Path $PSScriptRoot "llama_bridge.dll"

$gcc = (Get-Command gcc -ErrorAction SilentlyContinue)
if (-not $gcc) {
    Write-Error "gcc not found (need MinGW/MSYS2 toolchain)"
}

$args = @(
    "-shared", "-O2", "-o", $out, $src,
    "-I$llama\include", "-I$llama\ggml\include",
    "-Wl,--start-group",
    "$llama\build\src\libllama.a",
    "$llama\build\ggml\src\ggml-cpu.a",
    "$llama\build\ggml\src\ggml.a",
    "$llama\build\ggml\src\ggml-base.a"
)

# Vulkan backend (built with -DGGML_VULKAN=ON) - link it in if present.
# Runtime: llama.cpp auto-selection picks the GPU backend and falls back to CPU
# when Vulkan is unavailable (no driver/device); vulkan-1.dll ships with Win10+.
$vulkan = "$llama\build\ggml\src\ggml-vulkan\ggml-vulkan.a"
if (Test-Path $vulkan) {
    $args += $vulkan
}
$args += "-Wl,--end-group"
if (Test-Path $vulkan) {
    $args += "-lvulkan-1"
}
$args += "-lstdc++", "-lm", "-fopenmp"

& $gcc.Source @args

if ($LASTEXITCODE -ne 0) {
    Write-Error "compile failed (exit=$LASTEXITCODE)"
}
Write-Host "generated: $out"
