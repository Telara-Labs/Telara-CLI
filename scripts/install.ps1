# Telara CLI installer for Windows
# Usage: irm https://get.telara.ai/windows | iex

param(
    [string]$Version = "",
    [string]$InstallDir = "$env:LOCALAPPDATA\telara\bin"
)

$ErrorActionPreference = "Stop"

$Binary = "telara.exe"

# Detect arch
$Arch = if ([System.Environment]::Is64BitOperatingSystem) { "amd64" } else {
    Write-Error "Unsupported architecture. Only 64-bit Windows is supported."
    exit 1
}

# Get latest version
if (-not $Version) {
    $Version = (Invoke-RestMethod "https://get.telara.ai/latest-version").Trim()
}

$VersionNum = $Version.TrimStart("v")
Write-Host "Installing telara $Version (windows/$Arch)..."

$Filename = "telara_${VersionNum}_windows_${Arch}.zip"
$Url = "https://get.telara.ai/download/$Version/$Filename"

# Download
$Tmp = [System.IO.Path]::GetTempPath() + [System.IO.Path]::GetRandomFileName()
New-Item -ItemType Directory -Path $Tmp | Out-Null

$ZipPath = Join-Path $Tmp $Filename
Invoke-WebRequest -Uri $Url -OutFile $ZipPath -UseBasicParsing

# Extract
Expand-Archive -Path $ZipPath -DestinationPath $Tmp -Force

# Install
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

Copy-Item -Path (Join-Path $Tmp $Binary) -Destination (Join-Path $InstallDir $Binary) -Force

# Add to PATH if not already there
$CurrentPath = [System.Environment]::GetEnvironmentVariable("PATH", "User")
if ($CurrentPath -notlike "*$InstallDir*") {
    [System.Environment]::SetEnvironmentVariable("PATH", "$CurrentPath;$InstallDir", "User")
    Write-Host "Added $InstallDir to PATH. Restart your terminal for this to take effect."
}

# Cleanup
Remove-Item -Recurse -Force $Tmp

Write-Host ""
Write-Host "telara installed to $InstallDir\$Binary"
Write-Host ""
Write-Host "Get started:"
Write-Host "  1. Generate a token at https://app.telara.ai/settings?tab=developer"
Write-Host "  2. telara login --token <your-token>"
Write-Host "  3. telara setup claude-code"
