[CmdletBinding()]
param(
    [ValidatePattern('^v[0-9][A-Za-z0-9._-]*$')]
    [string]$Version = '',
    [string]$Workspace = '',
    [string]$ConfigPath = '',
    [string]$ReleaseBase = 'https://github.com/zlz3907/wecom-mcp/releases/download',
    [switch]$NoGui,
    [switch]$SkipRegistration
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$work = $null
$ui = ConvertFrom-Json @'
{
  "title": "\u4f01\u4e1a\u5fae\u4fe1 MCP \u5b89\u88c5\u5411\u5bfc",
  "chooseWorkspace": "\u8bf7\u9009\u62e9 TRAE Work CN \u5f53\u524d\u9879\u76ee\u6587\u4ef6\u5939\u3002\u5b89\u88c5\u5411\u5bfc\u53ea\u4f1a\u5728\u8be5\u9879\u76ee\u7684 .trae \u8303\u56f4\u5185\u5b89\u88c5 MCP\u3002",
  "cancelled": "\u5df2\u53d6\u6d88\u5b89\u88c5\uff0c\u672a\u4fee\u6539\u9879\u76ee\u3002",
  "configQuestion": "MCP \u7a0b\u5e8f\u5df2\u5b89\u88c5\u3002\n\n\u60a8\u662f\u5426\u5df2\u7ecf\u62ff\u5230\u6280\u672f\u4eba\u5458\u63d0\u4f9b\u7684 zoop_wecom_zhycit.local.json \u914d\u7f6e\u6587\u4ef6\uff1f",
  "chooseConfig": "\u9009\u62e9\u6280\u672f\u4eba\u5458\u63d0\u4f9b\u7684 zoop_wecom_zhycit.local.json",
  "needTech": "MCP \u7a0b\u5e8f\u5df2\u5b89\u88c5\uff0c\u4f46\u8fd8\u4e0d\u80fd\u8fde\u63a5\u4f01\u4e1a\u670d\u52a1\u3002\n\n\u8bf7\u8054\u7cfb\u672c\u7ec4\u7ec7\u7684\u6280\u672f\u4eba\u5458\uff0c\u83b7\u53d6\uff1a\n1. zoop_wecom_zhycit.local.json \u5b9e\u4f8b\u914d\u7f6e\n2. \u4e0e\u5b83\u5339\u914d\u7684 Schema \u955c\u50cf\n3. \u7531\u7ec4\u7ec7\u6279\u51c6\u7684 GNAS \u8fde\u63a5\u914d\u7f6e\n\n\u4e0d\u8981\u81ea\u5df1\u731c\u53c2\u6570\uff0c\u4e5f\u4e0d\u8981\u5728\u5bf9\u8bdd\u4e2d\u7c98\u8d34\u5bc6\u94a5\u3002\u83b7\u53d6\u914d\u7f6e\u540e\uff0c\u518d\u6b21\u53cc\u51fb\u672c\u5b89\u88c5\u5411\u5bfc\u5373\u53ef\u7ee7\u7eed\u3002",
  "success": "\u5b89\u88c5\u548c\u914d\u7f6e\u5df2\u5b8c\u6210\u3002\n\n\u8bf7\u5b8c\u5168\u9000\u51fa\u5e76\u91cd\u65b0\u6253\u5f00 TRAE Work CN\uff0c\u7136\u540e\u5728 MCP \u9762\u677f\u786e\u8ba4 zoop_wecom_zhycit \u5df2\u542f\u52a8\u3002",
  "failed": "\u5b89\u88c5\u5411\u5bfc\u672a\u5b8c\u6210\u3002\n\n\u8bf7\u5c06\u9519\u8bef\u7ed3\u679c\u53d1\u7ed9\u6280\u672f\u4eba\u5458\uff0c\u4e0d\u8981\u53cd\u590d\u91cd\u8bd5\u3002\n\n"
}
'@

function Show-Info([string]$Message, [string]$Icon = 'Information') {
    if ($NoGui) { return }
    Add-Type -AssemblyName System.Windows.Forms
    [void][System.Windows.Forms.MessageBox]::Show($Message, $ui.title, 'OK', $Icon)
}

function Select-Workspace {
    Add-Type -AssemblyName System.Windows.Forms
    [System.Windows.Forms.Application]::EnableVisualStyles()
    $dialog = New-Object System.Windows.Forms.FolderBrowserDialog
    $dialog.Description = $ui.chooseWorkspace
    $dialog.ShowNewFolderButton = $false
    if ($dialog.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) { return '' }
    return $dialog.SelectedPath
}

function Select-InstanceConfig {
    Add-Type -AssemblyName System.Windows.Forms
    $dialog = New-Object System.Windows.Forms.OpenFileDialog
    $dialog.Title = $ui.chooseConfig
    $dialog.Filter = 'JSON (*.json)|*.json'
    $dialog.CheckFileExists = $true
    $dialog.Multiselect = $false
    if ($dialog.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) { return '' }
    return $dialog.FileName
}

function Get-Sha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Read-Checksums([string]$Path) {
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ($line -notmatch '^([0-9a-fA-F]{64})\s+\*?(.+)$') { throw 'invalid SHA256SUMS line' }
        if ($values.ContainsKey($Matches[2])) { throw 'duplicate SHA256SUMS entry' }
        $values[$Matches[2]] = $Matches[1].ToLowerInvariant()
    }
    return $values
}

