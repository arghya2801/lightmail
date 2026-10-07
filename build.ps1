# Builds build\bin\Lightmail-amd64-installer.exe and build\bin\Lightmail-portable.zip.
$ErrorActionPreference = 'Stop'
$wails = Get-Command wails -ErrorAction SilentlyContinue
$wails = if ($wails) { $wails.Source } else { Join-Path (go env GOPATH) 'bin\wails.exe' }
if (-not (Get-Command makensis -ErrorAction SilentlyContinue)) { $env:PATH += ';C:\Program Files (x86)\NSIS' }

& $wails build -clean -nsis
if ($LASTEXITCODE) { exit $LASTEXITCODE }

# Portable zip: the same exe plus a "portable" marker, which keeps all data in a folder beside it.
$bin = Join-Path $PSScriptRoot 'build\bin'
$stage = Join-Path $bin 'Lightmail'
New-Item -ItemType Directory -Force $stage | Out-Null
Copy-Item (Join-Path $bin 'Lightmail.exe') $stage
Set-Content (Join-Path $stage 'portable') 'Keep this file to store all data in the "data" folder next to Lightmail.exe. Delete it to use %APPDATA% instead.'
Compress-Archive -Path $stage -DestinationPath (Join-Path $bin 'Lightmail-portable.zip') -Force
Remove-Item $stage -Recurse
Get-ChildItem $bin | Format-Table Name, @{ n = 'MB'; e = { [math]::Round($_.Length / 1MB, 1) } } -AutoSize
