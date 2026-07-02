# warden installer for Windows — no toolchain required.
#   irm https://raw.githubusercontent.com/klarlabs-studio/warden/main/scripts/install.ps1 | iex
$ErrorActionPreference = "Stop"

$repo = "klarlabs-studio/warden"
$version = if ($env:WARDEN_VERSION) { $env:WARDEN_VERSION } else { "latest" }
$binDir = if ($env:WARDEN_BIN_DIR) { $env:WARDEN_BIN_DIR } else { "$HOME\.warden\bin" }

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }

if ($version -eq "latest") {
  $rel = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
  $version = $rel.tag_name
}
$ver = $version.TrimStart("v")
$url = "https://github.com/$repo/releases/download/$version/warden_${ver}_windows_${arch}.zip"

New-Item -ItemType Directory -Force -Path $binDir | Out-Null
$zip = "$env:TEMP\warden.zip"
Write-Host "downloading warden $version (windows/$arch)…"
Invoke-WebRequest -Uri $url -OutFile $zip
Expand-Archive -Path $zip -DestinationPath $binDir -Force
Remove-Item $zip

Write-Host "installed: $binDir\warden.exe"
Write-Host "add to PATH:  `$env:Path = `"$binDir;`$env:Path`""