function Resolve-FixedVersion {
    if ($Version) { return $Version }
    $versionFile = Join-Path $PSScriptRoot 'wizard-version.txt'
    if (Test-Path -LiteralPath $versionFile -PathType Leaf) {
        $candidate = (Get-Content -LiteralPath $versionFile -Raw).Trim()
        if ($candidate -match '^v[0-9][A-Za-z0-9._-]*$') { return $candidate }
    }
    throw 'wizard-version.txt is missing or invalid; download the complete verified Windows wizard package from one fixed GitHub Release'
}

function Test-InstanceConfig([string]$Path) {
    if (-not [IO.Path]::IsPathRooted($Path) -or -not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw 'the organization instance configuration must be an existing absolute JSON file'
    }
    $config = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
    foreach ($name in @('instance_name', 'tenant_route', 'registry_key', 'registry_document_id', 'schema_mirror_path', 'state_path', 'api_whitelist')) {
        if ($null -eq $config.$name) { throw "the organization instance configuration is missing $name" }
    }
    if ($config.instance_name -ne 'zoop_wecom_zhycit') { throw 'the organization instance configuration has the wrong instance_name' }
    if (-not [IO.Path]::IsPathRooted([string]$config.schema_mirror_path) -or -not (Test-Path -LiteralPath ([string]$config.schema_mirror_path) -PathType Leaf)) {
        throw 'the Schema mirror referenced by the organization instance configuration is missing'
    }
}

function Test-PersistentGnasEnvironment {
    foreach ($name in @('GNAS_BASE_URL', 'GNAS_APP_ID', 'GNAS_APP_SECRET')) {
        if ($env:WECOM_MCP_INSTALLER_TEST -eq '1' -and -not [string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name, 'Process'))) { continue }
        $userValue = [Environment]::GetEnvironmentVariable($name, 'User')
        $machineValue = [Environment]::GetEnvironmentVariable($name, 'Machine')
        if ([string]::IsNullOrWhiteSpace($userValue) -and [string]::IsNullOrWhiteSpace($machineValue)) { return $false }
    }
    return $true
}

function Convert-LinesToMap([string[]]$Lines) {
    $values = @{}
    foreach ($line in $Lines) {
        $parts = $line -split '=', 2
        if ($parts.Count -eq 2) { $values[$parts[0]] = $parts[1] }
    }
    return $values
}

