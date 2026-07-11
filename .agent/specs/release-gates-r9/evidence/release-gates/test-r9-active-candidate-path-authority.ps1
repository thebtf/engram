[CmdletBinding()]
param(
    [string]$Repository = (Get-Location).Path,
    [string]$Artifact = '.agent/specs/release-gates-r9/evidence/release-gates/active-candidate-authority-harness.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$gate = Join-Path $Repository 'scripts/production-gates/assert-active-candidate-path-authority.ps1'
if (-not (Test-Path -LiteralPath $gate -PathType Leaf)) {
    throw "R9 active-candidate authority gate is missing: $gate"
}

& pwsh -NoProfile -File $gate -SelfTest
if ($LASTEXITCODE -ne 0) { throw "R9 active-candidate authority self-test failed with exit $LASTEXITCODE" }

& pwsh -NoProfile -File $gate `
    -Contract (Join-Path $Repository '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json') `
    -ExpectedContractSha256 'd8e7818d84831f047d30a8493f9c7d2a8cea288d5c381735960d11dd02988ae5' `
    -Plan (Join-Path $Repository '.agent/plans/2026-07-10-engram-production-ready-master-plan.md') `
    -ExpectedPlanSha256 '4388337722e57b48e93515008e4220d6cd2c83de695c4c449387f071c59fb96f' `
    -Artifact (Join-Path $Repository $Artifact)
if ($LASTEXITCODE -ne 0) { throw "R9 active-candidate authority audit failed with exit $LASTEXITCODE" }

$result = Get-Content -LiteralPath (Join-Path $Repository $Artifact) -Raw | ConvertFrom-Json -Depth 100
if ($result.verdict -cne 'PASS') { throw "R9 active-candidate authority artifact verdict is '$($result.verdict)'" }
if ([int]$result.counts.candidates -ne 9) { throw "R9 candidate count is '$($result.counts.candidates)', expected 9" }
if ([int]$result.counts.paths -ne 123) { throw "R9 frozen path count is '$($result.counts.paths)', expected 123" }
if ([int]$result.counts.pending_contracts -ne 2) { throw "R9 pending contract count is '$($result.counts.pending_contracts)', expected 2" }

Write-Output 'R9 ACTIVE-CANDIDATE AUTHORITY HARNESS PASS'
