[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^v[0-9][A-Za-z0-9._-]*$')]
    [string]$Version,
    [ValidateSet('standalone', 'codex', 'trae-solo-cn', 'trae-work-cn', 'workbuddy')]
    [string]$Client = 'standalone',
    [string]$Workspace = '',
    [string]$Prefix = '',
    [string]$ReleaseBase = 'https://github.com/zlz3907/wecom-mcp/releases/download'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$assetName = "wecom-mcp-v2_${Version}_windows_amd64.zip"
$releaseName = "${Version}-windows-amd64"
$binaryPath = 'missing'
$binarySha256 = 'missing'
$installed = 'no'
$configPaths = 'none'
$nextAction = 'register binary_path in the target MCP client only after an existing zoop_wecom_zhycit.local.json path is available'
$work = $null
$staging = $null
$permissionProbeSource = $null
$permissionProbeTarget = $null

function Get-Sha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Read-KeyValueFile([string]$Path) {
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line -split '=', 2
        if ($parts.Count -ne 2 -or $values.ContainsKey($parts[0])) {
            throw "invalid key/value manifest: $Path"
        }
        $values[$parts[0]] = $parts[1]
    }
    return $values
}

function Read-Checksums([string]$Path) {
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ($line -notmatch '^([0-9a-fA-F]{64})\s+\*?(.+)$') { throw 'invalid SHA256SUMS line' }
        $name = $Matches[2]
        if ($values.ContainsKey($name)) { throw "duplicate SHA256SUMS entry: $name" }
        $values[$name] = $Matches[1].ToLowerInvariant()
    }
    return $values
}

function Assert-RegularFile([string]$Path, [string]$Label) {
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.PSIsContainer -or (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)) {
        throw "$Label must be a regular file"
    }
}