try {
    if (-not $Workspace) {
        if ($NoGui) { throw '-Workspace is required with -NoGui' }
        $Workspace = Select-Workspace
        if (-not $Workspace) {
            Show-Info $ui.cancelled
            exit 2
        }
    }
    if (-not [IO.Path]::IsPathRooted($Workspace) -or -not (Test-Path -LiteralPath $Workspace -PathType Container)) {
        throw 'the selected TRAE workspace is not an existing absolute directory'
    }
    $Workspace = [IO.Path]::GetFullPath($Workspace)
    $Version = Resolve-FixedVersion
    $releaseBaseUri = [Uri]$ReleaseBase
    $testHttp = $env:WECOM_MCP_INSTALLER_TEST -eq '1' -and $releaseBaseUri.Scheme -eq 'http' -and $releaseBaseUri.Host -in @('127.0.0.1', 'localhost')
    if ($releaseBaseUri.Scheme -ne 'https' -and -not $testHttp) { throw '-ReleaseBase must use HTTPS' }
    $fixedBase = $ReleaseBase.TrimEnd('/') + '/' + $Version
    $work = Join-Path ([IO.Path]::GetTempPath()) ("wecom-mcp-wizard-{0}" -f [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $work | Out-Null
    $checksumsPath = Join-Path $work 'SHA256SUMS'
    $manifestPath = Join-Path $work 'RELEASE-MANIFEST.txt'
    $installerPath = Join-Path $work 'install.ps1'
    Invoke-WebRequest -UseBasicParsing -Uri "$fixedBase/SHA256SUMS" -OutFile $checksumsPath
    $checksums = Read-Checksums $checksumsPath
    foreach ($required in @('RELEASE-MANIFEST.txt', 'install.ps1')) {
        if (-not $checksums.ContainsKey($required)) { throw "SHA256SUMS is missing $required" }
    }
    Invoke-WebRequest -UseBasicParsing -Uri "$fixedBase/RELEASE-MANIFEST.txt" -OutFile $manifestPath
    if ((Get-Sha256 $manifestPath) -ne $checksums['RELEASE-MANIFEST.txt']) { throw 'RELEASE-MANIFEST.txt checksum mismatch' }
    $manifest = @{}
    foreach ($line in Get-Content -LiteralPath $manifestPath) {
        $parts = $line -split '=', 2
        if ($parts.Count -ne 2 -or $manifest.ContainsKey($parts[0])) { throw 'invalid release manifest' }
        $manifest[$parts[0]] = $parts[1]
    }
    $wizardAsset = "wecom-mcp-v2_${Version}_windows_wizard.zip"
    if ($manifest['version'] -ne $Version -or $manifest['installer_windows'] -ne 'install.ps1' -or $manifest['wizard_windows_amd64'] -ne $wizardAsset) { throw 'release manifest does not match this wizard version' }
    Invoke-WebRequest -UseBasicParsing -Uri "$fixedBase/install.ps1" -OutFile $installerPath
    if ((Get-Sha256 $installerPath) -ne $checksums['install.ps1']) { throw 'install.ps1 checksum mismatch' }

    $installLines = @(& powershell.exe -NoProfile -ExecutionPolicy Bypass -File $installerPath -Version $Version -Client trae-work-cn -Workspace $Workspace -ReleaseBase $ReleaseBase)
    $installExit = $LASTEXITCODE
    $installResult = Convert-LinesToMap $installLines
    $installLines | Write-Output
    if ($installExit -ne 0 -or $installResult['result'] -ne 'passed' -or $installResult['installed'] -ne 'yes') { throw 'verified Windows installer did not complete' }

    if ($SkipRegistration) { exit 0 }
    if (-not $ConfigPath -and -not $NoGui) {
        Add-Type -AssemblyName System.Windows.Forms
        $answer = [System.Windows.Forms.MessageBox]::Show($ui.configQuestion, $ui.title, 'YesNo', 'Question')
        if ($answer -eq [System.Windows.Forms.DialogResult]::Yes) { $ConfigPath = Select-InstanceConfig }
    }
    if (-not $ConfigPath -or -not (Test-PersistentGnasEnvironment)) {
        Show-Info $ui.needTech 'Warning'
        Write-Output 'wizard_result=installed_needs_organization_configuration'
        Write-Output 'configured=no'
        Write-Output 'next_action=contact the organization technical administrator for the instance config, matching Schema mirror, and approved persistent GNAS environment; do not paste secrets into chat'
        exit 0
    }
    Test-InstanceConfig $ConfigPath
    $binaryPath = $installResult['binary_path']
    $configurePath = Join-Path (Split-Path -Parent $binaryPath) 'wecom-mcp-v2-configure.exe'
    if (-not (Test-Path -LiteralPath $configurePath -PathType Leaf)) { throw 'verified configuration helper is missing' }
    $traeConfig = Join-Path $Workspace '.trae\mcp.json'
    $configureJson = (& $configurePath -client trae-work-cn -binary $binaryPath -config ([IO.Path]::GetFullPath($ConfigPath)) -trae-config $traeConfig) -join "`n"
    if ($LASTEXITCODE -ne 0) { throw 'TRAE project registration helper stopped without changing an unknown configuration' }
    $configured = $configureJson | ConvertFrom-Json
    if (($configured | Where-Object { $_.configured -eq $true }).Count -ne 1) { throw 'TRAE project registration did not complete' }
    Write-Output 'wizard_result=passed'
    Write-Output 'configured=yes'
    Write-Output 'loaded=no'
    Write-Output 'verified=no'
    Write-Output 'next_action=restart TRAE Work CN and verify zoop_wecom_zhycit in the MCP panel'
    Show-Info $ui.success
}
catch {
    Write-Output 'wizard_result=agent_blocked'
    Write-Output ("evidence=" + $_.Exception.Message)
    Write-Output 'next_action=send this result to the organization technical administrator; do not repeat the same failed installation'
    Show-Info ($ui.failed + $_.Exception.Message) 'Error'
    exit 3
}
finally {
    if ($work -and (Test-Path -LiteralPath $work)) { Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue }
}
