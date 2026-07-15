$ErrorActionPreference = "Stop"

$Repo = if ($env:AGEMUX_REPO) { $env:AGEMUX_REPO } else { "Humelo/agemux" }
$Ref = if ($env:AGEMUX_REF) { $env:AGEMUX_REF } else { "v0.1.10" }
$BinDir = if ($env:AGEMUX_BIN_DIR) { $env:AGEMUX_BIN_DIR } else { Join-Path $HOME ".local\bin" }
$InstallClaudeShim = $false
$InstallCodexLb = $false
$CodexLbSpec = if ($env:AGEMUX_CODEX_LB_SPEC) { $env:AGEMUX_CODEX_LB_SPEC } else { "codex-lb" }

for ($i = 0; $i -lt $args.Count; $i++) {
  switch ($args[$i]) {
    "--install-claude-shim" { $InstallClaudeShim = $true }
    "--with-codex-lb" { $InstallCodexLb = $true }
    "--no-codex-lb" { $InstallCodexLb = $false }
    "--codex-lb-spec" {
      if (($i + 1) -ge $args.Count) {
        throw "--codex-lb-spec requires a value"
      }
      $i++
      $CodexLbSpec = $args[$i]
    }
    default {
      throw "unknown option: $($args[$i])"
    }
  }
}

function Ensure-Uv {
  if (Get-Command uv -ErrorAction SilentlyContinue) {
    return
  }
  Write-Host "Installing uv for codex-lb..."
  powershell -ExecutionPolicy ByPass -c "irm https://astral.sh/uv/install.ps1 | iex"
  $UserBin = Join-Path $HOME ".local\bin"
  $env:Path = "$UserBin;$env:Path"
  if (-not (Get-Command uv -ErrorAction SilentlyContinue)) {
    throw "uv install finished, but uv is not on PATH. Add $UserBin to PATH and rerun."
  }
}

New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
$TempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("agemux-install-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $TempDir | Out-Null

$Arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()) {
  "x64" { "amd64" }
  "arm64" { "arm64" }
  default { throw "unsupported architecture: $([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)" }
}
$Version = $Ref.TrimStart("v")
$Asset = "agemux_${Version}_windows_${Arch}.zip"
$AssetUrl = "https://github.com/$Repo/releases/download/$Ref/$Asset"
$ZipPath = Join-Path $TempDir $Asset

Write-Host "Downloading Agent Multiplexer $Ref for windows/$Arch..."
Invoke-WebRequest -Uri $AssetUrl -OutFile $ZipPath
Expand-Archive -Force -Path $ZipPath -DestinationPath $TempDir

$TempTarget = Join-Path $TempDir "agemux.exe"
if (-not (Test-Path $TempTarget)) {
  throw "release asset missing executable: agemux.exe"
}
Move-Item -Force $TempTarget (Join-Path $BinDir "agemux.exe")
Remove-Item -Recurse -Force $TempDir

if ($InstallClaudeShim) {
  & (Join-Path $BinDir "agemux.exe") claude-accounts install-shim --force --bin-dir $BinDir
}

if ($InstallCodexLb) {
  Ensure-Uv
  Write-Host "Installing latest codex-lb via uv..."
  uv tool install --upgrade --force $CodexLbSpec
  $UvToolBin = (& uv tool dir --bin).Trim()
  if ($UvToolBin) {
    $env:Path = "$UvToolBin;$env:Path"
  }
} else {
  $UvToolBin = ""
}

Write-Host "Installed Agent Multiplexer:"
Write-Host "  $(Join-Path $BinDir 'agemux.exe')"
if ($InstallCodexLb) {
  Write-Host "  codex-lb: $(if (Get-Command codex-lb -ErrorAction SilentlyContinue) { (Get-Command codex-lb).Source } else { 'installed by uv; ensure ~/.local/bin is on PATH' })"
} else {
  Write-Host "  codex-lb: not installed by agemux installer; pass --with-codex-lb to opt in"
}
Write-Host ""
Write-Host "Make sure this directory is on PATH:"
Write-Host "  $BinDir"
if ($UvToolBin) {
  Write-Host "  $UvToolBin"
}
