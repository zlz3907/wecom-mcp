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
$previousTestHome = $env:WECOM_MCP_INSTALLER_TEST_HOME
$previousAppData = $env:APPDATA

function Sha([string]$Path) { return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }
function Assert-Line([string[]]$Lines, [string]$Wanted) {
    if ($Lines -notcontains $Wanted) { throw "missing installer output: $Wanted" }
}

try {
    $fixtureConfigure = Join-Path $root 'wecom-mcp-v2-configure.exe'
    Push-Location $repo
    try { & go build -trimpath -o $fixtureConfigure .\cmd\wecom-mcp-v2-configure } finally { Pop-Location }
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $fixtureConfigure -PathType Leaf)) { throw 'failed to build Windows configuration helper fixture' }
    New-Item -ItemType Directory -Path (Join-Path $stage 'bin'), (Join-Path $stage 'config'), $release -Force | Out-Null
    [IO.File]::WriteAllBytes((Join-Path $stage 'bin\wecom-mcp-v2.exe'), [byte[]](1, 2, 3, 4))
    Copy-Item -LiteralPath $fixtureConfigure -Destination (Join-Path $stage 'bin\wecom-mcp-v2-configure.exe')
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
    $env:WECOM_MCP_INSTALLER_TEST_HOME = Join-Path $root 'user-home'
    $env:APPDATA = Join-Path $root 'appdata'
    $staleStage = Join-Path $prefix 'releases\.staging-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    New-Item -ItemType Directory -Path $staleStage -Force | Out-Null
    (Get-Item -LiteralPath $staleStage).LastWriteTimeUtc = [DateTime]::UtcNow.AddHours(-1)
    $first = @(& $installer -Version $version -Prefix $prefix -ReleaseBase $baseUrl)
    Assert-Line $first 'result=passed'
    Assert-Line $first 'installed=yes'
    Assert-Line $first 'configured=no'
    Assert-Line $first 'loaded=no'
    Assert-Line $first 'verified=no'
    if (Test-Path -LiteralPath $staleStage) { throw 'installer did not clean its own stale staging residue after permission preflight' }
    if (Test-Path -LiteralPath (Join-Path $prefix 'current')) { throw 'Windows installer created a current path' }
    $binary = Join-Path $prefix "releases\${version}-windows-amd64\bin\wecom-mcp-v2.exe"
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw 'versioned binary is missing' }

    $repeat = @(& $installer -Version $version -Prefix $prefix -ReleaseBase $baseUrl)
    Assert-Line $repeat 'result=passed'
    if (($repeat -join "`n") -notmatch 'existing client-scoped version directory passed') { throw 'repeat install did not verify and reuse the release' }

    $traeWorkspace = Join-Path $root 'trae-workspace'
    New-Item -ItemType Directory -Path $traeWorkspace | Out-Null
    $trae = @(& $installer -Version $version -Client trae-work-cn -Workspace $traeWorkspace -ReleaseBase $baseUrl)
    Assert-Line $trae 'result=passed'
    Assert-Line $trae 'client=trae-work-cn'
    Assert-Line $trae ("config_paths=" + (Join-Path $traeWorkspace '.trae\mcp.json'))
    $traeBinary = Join-Path $traeWorkspace ".trae\mcp-servers\wecom-mcp-v2\releases\${version}-windows-amd64\bin\wecom-mcp-v2.exe"
    if (-not (Test-Path -LiteralPath $traeBinary -PathType Leaf)) { throw 'TRAE project-scoped binary is missing' }
    if (Test-Path -LiteralPath (Join-Path $traeWorkspace '.trae\mcp-servers\wecom-mcp-v2\current')) { throw 'TRAE install created a current path' }

    $schemaPath = Join-Path $root 'schema.json'
    Set-Content -LiteralPath $schemaPath -Value '{"version":1,"digest":"test","roles":{}}' -NoNewline
    $statePath = Join-Path $root 'state.json'
    $serviceConfig = Join-Path $root 'zoop_wecom_zhycit.local.json'
    @{
        version = 1
        instance_name = 'zoop_wecom_zhycit'
        tenant_route = 'test-route'
        registry_key = 'test-registry'
        registry_document_id = 'test-document'
        schema_mirror_path = $schemaPath
        state_path = $statePath
        api_whitelist = @{ read = @('get_records') }
    } | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $serviceConfig
    $soloWorkspace = Join-Path $root 'trae-solo-workspace'
    New-Item -ItemType Directory -Path $soloWorkspace | Out-Null
    $solo = @(& $installer -Version $version -Client trae-solo-cn -Workspace $soloWorkspace -ReleaseBase $baseUrl)
    Assert-Line $solo 'result=passed'
    Assert-Line $solo 'client=trae-solo-cn'
    $soloConfig = Join-Path $env:APPDATA 'TRAE SOLO CN\User\mcp.json'
    Assert-Line $solo ("config_paths=" + $soloConfig)
    $soloBinary = Join-Path $soloWorkspace ".trae\mcp-servers\wecom-mcp-v2\releases\${version}-windows-amd64\bin\wecom-mcp-v2.exe"
    if (-not (Test-Path -LiteralPath $soloBinary -PathType Leaf)) { throw 'TRAE SOLO CN workspace-scoped binary is missing' }
    New-Item -ItemType Directory -Path (Split-Path -Parent $soloConfig) -Force | Out-Null
    $soloConfigure = Join-Path (Split-Path -Parent $soloBinary) 'wecom-mcp-v2-configure.exe'
    $soloRegistration = @(& $soloConfigure -client trae-solo-cn -binary $soloBinary -config $serviceConfig)
    if ($LASTEXITCODE -ne 0) { throw ("TRAE SOLO CN registration failed: " + ($soloRegistration -join '; ')) }
    if (($soloRegistration -join "`n") -notmatch '"configured": true') { throw 'TRAE SOLO CN registration did not report configured=true' }
    if (-not (Test-Path -LiteralPath $soloConfig -PathType Leaf)) { throw 'TRAE SOLO CN user mcp.json was not created' }
    if ((Get-Content -LiteralPath $soloConfig -Raw) -notmatch 'zoop_wecom_zhycit') { throw 'TRAE SOLO CN user MCP registration is missing the fixed instance' }

    $blockedPrefix = Join-Path $root 'blocked-prefix'
    Set-Content -LiteralPath $blockedPrefix -Value 'not a directory' -NoNewline
    $blocked = @(& powershell.exe -NoProfile -ExecutionPolicy Bypass -File $installer -Version $version -Prefix $blockedPrefix -ReleaseBase $baseUrl)
    if ($LASTEXITCODE -eq 0) { throw 'target permission preflight unexpectedly passed' }
    Assert-Line $blocked 'result=agent_blocked'
    if (($blocked -join "`n") -notmatch 'stop and report agent_blocked without fallback') { throw 'permission preflight did not fail closed without fallback' }
    if (Get-ChildItem -LiteralPath $root -Recurse -Force -Directory -Filter '.staging-*') { throw 'permission preflight left a staging directory' }

    $workbuddy = @(& $installer -Version $version -Client workbuddy -ReleaseBase $baseUrl)
    Assert-Line $workbuddy 'result=passed'
    Assert-Line $workbuddy 'client=workbuddy'
    Assert-Line $workbuddy ("config_paths=" + (Join-Path $env:WECOM_MCP_INSTALLER_TEST_HOME '.codebuddy\.mcp.json'))
    $workbuddyBinary = Join-Path $env:WECOM_MCP_INSTALLER_TEST_HOME ".codebuddy\mcp-servers\wecom-mcp-v2\releases\${version}-windows-amd64\bin\wecom-mcp-v2.exe"
    if (-not (Test-Path -LiteralPath $workbuddyBinary -PathType Leaf)) { throw 'WorkBuddy user-scoped binary is missing' }
    if (Test-Path -LiteralPath (Join-Path $env:WECOM_MCP_INSTALLER_TEST_HOME '.codebuddy\mcp-servers\wecom-mcp-v2\current')) { throw 'WorkBuddy install created a current path' }
    Write-Output 'windows_installer_test=passed'
}
finally {
    $env:WECOM_MCP_INSTALLER_TEST = $previousTestMode
    $env:WECOM_MCP_INSTALLER_TEST_HOME = $previousTestHome
    $env:APPDATA = $previousAppData
    if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
exit 0
