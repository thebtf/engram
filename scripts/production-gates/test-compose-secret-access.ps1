[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'compose-secret-access.ps1')

$root = Join-Path ([System.IO.Path]::GetTempPath()) ('engram-compose-secret-access-' + [guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory -Path $root | Out-Null
    Set-ComposeSecretPathAccess -Path $root -Directory
    $file = Join-Path $root 'probe.secret'
    [System.IO.File]::WriteAllText($file, 'probe', [System.Text.UTF8Encoding]::new($false))
    Set-ComposeSecretPathAccess -Path $file
    Assert-ComposeSecretPathAccess -Path $root -Directory
    Assert-ComposeSecretPathAccess -Path $file
    if ($IsWindows) {
        Write-Output 'compose-secret-access self-test=PASS platform=windows acl=owner+LocalSystem'
    } else {
        $directoryMode = [Convert]::ToString([int][System.IO.File]::GetUnixFileMode($root), 8)
        $fileMode = [Convert]::ToString([int][System.IO.File]::GetUnixFileMode($file), 8)
        Write-Output "compose-secret-access self-test=PASS platform=unix directory_mode=$directoryMode file_mode=$fileMode"
    }
}
finally {
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