function Assert-TargetWritable([string]$ReleaseRoot) {
    # Exercise the exact create/rename/delete operations used by the installer
    # before downloading or expanding a release. Restricted Agent hosts can
    # otherwise leave a .staging-* directory that they are unable to remove.
    New-Item -ItemType Directory -Path $ReleaseRoot -Force | Out-Null
    $script:permissionProbeSource = Join-Path $ReleaseRoot (".permission-probe-{0}-source" -f [guid]::NewGuid().ToString('N'))
    $script:permissionProbeTarget = Join-Path $ReleaseRoot (".permission-probe-{0}-target" -f [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $script:permissionProbeSource | Out-Null
    [IO.File]::WriteAllText((Join-Path $script:permissionProbeSource 'probe.txt'), 'wecom-mcp-v2 target permission probe')
    Move-Item -LiteralPath $script:permissionProbeSource -Destination $script:permissionProbeTarget
    $script:permissionProbeSource = $null
    Remove-Item -LiteralPath $script:permissionProbeTarget -Recurse -Force
    $script:permissionProbeTarget = $null
}

function Remove-StaleInstallerResidue([string]$ReleaseRoot) {
    $cutoff = [DateTime]::UtcNow.AddMinutes(-15)
    foreach ($item in Get-ChildItem -LiteralPath $ReleaseRoot -Force -Directory) {
        if ($item.Name -notmatch '^\.(staging|permission-probe)-[0-9a-f]{32}(-source|-target)?$') { continue }
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { continue }
        if ($item.LastWriteTimeUtc -gt $cutoff) { continue }
        Remove-Item -LiteralPath $item.FullName -Recurse -Force
    }
}

function Assert-Release([string]$Directory) {
    $manifestPath = Join-Path $Directory 'INSTALL-MANIFEST.txt'
    $serverPath = Join-Path $Directory 'bin\wecom-mcp-v2.exe'
    $configurePath = Join-Path $Directory 'bin\wecom-mcp-v2-configure.exe'
    $configPath = Join-Path $Directory 'config\zoop_wecom_zhycit.json.example'
    $licensePath = Join-Path $Directory 'LICENSE'
    foreach ($entry in @(
        @($manifestPath, 'INSTALL-MANIFEST.txt'),
        @($serverPath, 'wecom-mcp-v2.exe'),
        @($configurePath, 'wecom-mcp-v2-configure.exe'),
        @($configPath, 'configuration example'),
        @($licensePath, 'LICENSE')
    )) { Assert-RegularFile $entry[0] $entry[1] }

    $manifest = Read-KeyValueFile $manifestPath
    if ($manifest['format'] -ne 'wecom-mcp-github-release-v1') { throw 'unexpected INSTALL-MANIFEST format' }
    if ($manifest['version'] -ne $Version) { throw 'INSTALL-MANIFEST version mismatch' }
    if ($manifest['platform'] -ne 'windows/amd64') { throw 'INSTALL-MANIFEST platform mismatch' }
    if ($manifest['source_commit'] -notmatch '^[0-9a-f]{40}$') { throw 'invalid source_commit' }
    if ((Get-Sha256 $serverPath) -ne $manifest['binary_sha256']) { throw 'server binary checksum mismatch' }
    if ((Get-Sha256 $configurePath) -ne $manifest['configure_sha256']) { throw 'configure binary checksum mismatch' }
    if ((Get-Sha256 $configPath) -ne $manifest['config_example_sha256']) { throw 'configuration example checksum mismatch' }
    if ((Get-Sha256 $licensePath) -ne $manifest['license_sha256']) { throw 'LICENSE checksum mismatch' }
    return $serverPath
}

function Write-Result([string]$Result, [string]$Evidence, [string]$NextAction) {
    Write-Output "result=$Result"
    Write-Output 'operation=install'
    Write-Output "release_version=$Version"
    Write-Output 'platform=windows/amd64'
    Write-Output "client=$Client"
    Write-Output "installed=$installed"
    Write-Output 'configured=no'
    Write-Output 'loaded=no'
    Write-Output 'verified=no'
    Write-Output "binary_path=$binaryPath"
    Write-Output "binary_sha256=$binarySha256"
    Write-Output "config_paths=$configPaths"
    Write-Output 'rollback_target=none'
    Write-Output "evidence=$Evidence"
    Write-Output "next_action=$NextAction"
}

try {
    $userHome = [Environment]::GetFolderPath('UserProfile')
    $localAppData = [Environment]::GetFolderPath('LocalApplicationData')
    if ($env:WECOM_MCP_INSTALLER_TEST -eq '1' -and $env:WECOM_MCP_INSTALLER_TEST_HOME) {
        $userHome = $env:WECOM_MCP_INSTALLER_TEST_HOME
    }
    if ($env:WECOM_MCP_INSTALLER_TEST -eq '1' -and $env:WECOM_MCP_INSTALLER_TEST_LOCALAPPDATA) {
        $localAppData = $env:WECOM_MCP_INSTALLER_TEST_LOCALAPPDATA
    }
    if (-not $localAppData -or -not [IO.Path]::IsPathRooted($localAppData)) {
        throw 'Windows client-scoped installation requires the standard LocalApplicationData path'
    }
    if ($Prefix -and $Client -ne 'standalone') { throw '-Prefix may only be used with -Client standalone' }
    if ($Workspace -and $Client -notin @('trae-solo-cn', 'trae-work-cn')) { throw '-Workspace may only be used with a TRAE client' }
    if ($Prefix) {
        if (-not [IO.Path]::IsPathRooted($Prefix)) { throw '-Prefix must be an absolute path' }
    } else {
        switch ($Client) {
            'codex' {
                $Prefix = Join-Path $localAppData 'wecom-mcp-v2\clients\codex'
                $configPaths = Join-Path $userHome '.codex\config.toml'
                $nextAction = 'locate the organization instance configuration and approved persistent GNAS environment, then use the verified configuration helper to merge the shared Codex config.toml'
            }
            'trae-solo-cn' {
                if ($Workspace) {
                    if (-not [IO.Path]::IsPathRooted($Workspace)) {
                        throw '-Workspace must be an absolute path when supplied'
                    }
                    if (-not (Test-Path -LiteralPath $Workspace -PathType Container)) {
                        throw '-Workspace must be an existing directory'
                    }
                    $Workspace = [IO.Path]::GetFullPath($Workspace)
                }
                if (-not $env:APPDATA -or -not [IO.Path]::IsPathRooted($env:APPDATA)) {
                    throw 'TRAE SOLO CN requires the standard Windows APPDATA path'
                }
                $Prefix = Join-Path $localAppData 'wecom-mcp-v2\clients\trae-solo-cn'
                $configPaths = Join-Path $env:APPDATA 'TRAE SOLO CN\User\mcp.json'
                $nextAction = 'locate the organization instance configuration and approved persistent GNAS environment, then use the verified configuration helper to merge the TRAE SOLO CN user mcp.json'
            }
            'trae-work-cn' {
                if (-not $Workspace -or -not [IO.Path]::IsPathRooted($Workspace)) {
                    throw '-Client trae-work-cn requires an absolute -Workspace path'
                }
                if (-not (Test-Path -LiteralPath $Workspace -PathType Container)) {
                    throw '-Workspace must be an existing directory'
                }
                $Workspace = [IO.Path]::GetFullPath($Workspace)
                $Prefix = Join-Path $localAppData 'wecom-mcp-v2\clients\trae-work-cn'
                $configPaths = Join-Path $Workspace '.trae\mcp.json'
                $nextAction = 'use TRAE Settings > MCP to merge zoop_wecom_zhycit into the project .trae/mcp.json after an existing local instance config path is available'
            }
            'workbuddy' {
                $Prefix = Join-Path $userHome '.codebuddy\mcp-servers\wecom-mcp-v2'
                $configPaths = Join-Path $userHome '.codebuddy\.mcp.json'
                $nextAction = 'use WorkBuddy MCP management to add a user-scope stdio server after an existing local instance config path is available'
            }
            default {
                $Prefix = Join-Path $userHome '.mcp\wecom-mcp-v2'
            }
        }
    }
    $releaseBaseUri = [Uri]$ReleaseBase
    $testHttp = $env:WECOM_MCP_INSTALLER_TEST -eq '1' -and $releaseBaseUri.Scheme -eq 'http' -and $releaseBaseUri.Host -in @('127.0.0.1', 'localhost')
    if ($releaseBaseUri.Scheme -ne 'https' -and -not $testHttp) { throw '-ReleaseBase must use HTTPS' }
    if ($releaseBaseUri.Query -or $releaseBaseUri.Fragment) { throw '-ReleaseBase must not contain a query or fragment' }
    $Prefix = [IO.Path]::GetFullPath($Prefix)
    $releaseRoot = Join-Path $Prefix 'releases'
    $target = Join-Path $releaseRoot $releaseName

    if (Test-Path -LiteralPath $target) {
        $binaryPath = Assert-Release $target
        $binarySha256 = Get-Sha256 $binaryPath
        $installed = 'yes'
        Write-Result 'passed' 'existing client-scoped version directory passed the complete INSTALL-MANIFEST checksum verification; no files were overwritten' $nextAction
        return
    }

    try {
        Assert-TargetWritable $releaseRoot
        Remove-StaleInstallerResidue $releaseRoot
    } catch {
        throw ("target directory permission preflight failed before release download; stop and report agent_blocked without fallback, manual commands, or repeated writes: " + $_.Exception.Message)
    }

    $work = Join-Path ([IO.Path]::GetTempPath()) ("wecom-mcp-v2-{0}" -f [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $work | Out-Null
    $checksumsPath = Join-Path $work 'SHA256SUMS'
    $releaseManifestPath = Join-Path $work 'RELEASE-MANIFEST.txt'
    $archivePath = Join-Path $work $assetName
    $fixedBase = $ReleaseBase.TrimEnd('/') + '/' + $Version

    Invoke-WebRequest -UseBasicParsing -Uri "$fixedBase/SHA256SUMS" -OutFile $checksumsPath
    $checksums = Read-Checksums $checksumsPath
    foreach ($required in @('install.ps1', 'RELEASE-MANIFEST.txt', $assetName)) {
        if (-not $checksums.ContainsKey($required)) { throw "SHA256SUMS is missing $required" }
    }
    Assert-RegularFile $PSCommandPath 'install.ps1'
    if ((Get-Sha256 $PSCommandPath) -ne $checksums['install.ps1']) { throw 'install.ps1 checksum mismatch' }
    Invoke-WebRequest -UseBasicParsing -Uri "$fixedBase/RELEASE-MANIFEST.txt" -OutFile $releaseManifestPath
    if ((Get-Sha256 $releaseManifestPath) -ne $checksums['RELEASE-MANIFEST.txt']) { throw 'RELEASE-MANIFEST.txt checksum mismatch' }
    $releaseManifest = Read-KeyValueFile $releaseManifestPath
    if ($releaseManifest['format'] -ne 'wecom-mcp-github-release-index-v1') { throw 'unexpected release manifest format' }
    if ($releaseManifest['version'] -ne $Version) { throw 'release manifest version mismatch' }
    if ($releaseManifest['installer_windows'] -ne 'install.ps1') { throw 'release manifest Windows installer mismatch' }
    if ($releaseManifest['asset_windows_amd64'] -ne $assetName) { throw 'release manifest Windows asset mismatch' }

    Invoke-WebRequest -UseBasicParsing -Uri "$fixedBase/$assetName" -OutFile $archivePath
    if ((Get-Sha256 $archivePath) -ne $checksums[$assetName]) { throw 'Windows archive checksum mismatch' }

    New-Item -ItemType Directory -Path $releaseRoot -Force | Out-Null
    $staging = Join-Path $releaseRoot (".staging-{0}" -f [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $staging | Out-Null
    Expand-Archive -LiteralPath $archivePath -DestinationPath $staging
    $null = Assert-Release $staging
    Move-Item -LiteralPath $staging -Destination $target
    $staging = $null
    $binaryPath = Join-Path $target 'bin\wecom-mcp-v2.exe'
    $binarySha256 = Get-Sha256 $binaryPath
    $installed = 'yes'
    Write-Result 'passed' 'fixed-tag release manifest, archive, and complete INSTALL-MANIFEST checksums verified; client-scoped version directory installed without current links' $nextAction
    return
}
catch {
    Write-Result 'agent_blocked' ("installation stopped without switching to another prefix: " + $_.Exception.Message) 'fix the reported download, checksum, or target-directory condition, then rerun the same fixed version once'
    exit 3
}
finally {
    if ($permissionProbeSource -and (Test-Path -LiteralPath $permissionProbeSource)) { Remove-Item -LiteralPath $permissionProbeSource -Recurse -Force -ErrorAction SilentlyContinue }
    if ($permissionProbeTarget -and (Test-Path -LiteralPath $permissionProbeTarget)) { Remove-Item -LiteralPath $permissionProbeTarget -Recurse -Force -ErrorAction SilentlyContinue }
    if ($staging -and (Test-Path -LiteralPath $staging)) { Remove-Item -LiteralPath $staging -Recurse -Force }
    if ($work -and (Test-Path -LiteralPath $work)) { Remove-Item -LiteralPath $work -Recurse -Force }
}
