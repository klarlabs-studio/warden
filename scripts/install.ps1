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
$archive = "warden_${ver}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/$version"

New-Item -ItemType Directory -Force -Path $binDir | Out-Null

# Use an unpredictable temp name (avoids a fixed, hijackable %TEMP%\warden.zip);
# give it a .zip extension so Expand-Archive accepts it.
$tmp = New-TemporaryFile
$zip = "$($tmp.FullName).zip"
$sums = "$($tmp.FullName).checksums"
try {
  Write-Host "downloading warden $version (windows/$arch)…"
  Invoke-WebRequest -Uri "$base/$archive" -OutFile $zip
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sums

  # Fail closed: verify the archive against the published checksum before extract.
  $line = Select-String -Path $sums -Pattern ([regex]::Escape($archive)) | Select-Object -First 1
  if (-not $line) { throw "no checksum for $archive in checksums.txt" }
  $want = (($line.Line -split '\s+') | Where-Object { $_ })[0]
  $got = (Get-FileHash -Path $zip -Algorithm SHA256).Hash
  if ($got.ToLower() -ne $want.ToLower()) {
    throw "checksum mismatch for $archive (expected $want, got $got)"
  }

  Expand-Archive -Path $zip -DestinationPath $binDir -Force
}
finally {
  Remove-Item -Force -ErrorAction SilentlyContinue $tmp.FullName, $zip, $sums
}

Write-Host "installed: $binDir\warden.exe"
Write-Host "add to PATH:  `$env:Path = `"$binDir;`$env:Path`""
