$ErrorActionPreference = "Stop"

$repo = "Blakeolson21/no-slop"

function Resolve-EnvAlias {
    param(
        [string]$Canonical,
        [string]$Legacy
    )

    $canonicalSet = Test-Path -LiteralPath "Env:$Canonical"
    $legacySet = Test-Path -LiteralPath "Env:$Legacy"
    $canonicalValue = if ($canonicalSet) { (Get-Item -LiteralPath "Env:$Canonical").Value } else { "" }
    $legacyValue = if ($legacySet) { (Get-Item -LiteralPath "Env:$Legacy").Value } else { "" }

    if ($canonicalSet -and $legacySet -and $canonicalValue -ne $legacyValue) {
        throw "$Canonical and $Legacy configure the same setting with different values"
    }
    if ($canonicalSet) {
        return $canonicalValue
    }
    if ($legacySet) {
        return $legacyValue
    }
    return ""
}

$installDir = Resolve-EnvAlias -Canonical "NS_INSTALL_DIR" -Legacy "NO_MISTAKES_INSTALL_DIR"
if ([string]::IsNullOrEmpty($installDir)) {
    $installDir = "$env:LOCALAPPDATA\no-mistakes"
}
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }

$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest"
$version = $release.tag_name
if (-not $version) {
    throw "Could not determine latest release"
}

$filename = "no-slop-$version-windows-$arch.zip"
$url = "https://github.com/$repo/releases/download/$version/$filename"

$tmpDir = New-TemporaryFile | ForEach-Object {
    Remove-Item $_
    New-Item -ItemType Directory -Path $_
}

Write-Host "Downloading no-slop $version for windows/$arch..."
Invoke-WebRequest -Uri $url -OutFile "$tmpDir\$filename"
Expand-Archive -Path "$tmpDir\$filename" -DestinationPath $tmpDir -Force

New-Item -ItemType Directory -Path $installDir -Force | Out-Null
Move-Item -Path "$tmpDir\no-slop.exe" -Destination "$installDir\no-slop.exe" -Force
Copy-Item -Path "$installDir\no-slop.exe" -Destination "$installDir\no-mistakes.exe" -Force
Remove-Item -Recurse -Force $tmpDir

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$installDir", "User")
    Write-Host "Added $installDir to user PATH. Restart your terminal."
}

$restart = Start-Process -FilePath "$installDir\no-slop.exe" -ArgumentList @(
    "daemon",
    "restart"
) -Wait -PassThru -NoNewWindow
if ($restart.ExitCode -ne 0) {
    throw "Failed to restart daemon (exit code $($restart.ExitCode))"
}

Write-Host "no-slop $version installed to $installDir\no-slop.exe"
