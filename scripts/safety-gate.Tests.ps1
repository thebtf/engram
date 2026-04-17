#Requires -Version 7
#Requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.0.0' }
<#
.SYNOPSIS
    Pester v5 tests for safety-gate.ps1 (Windows dev workstation variant).

.DESCRIPTION
    Tests run safety-gate.ps1 in isolated child processes.  Network calls are
    intercepted by injecting a tiny mock module that shadows Invoke-RestMethod
    before the real script runs.  No real server or vault is required.

.NOTES
    Run with: pwsh -NoProfile -Command "Invoke-Pester scripts/safety-gate.Tests.ps1 -Output Detailed"
#>

BeforeAll {
    $script:ScriptPath   = Join-Path $PSScriptRoot 'safety-gate.ps1'
    $script:BaselinePath = Join-Path $PSScriptRoot 'safety-gate-baseline.json'

    # Write a small PSM1 that overrides Invoke-RestMethod with a canned response.
    # The module file is re-created per-test call via the helper below.
    $script:MockDir = Join-Path $env:TEMP "safety-gate-mock-$([System.Guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $script:MockDir | Out-Null

    # Helper: run safety-gate.ps1 with a mocked vault response.
    # Returns hashtable: ExitCode, Stdout, Stderr.
    function Invoke-GateWithMock {
        param(
            [string]$Fingerprint      = 'aa78e55cf896508c',
            [int]   $CredentialCount  = 13,
            [int]   $MismatchCount    = 0,
            [string]$Token            = 'test-token',
            [string]$Phase            = 'auto',
            # Set to $true to simulate a network error from Invoke-RestMethod
            [bool]  $ThrowNetwork     = $false
        )

        # Build mock module text
        $throwLine = if ($ThrowNetwork) {
            "throw [System.Net.WebException]::new('simulated network error')"
        }
        else {
            @"
return [PSCustomObject]@{
    fingerprint      = '$Fingerprint'
    credential_count = $CredentialCount
    mismatch_count   = $MismatchCount
}
"@
        }

        $mockModulePath = Join-Path $script:MockDir "MockVault-$([System.Guid]::NewGuid().ToString('N')).psm1"
        $mockContent = @"
function Invoke-RestMethod {
    [CmdletBinding()]
    param(
        [Parameter(ValueFromRemainingArguments = `$true)]
        [object[]]`$Splat
    )
    $throwLine
}
Export-ModuleMember -Function Invoke-RestMethod
"@
        Set-Content -LiteralPath $mockModulePath -Value $mockContent -Encoding UTF8

        # Wrapper script: import mock module (shadows built-in), then call real script
        $wrapperPath = Join-Path $script:MockDir "wrapper-$([System.Guid]::NewGuid().ToString('N')).ps1"
        $escapedModule = $mockModulePath.Replace("'", "''")
        $escapedScript = $script:ScriptPath.Replace("'", "''")
        $escapedBaseline = $script:BaselinePath.Replace("'", "''")

        $wrapperContent = @"
#Requires -Version 7
Import-Module '$escapedModule' -Force
& '$escapedScript' -Baseline '$escapedBaseline' -Phase $Phase
"@
        Set-Content -LiteralPath $wrapperPath -Value $wrapperContent -Encoding UTF8

        # Launch child process
        $psi = [System.Diagnostics.ProcessStartInfo]::new('pwsh', "-NoProfile -NonInteractive -File `"$wrapperPath`"")
        $psi.UseShellExecute        = $false
        $psi.RedirectStandardOutput = $true
        $psi.RedirectStandardError  = $true
        $psi.CreateNoWindow         = $true

        # Forward current environment, then override ENGRAM_API_TOKEN
        foreach ($key in ([System.Environment]::GetEnvironmentVariables()).Keys) {
            if ($key -ne 'ENGRAM_API_TOKEN') {
                $val = [System.Environment]::GetEnvironmentVariable($key)
                if ($null -ne $val) {
                    $psi.EnvironmentVariables[$key] = $val
                }
            }
        }
        if ($Token -ne '') {
            $psi.EnvironmentVariables['ENGRAM_API_TOKEN'] = $Token
        }

        $proc   = [System.Diagnostics.Process]::Start($psi)
        $stdout = $proc.StandardOutput.ReadToEnd()
        $stderr = $proc.StandardError.ReadToEnd()
        $proc.WaitForExit()

        return @{
            ExitCode = $proc.ExitCode
            Stdout   = $stdout
            Stderr   = $stderr
        }
    }

    # Helper: run without any mock (for config-error paths that never hit the network)
    function Invoke-GateRaw {
        param(
            [string]$Token = 'test-token',
            [string[]]$ScriptArgs = @()
        )

        $argString = ($ScriptArgs | ForEach-Object { "`"$_`"" }) -join ' '
        $psi = [System.Diagnostics.ProcessStartInfo]::new('pwsh', "-NoProfile -NonInteractive -File `"$($script:ScriptPath)`" $argString")
        $psi.UseShellExecute        = $false
        $psi.RedirectStandardOutput = $true
        $psi.RedirectStandardError  = $true
        $psi.CreateNoWindow         = $true

        foreach ($key in ([System.Environment]::GetEnvironmentVariables()).Keys) {
            if ($key -ne 'ENGRAM_API_TOKEN') {
                $val = [System.Environment]::GetEnvironmentVariable($key)
                if ($null -ne $val) {
                    $psi.EnvironmentVariables[$key] = $val
                }
            }
        }
        if ($Token -ne '') {
            $psi.EnvironmentVariables['ENGRAM_API_TOKEN'] = $Token
        }

        $proc   = [System.Diagnostics.Process]::Start($psi)
        $stdout = $proc.StandardOutput.ReadToEnd()
        $stderr = $proc.StandardError.ReadToEnd()
        $proc.WaitForExit()

        return @{
            ExitCode = $proc.ExitCode
            Stdout   = $stdout
            Stderr   = $stderr
        }
    }
}

AfterAll {
    Remove-Item -LiteralPath $script:MockDir -Recurse -Force -ErrorAction SilentlyContinue
}

Describe 'safety-gate.ps1' {

    Context 'Help and usage' {
        It 'prints usage and exits 0 with -Help' {
            $result = Invoke-GateRaw -ScriptArgs @('-Help')
            $result.ExitCode | Should -Be 0
            $result.Stdout   | Should -Match 'ENGRAM_API_TOKEN'
        }
    }

    Context 'Config / prerequisite errors' {
        It 'exits 2 when ENGRAM_API_TOKEN is missing' {
            # Pass empty string to suppress env var forwarding
            $psi = [System.Diagnostics.ProcessStartInfo]::new('pwsh', "-NoProfile -NonInteractive -File `"$script:ScriptPath`"")
            $psi.UseShellExecute        = $false
            $psi.RedirectStandardOutput = $true
            $psi.RedirectStandardError  = $true
            $psi.CreateNoWindow         = $true

            # Forward env but explicitly exclude ENGRAM_API_TOKEN
            foreach ($key in ([System.Environment]::GetEnvironmentVariables()).Keys) {
                if ($key -ne 'ENGRAM_API_TOKEN') {
                    $val = [System.Environment]::GetEnvironmentVariable($key)
                    if ($null -ne $val) { $psi.EnvironmentVariables[$key] = $val }
                }
            }

            $proc   = [System.Diagnostics.Process]::Start($psi)
            $stdout = $proc.StandardOutput.ReadToEnd()
            $stderr = $proc.StandardError.ReadToEnd()
            $proc.WaitForExit()

            $proc.ExitCode | Should -Be 2
            ($stdout + $stderr) | Should -Match 'ENGRAM_API_TOKEN'
        }

        It 'exits 2 when baseline file does not exist' {
            $result = Invoke-GateRaw -ScriptArgs @('-Baseline', '/nonexistent/path/baseline.json')
            $result.ExitCode | Should -Be 2
            ($result.Stdout + $result.Stderr) | Should -Match 'not found'
        }
    }

    Context 'Vault invariant checks' {
        It 'returns OK when baseline matches' {
            $result = Invoke-GateWithMock -Fingerprint 'aa78e55cf896508c' -CredentialCount 13 -MismatchCount 0
            $result.ExitCode | Should -Be 0
            $result.Stdout   | Should -Match 'SAFETY-GATE: OK'
            $result.Stdout   | Should -Match 'fingerprint=aa78e55cf896508c'
        }

        It 'fails on fingerprint mismatch' {
            $result = Invoke-GateWithMock -Fingerprint 'deadbeefcafebabe' -CredentialCount 13 -MismatchCount 0
            $result.ExitCode | Should -Be 1
            $result.Stderr   | Should -Match 'fingerprint: expected aa78e55cf896508c, got deadbeefcafebabe'
        }

        It 'fails on credential count drift' {
            $result = Invoke-GateWithMock -Fingerprint 'aa78e55cf896508c' -CredentialCount 12 -MismatchCount 0
            $result.ExitCode | Should -Be 1
            $result.Stderr   | Should -Match 'credential_count: expected 13, got 12'
        }

        It 'fails when mismatch_count exceeds max' {
            $result = Invoke-GateWithMock -Fingerprint 'aa78e55cf896508c' -CredentialCount 13 -MismatchCount 1
            $result.ExitCode | Should -Be 1
            $result.Stderr   | Should -Match 'mismatch_count: expected <=0, got 1'
        }

        It 'reports all failures when multiple invariants are violated simultaneously' {
            $result = Invoke-GateWithMock -Fingerprint 'badfpbadfpbadfp0' -CredentialCount 0 -MismatchCount 5
            $result.ExitCode | Should -Be 1
            $result.Stderr   | Should -Match 'fingerprint'
            $result.Stderr   | Should -Match 'credential_count'
            $result.Stderr   | Should -Match 'mismatch_count'
        }

        It 'exits 2 on network error reaching vault endpoint' {
            $result = Invoke-GateWithMock -ThrowNetwork $true
            $result.ExitCode | Should -Be 2
            ($result.Stdout + $result.Stderr) | Should -Match 'unable to reach'
        }

        It 'does not print the token value in any output' {
            $sensitiveToken = 'super-secret-token-value-xyz987'
            $result = Invoke-GateWithMock -Fingerprint 'deadbeefcafebabe' -Token $sensitiveToken
            ($result.Stdout + $result.Stderr) | Should -Not -Match 'super-secret-token-value-xyz987'
        }

        It 'accepts post-us3 phase and uses entities_post_us3 credential count' {
            # post-US3 also expects 13 per baseline, so count 13 should pass
            $result = Invoke-GateWithMock -Fingerprint 'aa78e55cf896508c' -CredentialCount 13 -MismatchCount 0 -Phase 'post-us3'
            $result.ExitCode | Should -Be 0
            $result.Stdout   | Should -Match 'SAFETY-GATE: OK'
        }
    }
}
