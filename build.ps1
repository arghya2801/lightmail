# Builds build\bin\Lightmail-installer.msi and build\bin\Lightmail-portable.zip.
# Needs the WiX 3 tools (candle, light): on PATH, or unzip wix314-binaries.zip into %LOCALAPPDATA%\wix314.
$ErrorActionPreference = 'Stop'
$wails = Get-Command wails -ErrorAction SilentlyContinue
$wails = if ($wails) { $wails.Source } else { Join-Path (go env GOPATH) 'bin\wails.exe' }
if (-not (Get-Command candle -ErrorAction SilentlyContinue)) { $env:PATH += ";$env:LOCALAPPDATA\wix314" }

& $wails build -clean
if ($LASTEXITCODE) { exit $LASTEXITCODE }

$bin = Join-Path $PSScriptRoot 'build\bin'
$exe = Join-Path $bin 'Lightmail.exe'

# MSI
$installer = Join-Path $PSScriptRoot 'build\windows\installer'
$version = (Get-Content (Join-Path $PSScriptRoot 'wails.json') | ConvertFrom-Json).info.productVersion
$obj = Join-Path $env:TEMP 'lightmail.wixobj'
candle -nologo "-dVersion=$version" "-dExe=$exe" "-dIcon=$(Join-Path $PSScriptRoot 'build\windows\icon.ico')" -out $obj (Join-Path $installer 'lightmail.wxs')
if ($LASTEXITCODE) { exit $LASTEXITCODE }
light -nologo -spdb -out (Join-Path $bin 'Lightmail-installer.msi') $obj
if ($LASTEXITCODE) { exit $LASTEXITCODE }

# Portable zip: the same exe plus a "portable" marker, which keeps all data in a folder beside it.
$stage = Join-Path $bin 'Lightmail'
New-Item -ItemType Directory -Force $stage | Out-Null
Copy-Item $exe $stage
Set-Content (Join-Path $stage 'portable') 'Keep this file to store all data in the "data" folder next to Lightmail.exe. Delete it to use %APPDATA% instead.'
Compress-Archive -Path $stage -DestinationPath (Join-Path $bin 'Lightmail-portable.zip') -Force
Remove-Item $stage -Recurse
Get-ChildItem $bin | Format-Table Name, @{ n = 'MB'; e = { [math]::Round($_.Length / 1MB, 1) } } -AutoSize
