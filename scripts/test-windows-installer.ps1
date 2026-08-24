$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$installer = Join-Path $repo 'install.ps1'
$root = Join-Path ([IO.Path]::GetTempPath()) ("wecom-mcp-windows-test-{0}" -f [guid]::NewGuid().ToString('N'))
$version = 'v0.0.0-test'
$releaseBase = Join-Path $root 'http-root'
$release = Join-Path $releaseBase $version
$stage = Join-Path $root 'stage'
$prefix = Join-Path $root 'prefix'
$server = $null
$previousTestMode = $env:WECOM_MCP_INSTALLER_TEST

function Sha([string]$Path) { return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }
function Assert-Line([string[]]$Lines, [string]$Wanted) {
    if ($Lines -notcontains $Wanted) { throw "missing installer output: $Wanted" }
}

try {
    New-Item -ItemType Directory -Path (Join-Path $stage 'bin'), (Join-Path $stage 'config'), $release -Force | Out-Null
    [IO.File]::WriteAllBytes((Join-Path $stage 'bin\wecom-mcp-v2.exe'), [byte[]](1, 2, 3, 4))
    [IO.File]::WriteAllBytes((Join-Path $stage 'bin\wecom-mcp-v2-configure.exe'), [byte[]](5, 6, 7, 8))
    Set-Content -LiteralPath (Join-Path $stage 'config\zoop_wecom_zhycit.json.example') -Value '{}' -NoNewline
    Set-Content -LiteralPath (Join-Path $stage 'LICENSE') -Value 'test license' -NoNewline
    @(
        'format=wecom-mcp-github-release-v1'
        "version=$version"
        'platform=windows/amd64'
        ('source_commit=' + ('a' * 40))
        ('source_tree=' + ('b' * 40))
        ('binary_sha256=' + (Sha (Join-Path $stage 'bin\wecom-mcp-v2.exe')))
        ('configure_sha256=' + (Sha (Join-Path $stage 'bin\wecom-mcp-v2-configure.exe')))
        ('config_example_sha256=' + (Sha (Join-Path $stage 'config\zoop_wecom_zhycit.json.example')))
        ('license_sha256=' + (Sha (Join-Path $stage 'LICENSE')))
    ) | Set-Content -LiteralPath (Join-Path $stage 'INSTALL-MANIFEST.txt')

    $asset = "wecom-mcp-v2_${version}_windows_amd64.zip"
    Compress-Archive -Path (Join-Path $stage '*') -DestinationPath (Join-Path $release $asset)
    @(
        'format=wecom-mcp-github-release-index-v1'
        "version=$version"
        ('source_commit=' + ('a' * 40))
        ('source_tree=' + ('b' * 40))
        'supported_core=windows/amd64'
        'installer=install.sh'
        'installer_windows=install.ps1'
        'checksums=SHA256SUMS'
        "asset_windows_amd64=$asset"
    ) | Set-Content -LiteralPath (Join-Path $release 'RELEASE-MANIFEST.txt')
    Copy-Item -LiteralPath $installer -Destination (Join-Path $release 'install.ps1')
    @(
        "$(Sha (Join-Path $release 'install.ps1'))  install.ps1"
        "$(Sha (Join-Path $release 'RELEASE-MANIFEST.txt'))  RELEASE-MANIFEST.txt"
        "$(Sha (Join-Path $release $asset))  $asset"
    ) | Set-Content -LiteralPath (Join-Path $release 'SHA256SUMS')

    $listener = New-Object -TypeName Net.Sockets.TcpListener -ArgumentList ([Net.IPAddress]::Loopback), 0
    $listener.Start()
    $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    $server = Start-Process -FilePath python -ArgumentList @('-m', 'http.server', $port, '--bind', '127.0.0.1', '--directory', $releaseBase) -PassThru -WindowStyle Hidden
    $baseUrl = "http://127.0.0.1:$port"
    $ready = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/$version/SHA256SUMS" | Out-Null
            $ready = $true
            break
        } catch { Start-Sleep -Milliseconds 200 }
    }
    if (-not $ready) { throw 'local release server did not start' }

    $env:WECOM_MCP_INSTALLER_TEST = '1'
    $first = @(& $installer -Version $version -Prefix $prefix -ReleaseBase $baseUrl)
    Assert-Line $first 'result=passed'
    Assert-Line $first 'installed=yes'
    Assert-Line $first 'configured=no'
    Assert-Line $first 'loaded=no'
    Assert-Line $first 'verified=no'
    if (Test-Path -LiteralPath (Join-Path $prefix 'current')) { throw 'Windows installer created a current path' }
    $binary = Join-Path $prefix "releases\${version}-windows-amd64\bin\wecom-mcp-v2.exe"
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw 'versioned binary is missing' }

    $repeat = @(& $installer -Version $version -Prefix $prefix -ReleaseBase $baseUrl)
    Assert-Line $repeat 'result=passed'
    if (($repeat -join "`n") -notmatch 'existing version directory passed') { throw 'repeat install did not verify and reuse the release' }
    Write-Output 'windows_installer_test=passed'
}
finally {
    $env:WECOM_MCP_INSTALLER_TEST = $previousTestMode
    if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
