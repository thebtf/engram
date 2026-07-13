[CmdletBinding()]
param(
    [string]$Contract = '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json',
    [string]$ExpectedContractSha256,
    [string]$Plan = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md',
    [string]$ExpectedPlanSha256,
    [string]$Artifact = '.agent/e/rg9/active-candidate-path-authority.json',
    [switch]$VerifyAvailableGit,
    [switch]$RequireGitObjects,
    [string]$ProbeSlice,
    [string]$ProbeBase,
    [string]$ProbeHead,
    [switch]$PrintCanonicalContractSha256,
    [switch]$SelfTest
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Text)
    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($Path), $Text, [System.Text.UTF8Encoding]::new($false))
}

function Get-CanonicalUtf8LfSha256 {
    param([Parameter(Mandatory)][string]$Path)
    $text = [System.IO.File]::ReadAllText([System.IO.Path]::GetFullPath($Path))
    $canonical = ($text -replace "`r`n", "`n") -replace "`r", "`n"
    $bytes = [System.Text.UTF8Encoding]::new($false).GetBytes($canonical)
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Get-PathSetSha256 {
    param([Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Paths)
    [string[]]$values = @($Paths | ForEach-Object { [string]$_ })
    $payload = ($values -join "`n") + "`n"
    $bytes = [System.Text.UTF8Encoding]::new($false).GetBytes($payload)
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Get-PropertyValue {
    param([AllowNull()]$Object, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Object) { return $null }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Get-OptionalStringArray {
    param([AllowNull()]$Object, [Parameter(Mandatory)][string]$Name)
    [string[]]$values = @(
        foreach ($value in @((Get-PropertyValue $Object $Name))) {
            if ($null -ne $value -and -not [string]::IsNullOrWhiteSpace([string]$value)) {
                [string]$value
            }
        }
    )
    return $values
}

function Split-MarkdownRow {
    param([Parameter(Mandatory)][string]$Line)
    $cells = [System.Collections.Generic.List[string]]::new()
    $builder = [System.Text.StringBuilder]::new()
    $inCode = $false
    for ($index = 0; $index -lt $Line.Length; $index++) {
        $character = $Line[$index]
        if ($character -eq '`' -and ($index -eq 0 -or $Line[$index - 1] -ne '\')) {
            $inCode = -not $inCode
            [void]$builder.Append($character)
            continue
        }
        if ($character -eq '|' -and -not $inCode) {
            $cells.Add($builder.ToString().Trim())
            [void]$builder.Clear()
            continue
        }
        [void]$builder.Append($character)
    }
    $cells.Add($builder.ToString().Trim())
    if ($cells.Count -gt 0 -and [string]::IsNullOrWhiteSpace($cells[0])) { $cells.RemoveAt(0) }
    if ($cells.Count -gt 0 -and [string]::IsNullOrWhiteSpace($cells[$cells.Count - 1])) { $cells.RemoveAt($cells.Count - 1) }
    return @($cells)
}

function Get-CodeSpans {
    param([Parameter(Mandatory)][string]$Text)
    return @([regex]::Matches($Text, '`(?<value>[^`\r\n]+)`') | ForEach-Object { $_.Groups['value'].Value })
}

function Get-MarkdownSection {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$StartPattern, [Parameter(Mandatory)][string]$EndPattern)
    $start = [regex]::Match($Text, $StartPattern, [System.Text.RegularExpressions.RegexOptions]::Multiline)
    if (-not $start.Success) { throw "plan section '$StartPattern' is missing" }
    $tail = $Text.Substring($start.Index + $start.Length)
    $end = [regex]::Match($tail, $EndPattern, [System.Text.RegularExpressions.RegexOptions]::Multiline)
    if (-not $end.Success) { return $tail }
    return $tail.Substring(0, $end.Index)
}

function Get-TableRows {
    param([Parameter(Mandatory)][string]$Section, [Parameter(Mandatory)][string]$HeaderFirstCell)
    $lines = $Section -split "`r?`n"
    $headerIndex = -1
    for ($index = 0; $index -lt $lines.Count; $index++) {
        if (-not $lines[$index].TrimStart().StartsWith('|')) { continue }
        [object[]]$cells = @(Split-MarkdownRow $lines[$index])
        if ($cells.Count -gt 0 -and [string]$cells[0] -ceq $HeaderFirstCell) { $headerIndex = $index; break }
    }
    if ($headerIndex -lt 0) { throw "Markdown table '$HeaderFirstCell' is missing" }
    $rows = [System.Collections.Generic.List[object]]::new()
    for ($index = $headerIndex + 1; $index -lt $lines.Count; $index++) {
        $line = $lines[$index]
        if ([string]::IsNullOrWhiteSpace($line)) { if ($rows.Count -gt 0) { break }; continue }
        if (-not $line.TrimStart().StartsWith('|')) { if ($rows.Count -gt 0) { break }; continue }
        [object[]]$cells = @(Split-MarkdownRow $line)
        if ($cells.Count -gt 0 -and [string]$cells[0] -match '^:?-{3,}:?$') { continue }
        $rows.Add([pscustomobject][ordered]@{ line_number = $index + 1; cells = $cells; raw = $line })
    }
    return @($rows)
}

function Normalize-AuthorityPath {
    param([Parameter(Mandatory)][string]$Token)
    $value = $Token.Trim().Replace('\', '/')
    if ([string]::IsNullOrWhiteSpace($value)) { throw 'authority path is empty' }
    $isPrefix = $value.EndsWith('/**', [System.StringComparison]::Ordinal)
    $basePath = if ($isPrefix) { $value.Substring(0, $value.Length - 3) } else { $value }
    if ($basePath.StartsWith('/') -or $basePath -match '^[A-Za-z]:' -or $basePath.Contains('//')) { throw "authority path must be repository-relative: '$value'" }
    if ($basePath -match '(^|/)\.\.?(?:/|$)') { throw "authority path contains traversal: '$value'" }
    if ($basePath -notmatch '^[A-Za-z0-9._/-]+$') { throw "authority path contains unsupported characters: '$value'" }
    if ($value -match '[*?\[\]{}]' -and -not $isPrefix) { throw "only a terminal /** prefix is allowed: '$value'" }
    if ($isPrefix -and ([string]::IsNullOrWhiteSpace($basePath) -or $basePath -match '[*?\[\]{}]')) { throw "authority prefix is invalid: '$value'" }
    $normalized = $basePath.TrimEnd('/')
    return [pscustomobject][ordered]@{ path = $normalized; display = if ($isPrefix) { $normalized + '/**' } else { $normalized }; kind = if ($isPrefix) { 'prefix' } else { 'exact' } }
}

function Test-DeclarationMatchesPath {
    param([Parameter(Mandatory)]$Declaration, [Parameter(Mandatory)][string]$Path)
    if ([string]$Declaration.kind -ceq 'exact') { return [string]$Declaration.path -ceq $Path }
    return $Path.StartsWith(([string]$Declaration.path).TrimEnd('/') + '/', [System.StringComparison]::Ordinal)
}

function Get-PlanAuthority {
    param([Parameter(Mandatory)][string]$PlanText)
    $errors = [System.Collections.Generic.List[string]]::new()
    $slices = [System.Collections.Generic.List[object]]::new()
    $declarations = [System.Collections.Generic.List[object]]::new()
    try {
        $section = Get-MarkdownSection $PlanText '^## 4\. Worktree and Ownership Matrix\s*$' '^### 4\.1\s+'
        [object[]]$rows = @(Get-TableRows $section 'Slice')
        foreach ($row in $rows) {
            if (@($row.cells).Count -lt 3) { $errors.Add("line $($row.line_number): ownership row is malformed"); continue }
            $slice = ([string]$row.cells[0]).Trim().Trim('`')
            [object[]]$branchSpans = @(Get-CodeSpans ([string]$row.cells[1]))
            $branch = if ($branchSpans.Count -gt 0) { [string]$branchSpans[0] } else { ([string]$row.cells[1]).Trim() }
            if ($branch -notmatch '^work/[A-Za-z0-9._/-]+$') { continue }
            [object[]]$pathTokens = @(Get-CodeSpans ([string]$row.cells[2]))
            $slices.Add([pscustomobject][ordered]@{ slice = $slice; branch = $branch; line = $row.line_number })
            foreach ($token in $pathTokens) {
                try {
                    $normalized = Normalize-AuthorityPath ([string]$token)
                    $declarations.Add([pscustomobject][ordered]@{ owner = $slice; branch = $branch; path = $normalized.path; display = $normalized.display; kind = $normalized.kind; line = $row.line_number })
                }
                catch { $errors.Add("line $($row.line_number): $($_.Exception.Message)") }
            }
        }
    }
    catch { $errors.Add($_.Exception.Message) }
    return [pscustomobject][ordered]@{ slices = @($slices); declarations = @($declarations); errors = @($errors) }
}

function Get-GitDiffEntries {
    param([Parameter(Mandatory)][string]$Repository, [Parameter(Mandatory)][string]$Base, [Parameter(Mandatory)][string]$Head)
    $raw = @(& git -C $Repository -c core.quotepath=false diff --name-status --find-renames --find-copies "$Base..$Head" -- 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "git diff failed for $Base..$Head`: $($raw -join ' ')" }
    $entries = [System.Collections.Generic.List[object]]::new()
    foreach ($line in $raw) {
        if ([string]::IsNullOrWhiteSpace([string]$line)) { continue }
        $parts = ([string]$line) -split "`t"
        if ($parts.Count -lt 2) { throw "git diff emitted malformed name-status line '$line'" }
        $status = [string]$parts[0]
        $expected = if ($status -match '^[RC][0-9]+$') { 2 } else { 1 }
        if (($parts.Count - 1) -ne $expected) { throw "git diff status '$status' emitted $($parts.Count - 1) paths, expected $expected" }
        for ($index = 1; $index -lt $parts.Count; $index++) {
            $normalized = Normalize-AuthorityPath ([string]$parts[$index])
            if ($normalized.kind -ne 'exact') { throw "git diff path cannot be a prefix: '$($parts[$index])'" }
            $entries.Add([pscustomobject][ordered]@{ path = $normalized.path; git_status = $status })
        }
    }
    return @($entries)
}

function Test-CommitAvailable {
    param([Parameter(Mandatory)][string]$Repository, [Parameter(Mandatory)][string]$Commit)
    & git -C $Repository cat-file -e "$Commit`^{commit}" 2>$null
    return $LASTEXITCODE -eq 0
}

function Invoke-PendingSurfaceAudit {
    param([Parameter(Mandatory)]$Pending, [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$OwnerDeclarations, [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Entries)
    $errors = [System.Collections.Generic.List[string]]::new()
    $violations = [System.Collections.Generic.List[object]]::new()
    $allowed = [System.Collections.Generic.List[object]]::new()
    [string[]]$exactPaths = @(Get-OptionalStringArray $Pending 'exact_paths')
    foreach ($path in $exactPaths) {
        try { $allowed.Add((Normalize-AuthorityPath ([string]$path))) } catch { $errors.Add($_.Exception.Message) }
    }
    foreach ($declaration in @($OwnerDeclarations | Where-Object { ([string]$_.path).StartsWith('.agent', [System.StringComparison]::Ordinal) })) { $allowed.Add($declaration) }
    [string[]]$forbidden = @(Get-OptionalStringArray $Pending 'forbidden_final_paths')
    foreach ($entry in @($Entries)) {
        $path = [string]$entry.path
        $isForbidden = @($forbidden | Where-Object { [string]$_ -ceq $path }).Count -gt 0
        $matches = @($allowed | Where-Object { Test-DeclarationMatchesPath $_ $path })
        if ($isForbidden -or $matches.Count -eq 0) {
            $reason = if ($isForbidden) { 'path is explicitly forbidden in a pending final diff' } else { 'path is outside pending exact product/test paths and bounded plan-owned .agent evidence/report declarations' }
            $violations.Add([pscustomobject][ordered]@{ path = $path; git_status = [string]$entry.git_status; reason = $reason })
        }
    }
    if (@($Entries).Count -eq 0) { $errors.Add('pending probe diff contains zero paths') }
    foreach ($violation in $violations) { $errors.Add("pending path violation '$($violation.path)': $($violation.reason)") }
    return [pscustomobject][ordered]@{ verdict = if ($errors.Count -eq 0) { 'PASS_PENDING_SURFACE_ONLY' } else { 'FAIL' }; allowed_declarations = @($allowed); violations = @($violations); errors = @($errors) }
}

function Invoke-R9ProfileAudit {
    param([Parameter(Mandatory)]$ContractObject, [Parameter(Mandatory)]$PlanAuthority)
    $errors = [System.Collections.Generic.List[string]]::new()
    $authority = Get-PropertyValue $ContractObject 'authority'
    foreach ($expected in ([ordered]@{
        plan_path = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md'
        scope_map_path = '.agent/plans/2026-07-10-engram-production-ready-scope-map.json'
        ownership_state_path = '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json'
    }).GetEnumerator()) {
        if ([string](Get-PropertyValue $authority ([string]$expected.Key)) -cne [string]$expected.Value) { $errors.Add("authority $([string]$expected.Key) drifted") }
    }
    if ([string](Get-PropertyValue $authority 'rejected_r8_head') -cne '406fe952c143eb8aaf5895427c568a41d4cec225') { $errors.Add('authority rejected_r8_head drifted') }
    if ([string](Get-PropertyValue $authority 'r8_scope_provenance_sha256') -cne 'ab5f882fa110ca823a317061ecbca0c62516702735325893a56206f9e7a29415') { $errors.Add('authority r8_scope_provenance_sha256 drifted') }
    $sourceAudit = Get-PropertyValue $ContractObject 'source_audit'
    if ([string](Get-PropertyValue $sourceAudit 'mutable_register_path') -cne '.agent/reports/production-readiness-evidence-register.json') { $errors.Add('source_audit mutable register path drifted') }
    if ([string](Get-PropertyValue $sourceAudit 'observed_sha256') -notmatch '^[0-9a-f]{64}$') { $errors.Add('source_audit observed_sha256 must be lowercase full SHA-256 provenance') }
    $observedAt = [DateTimeOffset]::MinValue
    if (-not [DateTimeOffset]::TryParse([string](Get-PropertyValue $sourceAudit 'observed_updated_at'), [ref]$observedAt)) { $errors.Add('source_audit observed_updated_at is invalid') }
    if ([string](Get-PropertyValue $sourceAudit 'use') -cne 'discovery-only; never required by the frozen CI gate') { $errors.Add('source_audit use must remain discovery-only and never acceptance authority') }
    $digestContract = Get-PropertyValue $ContractObject 'digest_contract'
    if ([string](Get-PropertyValue $digestContract 'algorithm') -cne 'SHA-256') { $errors.Add('digest algorithm drifted') }
    if ([string](Get-PropertyValue $digestContract 'serialization') -cne 'ordinally sorted normalized repository paths, one UTF-8 path plus LF per entry') { $errors.Add('digest serialization drifted') }
    if ([string](Get-PropertyValue $digestContract 'path_case') -cne 'ordinal-case-sensitive') { $errors.Add('digest path_case drifted') }
    [object[]]$candidates = @((Get-PropertyValue $ContractObject 'candidates'))
    [object[]]$pending = @((Get-PropertyValue $ContractObject 'pending_namespaces'))
    [object[]]$excluded = @((Get-PropertyValue $ContractObject 'excluded_resolvable_rows'))
    if ($candidates.Count -ne 9) { $errors.Add("R9 must freeze 9 candidate contracts; found $($candidates.Count)") }
    if ((@($candidates | ForEach-Object { [int](Get-PropertyValue $_ 'path_count') }) | Measure-Object -Sum).Sum -ne 123) { $errors.Add('R9 must freeze exactly 123 paths') }
    if ($pending.Count -ne 2) { $errors.Add("R9 must carry exactly two pending contracts; found $($pending.Count)") }
    $securityR3 = @($candidates | Where-Object { [string]$_.slice -ceq 'SECURITY-PROJECT-IDENTITY' })
    if ($securityR3.Count -ne 1 -or [string]$securityR3[0].status_class -cne 'rejected-security-r3') { $errors.Add('SECURITY-PROJECT-IDENTITY R3 must be frozen as rejected history') }
    $securityPending = @($pending | Where-Object { [string]$_.slice -ceq 'SECURITY-PROJECT-IDENTITY' })
    if ($securityPending.Count -ne 1) { $errors.Add('SECURITY-PROJECT-IDENTITY R4 pending contract is missing') }
    else {
        if ([string]$securityPending[0].branch -cne 'work/prc-security-project-identity-r4' -or [string]$securityPending[0].base_anchor -cne '38344455754fe503acbd79d2134141f996adff7f') { $errors.Add('SECURITY-PROJECT-IDENTITY R4 branch/base drifted') }
        if ([string]$securityPending[0].forbidden_base -cne '0d84047c280a873dd21baae2ecbf83ec422d497f') { $errors.Add('SECURITY-PROJECT-IDENTITY checker-only commit is not forbidden as the R4 base') }
        [object[]]$exact = @((Get-PropertyValue $securityPending[0] 'exact_paths'))
        if ('internal/proxy/identity_process_test.go' -cnotin $exact -or 'internal/proxy/identity_test.go' -cnotin $exact) { $errors.Add('R4 pending test surface is incomplete') }
        if ('internal/proxy/identity.go' -cnotin @((Get-PropertyValue $securityPending[0] 'forbidden_final_paths'))) { $errors.Add('R4 pending contract must forbid final identity.go mutation') }
    }
    $r6 = @($pending | Where-Object { [string]$_.slice -ceq 'DB-EMBEDDING-EVIDENCE-TRANSPORT' })
    if ($r6.Count -ne 1 -or [string]$r6[0].base_anchor -cne 'a538f6224ef31f612152470a4ecd45e78ff9d0f2') { $errors.Add('DB embedding R6 pending exact base is missing') }
    else {
        [object[]]$r6Declarations = @($PlanAuthority.declarations | Where-Object { [string]$_.owner -ceq 'DB-EMBEDDING-EVIDENCE-TRANSPORT' -and ([string]$_.path).StartsWith('.agent', [System.StringComparison]::Ordinal) })
        $requiredR6Surface = @(
            '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport',
            '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport-r3',
            '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport-r4',
            '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport-r5',
            '.agent/reports/evidence/production-ready/db-embedding-stats-evidence-transport-r6',
            '.agent/specs/db-embedding-stats-evidence-transport/evidence'
        )
        foreach ($path in $requiredR6Surface) { if (@($r6Declarations | Where-Object { [string]$_.path -ceq $path -and [string]$_.kind -ceq 'prefix' }).Count -ne 1) { $errors.Add("R6 full bounded evidence surface is missing '$path/**'") } }
    }
    foreach ($required in @(@('DB-REAPER','rejected-path-authority-conflict'), @('DEMOLITION-SKIP-CLASSIFICATION','checker-classification'))) {
        $row = @($excluded | Where-Object { [string]$_.slice -ceq $required[0] })
        if ($row.Count -ne 1 -or -not ([string]$row[0].disposition).Contains([string]$required[1])) { $errors.Add("R9 excluded disposition for '$($required[0])' is missing") }
    }
    $securityDeclarations = @($PlanAuthority.declarations | Where-Object { [string]$_.owner -ceq 'SECURITY-PROJECT-IDENTITY' })
    if (@($securityDeclarations | Where-Object { Test-DeclarationMatchesPath $_ '.agent/testing/SECURITY-PROJECT-IDENTITY-R4/behavior-signal.md' }).Count -gt 0) { $errors.Add('R9 must not silently authorize the R4 .agent/testing behavior-signal path') }
    return @($errors)
}

function Invoke-R12ContractAudit {
    param(
        [Parameter(Mandatory)]$ContractObject,
        [Parameter(Mandatory)][string]$ObservedContractSha256,
        [Parameter(Mandatory)][string]$ExpectedContractSha256Value,
        [Parameter(Mandatory)][string]$ObservedPlanSha256,
        [Parameter(Mandatory)][string]$ExpectedPlanSha256Value,
        [string]$Repository,
        [switch]$VerifyGit,
        [switch]$RequireObjects,
        [AllowNull()]$Probe
    )
    $errors = [System.Collections.Generic.List[string]]::new()
    if ($ObservedContractSha256 -cne $ExpectedContractSha256Value.ToLowerInvariant()) { $errors.Add("contract SHA256 mismatch: expected=$ExpectedContractSha256Value observed=$ObservedContractSha256") }
    if ($ObservedPlanSha256 -cne $ExpectedPlanSha256Value.ToLowerInvariant()) { $errors.Add("plan SHA256 mismatch: expected=$ExpectedPlanSha256Value observed=$ObservedPlanSha256") }
    $schemaVersion = Get-PropertyValue $ContractObject 'schema_version'
    $kind = Get-PropertyValue $ContractObject 'kind'
    $revision = Get-PropertyValue $ContractObject 'revision'
    if ($schemaVersion -isnot [long] -or [long]$schemaVersion -ne 1 -or $kind -isnot [string] -or [string]$kind -cne 'production-ready-active-diff-contracts' -or $revision -isnot [long] -or [long]$revision -ne 12) {
        $errors.Add('contract identity must be production-ready-active-diff-contracts revision 12')
    }
    $statusClasses = Get-PropertyValue $ContractObject 'status_classes'
    [string[]]$currentStatuses = @((Get-PropertyValue $statusClasses 'current') | ForEach-Object { [string]$_ })
    [string[]]$rejectedStatuses = @((Get-PropertyValue $statusClasses 'rejected') | ForEach-Object { [string]$_ })
    [object[]]$candidates = @((Get-PropertyValue $ContractObject 'candidates'))
    $candidateResults = [System.Collections.Generic.List[object]]::new()
    $gitResults = [System.Collections.Generic.List[object]]::new()
    $seenSlices = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($candidate in $candidates) {
        $slice = [string](Get-PropertyValue $candidate 'slice')
        $statusClass = [string](Get-PropertyValue $candidate 'status_class')
        $branch = [string](Get-PropertyValue $candidate 'branch')
        $base = [string](Get-PropertyValue $candidate 'base')
        $head = [string](Get-PropertyValue $candidate 'head')
        if ([string]::IsNullOrWhiteSpace($slice) -or -not $seenSlices.Add($slice)) { $errors.Add("candidate slice is blank or duplicated: '$slice'") }
        if ($statusClass -cnotin @($currentStatuses + $rejectedStatuses)) { $errors.Add("candidate '$slice' has unknown status class '$statusClass'") }
        if ($branch -notmatch '^work/[A-Za-z0-9._/-]+$') { $errors.Add("candidate '$slice' has invalid branch '$branch'") }
        if ($base -notmatch '^[0-9a-f]{40}$' -or $head -notmatch '^[0-9a-f]{40}$' -or $base -ceq $head) { $errors.Add("candidate '$slice' must bind distinct full lowercase base/head commits") }
        if ([string]::IsNullOrWhiteSpace([string](Get-PropertyValue $candidate 'plan_owner'))) { $errors.Add("candidate '$slice' has no plan owner") }
        if ((Get-PropertyValue $candidate 'path_authority_eligible') -isnot [bool] -or -not [bool](Get-PropertyValue $candidate 'path_authority_eligible')) { $errors.Add("candidate '$slice' is not path-authority eligible") }
        if ((Get-PropertyValue $candidate 'release_accepted') -isnot [bool] -or [bool](Get-PropertyValue $candidate 'release_accepted')) { $errors.Add("candidate '$slice' must not claim release acceptance") }
        [object[]]$pathObjects = @((Get-PropertyValue $candidate 'paths'))
        [string[]]$paths = @($pathObjects | ForEach-Object { [string](Get-PropertyValue $_ 'path') })
        [string[]]$sortedPaths = @($paths); [Array]::Sort($sortedPaths, [System.StringComparer]::Ordinal)
        if (($paths -join "`n") -cne ($sortedPaths -join "`n")) { $errors.Add("candidate '$slice' paths are not ordinally sorted") }
        if (@($paths | Sort-Object -Unique).Count -ne $paths.Count) { $errors.Add("candidate '$slice' repeats a path") }
        $pathCount = Get-PropertyValue $candidate 'path_count'
        if ($pathCount -isnot [long] -or [long]$pathCount -ne $paths.Count) { $errors.Add("candidate '$slice' path_count must be a JSON integer matching paths") }
        $digest = Get-PathSetSha256 $paths
        if ($digest -cne [string](Get-PropertyValue $candidate 'paths_sha256')) { $errors.Add("candidate '$slice' path digest mismatch") }
        foreach ($pathObject in $pathObjects) {
            $path = [string](Get-PropertyValue $pathObject 'path')
            try { $normalized = Normalize-AuthorityPath $path; if ($normalized.kind -ne 'exact' -or $normalized.path -cne $path) { throw "candidate path is not normalized exact: '$path'" } } catch { $errors.Add("candidate '$slice': $($_.Exception.Message)"); continue }
            if ([string](Get-PropertyValue $pathObject 'git_status') -cnotin @('A','M','D','T')) { $errors.Add("candidate '$slice' path '$path' has unsupported git status") }
            if ([string](Get-PropertyValue $pathObject 'classification') -cnotin @('product','evidence','report')) { $errors.Add("candidate '$slice' path '$path' has unsupported classification") }
        }
        $candidateResults.Add([pscustomobject][ordered]@{slice=$slice;status_class=$statusClass;branch=$branch;base=$base;head=$head;path_count=$paths.Count;paths_sha256=$digest})
        if ($VerifyGit) {
            $baseAvailable = Test-CommitAvailable $Repository $base; $headAvailable = Test-CommitAvailable $Repository $head
            if (-not ($baseAvailable -and $headAvailable)) {
                $gitResults.Add([pscustomobject][ordered]@{slice=$slice;available=$false;verified=$false})
                if ($RequireObjects) { $errors.Add("candidate '$slice' Git objects are unavailable") }
            }
            else {
                try {
                    [object[]]$actual = @(Get-GitDiffEntries $Repository $base $head)
                    [string[]]$actualPaths = @($actual | ForEach-Object { [string]$_.path }); [Array]::Sort($actualPaths,[System.StringComparer]::Ordinal)
                    $actualDigest = Get-PathSetSha256 $actualPaths
                    $statusMismatch = $false
                    foreach ($pathObject in $pathObjects) {
                        $expectedPath = [string](Get-PropertyValue $pathObject 'path')
                        $expectedStatus = [string](Get-PropertyValue $pathObject 'git_status')
                        if (@($actual | Where-Object { [string]$_.path -ceq $expectedPath -and [string]$_.git_status -ceq $expectedStatus }).Count -ne 1) { $statusMismatch = $true }
                    }
                    if ($actualDigest -cne $digest) { $errors.Add("candidate '$slice' live Git path digest differs from the frozen contract") }
                    if ($statusMismatch) { $errors.Add("candidate '$slice' live Git statuses differ from the frozen contract") }
                    $gitResults.Add([pscustomobject][ordered]@{slice=$slice;available=$true;verified=($actualDigest -ceq $digest -and -not $statusMismatch);path_count=$actualPaths.Count;paths_sha256=$actualDigest})
                }
                catch { $errors.Add("candidate '$slice' Git verification failed: $($_.Exception.Message)") }
            }
        }
    }
    [object[]]$pendingContracts = @((Get-PropertyValue $ContractObject 'pending_namespaces'))
    $pendingResults = [System.Collections.Generic.List[object]]::new()
    foreach ($pending in $pendingContracts) {
        $slice = [string](Get-PropertyValue $pending 'slice')
        $branch = [string](Get-PropertyValue $pending 'branch')
        $baseAnchor = [string](Get-PropertyValue $pending 'base_anchor')
        if ([string]::IsNullOrWhiteSpace($slice) -or [string]::IsNullOrWhiteSpace([string](Get-PropertyValue $pending 'status_class'))) { $errors.Add('pending authority entry has blank identity/status') }
        if ($branch -notmatch '^work/[A-Za-z0-9._/-]+$' -or $baseAnchor -notmatch '^[0-9a-f]{40}$') { $errors.Add("pending '$slice' has invalid branch or base anchor") }
        if ((Get-PropertyValue $pending 'release_accepted') -isnot [bool] -or [bool](Get-PropertyValue $pending 'release_accepted')) { $errors.Add("pending '$slice' must not claim release acceptance") }
        [string[]]$exactPaths = @(Get-OptionalStringArray $pending 'exact_paths')
        [string[]]$exactPrefixes = @(Get-OptionalStringArray $pending 'exact_prefixes')
        $macroBound = [string](Get-PropertyValue $pending 'authority_mode') -ceq 'bound-plan-member-union-with-exact-r12-overrides' -and @((Get-PropertyValue $pending 'member_slices')).Count -gt 0 -and @((Get-PropertyValue $pending 'exact_r12_overrides')).Count -gt 0
        if ($exactPaths.Count + $exactPrefixes.Count -eq 0 -and -not $macroBound) { $errors.Add("pending '$slice' declares no bounded path") }
        $seenPending = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
        foreach ($token in @($exactPaths) + @($exactPrefixes)) {
            try { $normalized = Normalize-AuthorityPath $token; if ($normalized.display -cne $token -or -not $seenPending.Add($token)) { throw "pending path is non-canonical or repeated: '$token'" } } catch { $errors.Add("pending '$slice': $($_.Exception.Message)") }
        }
        [string[]]$forbidden = @(Get-OptionalStringArray $pending 'forbidden_final_paths')
        $seenForbidden = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
        foreach ($token in $forbidden) {
            try {
                $normalizedForbidden = Normalize-AuthorityPath $token
                if ($normalizedForbidden.kind -cne 'exact' -or $normalizedForbidden.display -cne $token -or -not $seenForbidden.Add($token)) { throw "forbidden path is non-canonical, non-exact, or repeated: '$token'" }
                $coveredByPrefix = @($exactPrefixes | Where-Object {
                    $normalizedPrefix = Normalize-AuthorityPath $_
                    $token.StartsWith($normalizedPrefix.path.TrimEnd('/') + '/', [System.StringComparison]::Ordinal)
                }).Count -gt 0
                if ($token -cin $exactPaths -or $coveredByPrefix) { $errors.Add("pending '$slice' both allows and forbids '$token'") }
            }
            catch { $errors.Add("pending '$slice': $($_.Exception.Message)") }
        }
        $pendingResults.Add([pscustomobject][ordered]@{slice=$slice;branch=$branch;base_anchor=$baseAnchor;allowed_exact_paths=@($exactPaths);allowed_prefixes=@($exactPrefixes);macro_bound_plan_union=$macroBound;requires_final_exact_diff_contract=$true})
    }
    $probeResult = $null
    if ($null -ne $Probe) {
        $probeSlice = [string](Get-PropertyValue $Probe 'slice')
        $probeBase = [string](Get-PropertyValue $Probe 'base')
        $probeHead = [string](Get-PropertyValue $Probe 'head')
        $matches = @($pendingContracts | Where-Object { [string](Get-PropertyValue $_ 'slice') -ceq $probeSlice })
        if ($matches.Count -ne 1) { $errors.Add("pending probe slice '$probeSlice' has $($matches.Count) contracts, expected 1") }
        elseif ($probeBase -cne [string](Get-PropertyValue $matches[0] 'base_anchor')) { $errors.Add("pending probe base '$probeBase' must equal frozen base anchor '$([string](Get-PropertyValue $matches[0] 'base_anchor'))'") }
        else {
            try {
                & git -C $Repository merge-base --is-ancestor $probeBase $probeHead 2>$null
                if ($LASTEXITCODE -ne 0) { throw 'pending probe base is not an ancestor of head' }
                [object[]]$entries = @(Get-GitDiffEntries $Repository $probeBase $probeHead)
                [object[]]$declarations = @(
                    foreach ($token in @(Get-OptionalStringArray $matches[0] 'exact_paths') + @(Get-OptionalStringArray $matches[0] 'exact_prefixes')) {
                        $normalized = Normalize-AuthorityPath $token
                        [pscustomobject]@{owner=$probeSlice;kind=$normalized.kind;path=$normalized.path;display=$token}
                    }
                )
                $probeResult = Invoke-PendingSurfaceAudit $matches[0] $declarations $entries
                foreach ($probeError in @($probeResult.errors)) { $errors.Add("pending probe: $probeError") }
            }
            catch { $errors.Add("pending probe failed: $($_.Exception.Message)") }
        }
    }
    return [pscustomobject][ordered]@{
        verdict = if($errors.Count -eq 0){'PASS'}else{'FAIL'}
        counts = [pscustomobject][ordered]@{candidates=$candidates.Count;paths=(@($candidateResults|ForEach-Object path_count)|Measure-Object -Sum).Sum;pending_contracts=$pendingContracts.Count;current_candidates=@($candidateResults|Where-Object status_class -cin $currentStatuses).Count;rejected_candidates=@($candidateResults|Where-Object status_class -cin $rejectedStatuses).Count;git_verified=@($gitResults|Where-Object verified).Count;errors=$errors.Count}
        candidates=@($candidateResults);pending_contracts=@($pendingResults);git_verification=@($gitResults);pending_probe=$probeResult
        source_snapshot=[pscustomobject][ordered]@{freshness='HISTORICAL_DISCOVERY_ONLY';used_for_acceptance=$false;mutable_register_read=$false;observed_sha256=[string](Get-PropertyValue (Get-PropertyValue $ContractObject 'source_audit') 'observed_sha256')}
        errors=@($errors)
    }
}

function Invoke-ContractAudit {
    param(
        [Parameter(Mandatory)]$ContractObject,
        [Parameter(Mandatory)][string]$PlanText,
        [Parameter(Mandatory)][string]$ObservedContractSha256,
        [Parameter(Mandatory)][string]$ExpectedContractSha256Value,
        [Parameter(Mandatory)][string]$ObservedPlanSha256,
        [Parameter(Mandatory)][string]$ExpectedPlanSha256Value,
        [string]$Repository,
        [switch]$VerifyGit,
        [switch]$RequireObjects,
        [AllowNull()]$Probe,
        [switch]$EnforceR9Profile
    )
    if ([int](Get-PropertyValue $ContractObject 'revision') -eq 12) {
        return Invoke-R12ContractAudit -ContractObject $ContractObject -ObservedContractSha256 $ObservedContractSha256 -ExpectedContractSha256Value $ExpectedContractSha256Value -ObservedPlanSha256 $ObservedPlanSha256 -ExpectedPlanSha256Value $ExpectedPlanSha256Value -Repository $Repository -VerifyGit:$VerifyGit -RequireObjects:$RequireObjects -Probe $Probe
    }
    $errors = [System.Collections.Generic.List[string]]::new()
    if ($ObservedContractSha256 -cne $ExpectedContractSha256Value.ToLowerInvariant()) { $errors.Add("contract SHA256 mismatch: expected=$ExpectedContractSha256Value observed=$ObservedContractSha256") }
    if ($ObservedPlanSha256 -cne $ExpectedPlanSha256Value.ToLowerInvariant()) { $errors.Add("plan SHA256 mismatch: expected=$ExpectedPlanSha256Value observed=$ObservedPlanSha256") }
    $planAuthority = Get-PlanAuthority $PlanText
    foreach ($error in @($planAuthority.errors)) { $errors.Add("plan: $error") }
    if ([int](Get-PropertyValue $ContractObject 'schema_version') -ne 1) { $errors.Add('contract schema_version must be 1') }
    if ([string](Get-PropertyValue $ContractObject 'kind') -cne 'production-ready-active-diff-contracts') { $errors.Add('contract kind is not production-ready-active-diff-contracts') }
    if ([int](Get-PropertyValue $ContractObject 'revision') -ne 9) { $errors.Add('contract revision must be 9') }
    $statusClasses = Get-PropertyValue $ContractObject 'status_classes'
    [object[]]$currentStatuses = @((Get-PropertyValue $statusClasses 'current'))
    [object[]]$rejectedStatuses = @((Get-PropertyValue $statusClasses 'rejected'))
    [object[]]$pendingStatuses = @((Get-PropertyValue $statusClasses 'pending'))
    $allCandidateStatuses = @($currentStatuses) + @($rejectedStatuses)
    [object[]]$candidates = @((Get-PropertyValue $ContractObject 'candidates'))
    $seenCandidates = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    $candidateResults = [System.Collections.Generic.List[object]]::new()
    $gitResults = [System.Collections.Generic.List[object]]::new()
    foreach ($candidate in $candidates) {
        $slice = [string](Get-PropertyValue $candidate 'slice')
        $owner = [string](Get-PropertyValue $candidate 'plan_owner')
        $statusClass = [string](Get-PropertyValue $candidate 'status_class')
        $branch = [string](Get-PropertyValue $candidate 'branch')
        if ([string]::IsNullOrWhiteSpace($slice) -or -not $seenCandidates.Add($slice)) { $errors.Add("candidate slice is empty or duplicated: '$slice'") }
        if ($statusClass -cnotin $allCandidateStatuses) { $errors.Add("candidate '$slice' has unknown status class '$statusClass'") }
        if ($branch -notmatch '^work/[A-Za-z0-9._/-]+$') { $errors.Add("candidate '$slice' has invalid branch '$branch'") }
        $base = [string](Get-PropertyValue $candidate 'base'); $head = [string](Get-PropertyValue $candidate 'head')
        if ($base -notmatch '^[0-9a-f]{40}$' -or $head -notmatch '^[0-9a-f]{40}$' -or $base -ceq $head) { $errors.Add("candidate '$slice' must have distinct full lowercase base/head commits") }
        [object[]]$ownerRows = @($planAuthority.slices | Where-Object { [string]$_.slice -ceq $owner })
        if ($ownerRows.Count -ne 1) { $errors.Add("candidate '$slice' plan owner '$owner' has $($ownerRows.Count) maker rows, expected 1") }
        elseif ($statusClass -cin $currentStatuses -and [string]$ownerRows[0].branch -cne $branch) { $errors.Add("current candidate '$slice' branch '$branch' differs from plan '$($ownerRows[0].branch)'") }
        [object[]]$declarations = @($planAuthority.declarations | Where-Object { [string]$_.owner -ceq $owner })
        [object[]]$pathObjects = @((Get-PropertyValue $candidate 'paths'))
        [string[]]$paths = @($pathObjects | ForEach-Object { [string](Get-PropertyValue $_ 'path') })
        [string[]]$sorted = @($paths); [Array]::Sort($sorted, [System.StringComparer]::Ordinal)
        if (($paths -join "`n") -cne ($sorted -join "`n")) { $errors.Add("candidate '$slice' paths are not ordinally sorted") }
        if (@($paths | Select-Object -Unique).Count -ne $paths.Count) { $errors.Add("candidate '$slice' repeats a path") }
        if ([int](Get-PropertyValue $candidate 'path_count') -ne $paths.Count) { $errors.Add("candidate '$slice' path_count does not match paths") }
        $digest = Get-PathSetSha256 $paths
        if ($digest -cne [string](Get-PropertyValue $candidate 'paths_sha256')) { $errors.Add("candidate '$slice' path digest mismatch") }
        foreach ($pathObject in $pathObjects) {
            $path = [string](Get-PropertyValue $pathObject 'path')
            try { $normalized = Normalize-AuthorityPath $path; if ($normalized.kind -ne 'exact' -or $normalized.path -cne $path) { throw "candidate path is not normalized exact: '$path'" } } catch { $errors.Add("candidate '$slice': $($_.Exception.Message)"); continue }
            $classification = [string](Get-PropertyValue $pathObject 'classification')
            if ($classification -cnotin @('product','evidence','report')) { $errors.Add("candidate '$slice' path '$path' has invalid classification '$classification'") }
            elseif ($classification -ceq 'product' -and $path.StartsWith('.agent/', [System.StringComparison]::Ordinal)) { $errors.Add("candidate '$slice' .agent path '$path' is misclassified as product") }
            elseif ($classification -ceq 'evidence' -and -not $path.StartsWith('.agent/', [System.StringComparison]::Ordinal)) { $errors.Add("candidate '$slice' non-.agent path '$path' is misclassified as evidence") }
            elseif ($classification -ceq 'report' -and -not $path.StartsWith('.agent/reports/', [System.StringComparison]::Ordinal)) { $errors.Add("candidate '$slice' report '$path' is outside .agent/reports") }
            if (@($declarations | Where-Object { Test-DeclarationMatchesPath $_ $path }).Count -eq 0) { $errors.Add("candidate '$slice' path '$path' is not allowed by plan owner '$owner'") }
        }
        if ((Get-PropertyValue $candidate 'path_authority_eligible') -isnot [bool] -or -not [bool](Get-PropertyValue $candidate 'path_authority_eligible')) { $errors.Add("candidate '$slice' must be path-authority eligible") }
        if ((Get-PropertyValue $candidate 'release_accepted') -isnot [bool] -or [bool](Get-PropertyValue $candidate 'release_accepted')) { $errors.Add("candidate '$slice' must not claim release acceptance") }
        $candidateResults.Add([pscustomobject][ordered]@{ slice = $slice; status_class = $statusClass; branch = $branch; base = $base; head = $head; path_count = $paths.Count; paths_sha256 = $digest })
        if ($VerifyGit) {
            $baseAvailable = Test-CommitAvailable $Repository $base; $headAvailable = Test-CommitAvailable $Repository $head
            if (-not ($baseAvailable -and $headAvailable)) {
                $gitResults.Add([pscustomobject][ordered]@{ slice = $slice; available = $false; verified = $false })
                if ($RequireObjects) { $errors.Add("candidate '$slice' Git objects are unavailable") }
            }
            else {
                try {
                    [object[]]$actual = @(Get-GitDiffEntries $Repository $base $head)
                    [string[]]$actualPaths = @($actual | ForEach-Object { [string]$_.path }); [Array]::Sort($actualPaths, [System.StringComparer]::Ordinal)
                    $actualDigest = Get-PathSetSha256 $actualPaths
                    $statusMismatch = $false
                    foreach ($pathObject in $pathObjects) {
                        $matches = @($actual | Where-Object { [string]$_.path -ceq [string]$pathObject.path -and [string]$_.git_status -ceq [string]$pathObject.git_status })
                        if ($matches.Count -ne 1) { $statusMismatch = $true }
                    }
                    if ($actualDigest -cne $digest) { $errors.Add("candidate '$slice' live Git path digest differs from frozen contract") }
                    if ($statusMismatch) { $errors.Add("candidate '$slice' live Git statuses differ from frozen contract") }
                    $gitResults.Add([pscustomobject][ordered]@{ slice = $slice; available = $true; verified = ($actualDigest -ceq $digest -and -not $statusMismatch); path_count = $actualPaths.Count; paths_sha256 = $actualDigest })
                }
                catch { $errors.Add("candidate '$slice' Git verification failed: $($_.Exception.Message)") }
            }
        }
    }
    [object[]]$pendingContracts = @((Get-PropertyValue $ContractObject 'pending_namespaces'))
    $pendingResults = [System.Collections.Generic.List[object]]::new()
    foreach ($pending in $pendingContracts) {
        $slice = [string](Get-PropertyValue $pending 'slice'); $owner = [string](Get-PropertyValue $pending 'plan_owner'); $branch = [string](Get-PropertyValue $pending 'branch')
        if ([string](Get-PropertyValue $pending 'status_class') -cnotin $pendingStatuses) { $errors.Add("pending '$slice' has unknown status class") }
        if ([string](Get-PropertyValue $pending 'base_anchor') -notmatch '^[0-9a-f]{40}$') { $errors.Add("pending '$slice' has invalid base anchor") }
        [object[]]$ownerRows = @($planAuthority.slices | Where-Object { [string]$_.slice -ceq $owner })
        if ($ownerRows.Count -ne 1) { $errors.Add("pending '$slice' owner '$owner' has $($ownerRows.Count) maker rows") }
        elseif ([string]$ownerRows[0].branch -cne $branch) { $errors.Add("pending '$slice' branch '$branch' differs from plan '$($ownerRows[0].branch)'") }
        [object[]]$declarations = @($planAuthority.declarations | Where-Object { [string]$_.owner -ceq $owner })
        [string[]]$exactPaths = @(Get-OptionalStringArray $pending 'exact_paths')
        [string[]]$exactPrefixes = @(Get-OptionalStringArray $pending 'exact_prefixes')
        [object[]]$explicit = @($exactPaths) + @($exactPrefixes)
        if ($explicit.Count -eq 0) { $errors.Add("pending '$slice' declares no bounded path") }
        foreach ($token in $explicit) {
            try {
                $normalized = Normalize-AuthorityPath ([string]$token)
                if (@($declarations | Where-Object { [string]$_.kind -ceq $normalized.kind -and [string]$_.path -ceq $normalized.path }).Count -ne 1) { $errors.Add("pending '$slice' explicit path '$token' is not an exact plan declaration") }
            }
            catch { $errors.Add("pending '$slice': $($_.Exception.Message)") }
        }
        [string[]]$forbidden = @(Get-OptionalStringArray $pending 'forbidden_final_paths')
        foreach ($token in $forbidden) { if ([string]$token -cin $exactPaths) { $errors.Add("pending '$slice' both allows and forbids '$token'") } }
        if ((Get-PropertyValue $pending 'release_accepted') -isnot [bool] -or [bool](Get-PropertyValue $pending 'release_accepted')) { $errors.Add("pending '$slice' must not claim release acceptance") }
        [object[]]$effectiveEvidence = @($declarations | Where-Object { ([string]$_.path).StartsWith('.agent', [System.StringComparison]::Ordinal) })
        $pendingResults.Add([pscustomobject][ordered]@{ slice = $slice; branch = $branch; base_anchor = [string]$pending.base_anchor; allowed_exact_paths = @($exactPaths); effective_agent_declarations = @($effectiveEvidence | ForEach-Object display); requires_final_exact_diff_contract = $true })
    }
    if ($EnforceR9Profile) { foreach ($profileError in @(Invoke-R9ProfileAudit $ContractObject $planAuthority)) { $errors.Add("R9 profile: $profileError") } }
    $probeResult = $null
    if ($null -ne $Probe) {
        $probeSlice = [string](Get-PropertyValue $Probe 'slice')
        $matchingPending = @($pendingContracts | Where-Object { [string]$_.slice -ceq $probeSlice })
        if ($matchingPending.Count -ne 1) { $errors.Add("pending probe slice '$probeSlice' has $($matchingPending.Count) contracts, expected 1") }
        else {
            $probeOwner = [string]$matchingPending[0].plan_owner
            [object[]]$probeDeclarations = @($planAuthority.declarations | Where-Object { [string]$_.owner -ceq $probeOwner })
            $expectedProbeBase = [string](Get-PropertyValue $matchingPending[0] 'base_anchor')
            if ([string]$Probe.base -cne $expectedProbeBase) {
                $baseError = "pending probe base '$([string]$Probe.base)' must equal frozen base anchor '$expectedProbeBase'"
                $errors.Add($baseError)
                $probeResult = [pscustomobject][ordered]@{ verdict='FAIL'; requested_base=[string]$Probe.base; expected_base=$expectedProbeBase; head=[string]$Probe.head; allowed_declarations=@(); violations=@(); errors=@($baseError) }
            }
            else {
                try {
                    & git -C $Repository merge-base --is-ancestor ([string]$Probe.base) ([string]$Probe.head) 2>$null
                    $ancestorExit = $LASTEXITCODE
                    if ($ancestorExit -eq 1) { throw "pending probe base '$([string]$Probe.base)' is not an ancestor of head '$([string]$Probe.head)'" }
                    if ($ancestorExit -ne 0) { throw "git merge-base --is-ancestor failed with exit $ancestorExit" }
                    [object[]]$probeEntries = @(Get-GitDiffEntries $Repository ([string]$Probe.base) ([string]$Probe.head))
                    $probeResult = Invoke-PendingSurfaceAudit $matchingPending[0] $probeDeclarations $probeEntries
                    $probeResult | Add-Member -NotePropertyName requested_base -NotePropertyValue ([string]$Probe.base)
                    $probeResult | Add-Member -NotePropertyName expected_base -NotePropertyValue $expectedProbeBase
                    $probeResult | Add-Member -NotePropertyName head -NotePropertyValue ([string]$Probe.head)
                    $probeResult | Add-Member -NotePropertyName base_is_ancestor -NotePropertyValue $true
                    foreach ($probeError in @($probeResult.errors)) { $errors.Add("pending probe: $probeError") }
                }
                catch { $errors.Add("pending probe failed: $($_.Exception.Message)") }
            }
        }
    }
    return [pscustomobject][ordered]@{
        verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        counts = [pscustomobject][ordered]@{ candidates = $candidates.Count; paths = (@($candidateResults | ForEach-Object path_count) | Measure-Object -Sum).Sum; pending_contracts = $pendingContracts.Count; current_candidates = @($candidateResults | Where-Object { [string]$_.status_class -cin $currentStatuses }).Count; rejected_candidates = @($candidateResults | Where-Object { [string]$_.status_class -cin $rejectedStatuses }).Count; git_verified = @($gitResults | Where-Object verified).Count; errors = $errors.Count }
        candidates = @($candidateResults)
        pending_contracts = @($pendingResults)
        git_verification = @($gitResults)
        pending_probe = $probeResult
        source_snapshot = [pscustomobject][ordered]@{ freshness = 'HISTORICAL_DISCOVERY_ONLY'; used_for_acceptance = $false; mutable_register_read = $false; observed_sha256 = [string](Get-PropertyValue (Get-PropertyValue $ContractObject 'source_audit') 'observed_sha256') }
        errors = @($errors)
    }
}

function Copy-JsonObject {
    param([Parameter(Mandatory)]$Object)
    return ($Object | ConvertTo-Json -Depth 100 | ConvertFrom-Json -Depth 100)
}

function Set-TestCandidateDigest {
    param([Parameter(Mandatory)]$Candidate)
    [object[]]$paths = @($Candidate.paths)
    $Candidate.path_count = $paths.Count
    $Candidate.paths_sha256 = Get-PathSetSha256 @($paths | ForEach-Object path)
}

function Assert-SelfTest {
    param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message)
    if (-not $Condition) { throw "SELFTEST FAIL: $Message" }
}

function Invoke-SelfTest {
    $plan = @'
## 4. Worktree and Ownership Matrix

| Slice | Branch | Exclusive maker paths | Dependencies | Required proof |
| --- | --- | --- | --- | --- |
| A | `work/a` | `src/a.go`, `src/a_test.go`, `.agent/specs/a/evidence/**`, `.agent/reports/a-common/**`, `.agent/reports/a-r3/**`, `.agent/reports/a-r4/**`, `.agent/reports/a-r5/**`, `.agent/reports/a-r6/**`, `.agent/reports/a.md` | none | proof |
| NO-PATHS | checker-only | read-only | none | proof |

### 4.1 Test inventory
'@
    $contract = [pscustomobject][ordered]@{
        schema_version = 1; kind = 'production-ready-active-diff-contracts'; revision = 9
        authority = [pscustomobject]@{ plan_path='.agent/plans/2026-07-10-engram-production-ready-master-plan.md'; scope_map_path='.agent/plans/2026-07-10-engram-production-ready-scope-map.json'; ownership_state_path='.agent/plans/2026-07-10-engram-production-ready-ownership-state.json'; rejected_r8_head='406fe952c143eb8aaf5895427c568a41d4cec225'; r8_scope_provenance_sha256='ab5f882fa110ca823a317061ecbca0c62516702735325893a56206f9e7a29415' }
        source_audit = [pscustomobject]@{ mutable_register_path='.agent/reports/production-readiness-evidence-register.json'; observed_sha256 = ('a' * 64); observed_updated_at='2026-07-11T00:00:00+03:00'; use='discovery-only; never required by the frozen CI gate' }
        digest_contract = [pscustomobject]@{ algorithm='SHA-256'; serialization='ordinally sorted normalized repository paths, one UTF-8 path plus LF per entry'; path_case='ordinal-case-sensitive' }
        status_classes = [pscustomobject]@{ current = @('current-ready'); rejected = @('rejected-historical'); pending = @('current-maker-in-progress') }
        pending_namespaces = @([pscustomobject][ordered]@{ slice='A'; plan_owner='A'; status_class='current-maker-in-progress'; branch='work/a'; base_anchor=('1'*40); exact_paths=@('src/a_test.go'); exact_prefixes=@('.agent/specs/a/evidence/**'); forbidden_final_paths=@('src/a.go'); release_accepted=$false })
        excluded_resolvable_rows = @()
        candidates = @([pscustomobject][ordered]@{
            slice='A'; status_class='current-ready'; branch='work/a'; base=('2'*40); head=('3'*40); path_count=0; paths_sha256=''; plan_owner='A'; path_authority_eligible=$true; release_accepted=$false
            paths=@(
                [pscustomobject]@{path='.agent/reports/a.md';git_status='A';classification='report'},
                [pscustomobject]@{path='.agent/specs/a/evidence/proof.json';git_status='A';classification='evidence'},
                [pscustomobject]@{path='src/a.go';git_status='M';classification='product'},
                [pscustomobject]@{path='src/a_test.go';git_status='M';classification='product'}
            )
        })
    }
    Set-TestCandidateDigest $contract.candidates[0]
    $hash = 'f' * 64
    $positive = Invoke-ContractAudit $contract $plan $hash $hash $hash $hash
    Assert-SelfTest ($positive.verdict -eq 'PASS') ("valid frozen contract failed: " + ($positive.errors -join '; '))
    $missing = Copy-JsonObject $contract; $missing.candidates[0].paths = @($missing.candidates[0].paths | Select-Object -Skip 1)
    Assert-SelfTest ((Invoke-ContractAudit $missing $plan $hash $hash $hash $hash).verdict -eq 'FAIL') 'missing frozen path was accepted'
    $extra = Copy-JsonObject $contract; $extra.candidates[0].paths = @($extra.candidates[0].paths) + [pscustomobject]@{path='src/extra.go';git_status='A';classification='product'}
    Assert-SelfTest ((Invoke-ContractAudit $extra $plan $hash $hash $hash $hash).verdict -eq 'FAIL') 'extra frozen path was accepted'
    $wrongOwner = Copy-JsonObject $contract; $wrongOwner.candidates[0].plan_owner = 'NO-PATHS'
    $zeroResult = Invoke-ContractAudit $wrongOwner $plan $hash $hash $hash $hash
    Assert-SelfTest ($zeroResult.verdict -eq 'FAIL' -and @($zeroResult.errors | Where-Object { $_ -match '0 maker rows' }).Count -gt 0) 'zero-declaration non-empty candidate did not fail clearly'
    $wrongTest = Copy-JsonObject $contract; $wrongTest.candidates[0].paths[3].path='src/wrong_test.go'; Set-TestCandidateDigest $wrongTest.candidates[0]
    Assert-SelfTest ((Invoke-ContractAudit $wrongTest $plan $hash $hash $hash $hash).verdict -eq 'FAIL') 'wrong test name was accepted'
    $staleNamespace = Copy-JsonObject $contract; $staleNamespace.candidates[0].paths[1].path='.agent/specs/a-r7/evidence/proof.json'; Set-TestCandidateDigest $staleNamespace.candidates[0]
    Assert-SelfTest ((Invoke-ContractAudit $staleNamespace $plan $hash $hash $hash $hash).verdict -eq 'FAIL') 'stale evidence namespace was accepted'
    $wrongBranch = Copy-JsonObject $contract; $wrongBranch.candidates[0].branch='work/b'
    Assert-SelfTest ((Invoke-ContractAudit $wrongBranch $plan $hash $hash $hash $hash).verdict -eq 'FAIL') 'current candidate wrong branch was accepted'
    $hashDrift = Invoke-ContractAudit $contract $plan ('0'*64) $hash $hash $hash
    Assert-SelfTest ($hashDrift.verdict -eq 'FAIL') 'contract hash drift was accepted'
    $r12WrongIdentityTypes = [pscustomobject][ordered]@{
        schema_version='1'; kind='production-ready-active-diff-contracts'; revision='12'
        status_classes=[pscustomobject]@{current=@();rejected=@()};candidates=@();pending_namespaces=@();source_audit=[pscustomobject]@{observed_sha256=('a'*64)}
    }
    $r12WrongIdentityResult = Invoke-R12ContractAudit $r12WrongIdentityTypes $hash $hash $hash $hash
    Assert-SelfTest ($r12WrongIdentityResult.verdict -eq 'FAIL' -and @($r12WrongIdentityResult.errors | Where-Object { $_ -match 'contract identity' }).Count -eq 1) 'R12 numeric-string identity fields were accepted'
    $r12ForbiddenPrefix = [pscustomobject][ordered]@{
        schema_version=[long]1; kind='production-ready-active-diff-contracts'; revision=[long]12
        status_classes=[pscustomobject]@{current=@();rejected=@()}; candidates=@(); source_audit=[pscustomobject]@{observed_sha256=('a'*64)}
        pending_namespaces=@([pscustomobject][ordered]@{
            slice='SELFTEST';status_class='current-maker-in-progress';branch='work/selftest';base_anchor=('1'*40);release_accepted=$false
            exact_prefixes=@('src/generated/**');forbidden_final_paths=@('src/generated/secret.go')
        })
    }
    $r12ForbiddenPrefixResult = Invoke-R12ContractAudit $r12ForbiddenPrefix $hash $hash $hash $hash
    Assert-SelfTest ($r12ForbiddenPrefixResult.verdict -eq 'FAIL' -and @($r12ForbiddenPrefixResult.errors | Where-Object { $_ -match 'both allows and forbids' }).Count -eq 1) 'R12 prefix-covered forbidden_final_paths entry was accepted'
    $authority = Get-PlanAuthority $plan
    $pending = $contract.pending_namespaces[0]
    [object[]]$pendingDeclarations = @($authority.declarations | Where-Object owner -CEQ 'A')
    $pendingPass = Invoke-PendingSurfaceAudit $pending $pendingDeclarations @([pscustomobject]@{path='src/a_test.go';git_status='A'},[pscustomobject]@{path='.agent/specs/a/evidence/r4.json';git_status='A'})
    Assert-SelfTest ($pendingPass.verdict -eq 'PASS_PENDING_SURFACE_ONLY') ("valid pending surface failed: " + ($pendingPass.errors -join '; '))
    $pendingExtra = Invoke-PendingSurfaceAudit $pending $pendingDeclarations @([pscustomobject]@{path='.agent/testing/a/signal.md';git_status='A'})
    Assert-SelfTest ($pendingExtra.verdict -eq 'FAIL') 'undeclared pending .agent/testing path was accepted'
    $pendingForbidden = Invoke-PendingSurfaceAudit $pending $pendingDeclarations @([pscustomobject]@{path='src/a.go';git_status='M'})
    Assert-SelfTest ($pendingForbidden.verdict -eq 'FAIL') 'explicitly forbidden pending path was accepted'
    $prefixOnly = Copy-JsonObject $contract
    $prefixOnly.pending_namespaces[0].PSObject.Properties.Remove('exact_paths')
    $prefixOnly.pending_namespaces[0].exact_prefixes = @('.agent/reports/a-r6/**')
    $prefixOnlyAudit = Invoke-ContractAudit $prefixOnly $plan $hash $hash $hash $hash
    Assert-SelfTest ($prefixOnlyAudit.verdict -eq 'PASS') ("prefix-only pending contract failed: " + ($prefixOnlyAudit.errors -join '; '))
    Assert-SelfTest (@($prefixOnlyAudit.pending_contracts[0].allowed_exact_paths).Count -eq 0) 'prefix-only pending contract serialized a null exact-path declaration'
    $prefixOnlyPending = $prefixOnly.pending_namespaces[0]
    $prefixOnlyProbe = Invoke-PendingSurfaceAudit $prefixOnlyPending $pendingDeclarations @(
        [pscustomobject]@{path='.agent/reports/a-r4/prior.json';git_status='A'},
        [pscustomobject]@{path='.agent/reports/a-r6/current.json';git_status='A'}
    )
    Assert-SelfTest ($prefixOnlyProbe.verdict -eq 'PASS_PENDING_SURFACE_ONLY') ("prefix-only pending full plan-owned evidence surface failed: " + ($prefixOnlyProbe.errors -join '; '))
    $wrongBaseProbe = Invoke-ContractAudit $contract $plan $hash $hash $hash $hash -Repository (Get-Location).Path -Probe ([pscustomobject]@{slice='A';base=('9'*40);head=('8'*40)})
    Assert-SelfTest (@($wrongBaseProbe.errors | Where-Object { $_ -match 'pending probe base .+ must equal frozen base anchor' }).Count -eq 1) 'pending probe accepted or obscured a non-anchor base'
    $wrongRejectedHead = Copy-JsonObject $contract; $wrongRejectedHead.authority.rejected_r8_head = ('4' * 40)
    Assert-SelfTest (@((Invoke-R9ProfileAudit $wrongRejectedHead $authority) | Where-Object { $_ -match 'authority rejected_r8_head drifted' }).Count -eq 1) 'R9 profile accepted a wrong rejected R8 head'
    $wrongScopeProvenance = Copy-JsonObject $contract; $wrongScopeProvenance.authority.r8_scope_provenance_sha256 = ('b' * 64)
    Assert-SelfTest (@((Invoke-R9ProfileAudit $wrongScopeProvenance $authority) | Where-Object { $_ -match 'authority r8_scope_provenance_sha256 drifted' }).Count -eq 1) 'R9 profile accepted wrong AB5F scope provenance'
    $mutableSource = Copy-JsonObject $contract; $mutableSource.source_audit.use = 'acceptance authority'
    Assert-SelfTest (@((Invoke-R9ProfileAudit $mutableSource $authority) | Where-Object { $_ -match 'source_audit use must remain discovery-only' }).Count -eq 1) 'R9 profile accepted the mutable register as authority'
    $wrongSerialization = Copy-JsonObject $contract; $wrongSerialization.digest_contract.serialization = 'platform-default path list'
    Assert-SelfTest (@((Invoke-R9ProfileAudit $wrongSerialization $authority) | Where-Object { $_ -match 'digest serialization drifted' }).Count -eq 1) 'R9 profile accepted wrong digest serialization'
    $wrongPathCase = Copy-JsonObject $contract; $wrongPathCase.digest_contract.path_case = 'case-insensitive'
    Assert-SelfTest (@((Invoke-R9ProfileAudit $wrongPathCase $authority) | Where-Object { $_ -match 'digest path_case drifted' }).Count -eq 1) 'R9 profile accepted wrong digest path-case semantics'
    Write-Output 'SELFTEST PASS: active-candidate path authority (missing/extra/wrong-owner/wrong-test/stale-namespace/zero-declarations/pending-surface mutations rejected)'
}

if ($SelfTest) { Invoke-SelfTest; exit 0 }
if ($PrintCanonicalContractSha256) {
    if (-not (Test-Path -LiteralPath $Contract -PathType Leaf)) { throw "contract does not exist: $Contract" }
    Write-Output (Get-CanonicalUtf8LfSha256 $Contract)
    exit 0
}

$startedAt = [DateTimeOffset]::UtcNow
$artifactObject = $null
$exitCode = 1
try {
    if (-not (Test-Path -LiteralPath $Contract -PathType Leaf)) { throw "contract does not exist: $Contract" }
    if (-not (Test-Path -LiteralPath $Plan -PathType Leaf)) { throw "plan does not exist: $Plan" }
    if ($ExpectedContractSha256 -notmatch '^[0-9a-fA-F]{64}$') { throw '-ExpectedContractSha256 must be a full SHA256' }
    if ($ExpectedPlanSha256 -notmatch '^[0-9a-fA-F]{64}$') { throw '-ExpectedPlanSha256 must be a full SHA256' }
    $probeSupplied = -not [string]::IsNullOrWhiteSpace($ProbeSlice) -or -not [string]::IsNullOrWhiteSpace($ProbeBase) -or -not [string]::IsNullOrWhiteSpace($ProbeHead)
    if ($probeSupplied -and ([string]::IsNullOrWhiteSpace($ProbeSlice) -or $ProbeBase -notmatch '^[0-9a-fA-F]{40}$' -or $ProbeHead -notmatch '^[0-9a-fA-F]{40}$')) { throw 'pending probe requires -ProbeSlice and full 40-hex -ProbeBase/-ProbeHead' }
    $repository = $null
    if ($VerifyAvailableGit -or $RequireGitObjects -or $probeSupplied) {
        $root = @(& git rev-parse --show-toplevel 2>&1)
        if ($LASTEXITCODE -ne 0) { throw "cannot resolve Git repository: $($root -join ' ')" }
        $repository = [System.IO.Path]::GetFullPath(([string]$root[-1]).Trim())
    }
    $contractHash = Get-CanonicalUtf8LfSha256 $Contract
    $planHash = Get-CanonicalUtf8LfSha256 $Plan
    $contractObject = Get-Content -LiteralPath $Contract -Raw | ConvertFrom-Json -Depth 100
    $planText = [System.IO.File]::ReadAllText([System.IO.Path]::GetFullPath($Plan))
    $probe = if ($probeSupplied) { [pscustomobject]@{ slice=$ProbeSlice; base=$ProbeBase.ToLowerInvariant(); head=$ProbeHead.ToLowerInvariant() } } else { $null }
    $audit = Invoke-ContractAudit -ContractObject $contractObject -PlanText $planText -ObservedContractSha256 $contractHash -ExpectedContractSha256Value $ExpectedContractSha256 -ObservedPlanSha256 $planHash -ExpectedPlanSha256Value $ExpectedPlanSha256 -Repository $repository -VerifyGit:($VerifyAvailableGit -or $RequireGitObjects) -RequireObjects:$RequireGitObjects -Probe $probe -EnforceR9Profile
    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{
        schema_version = 1
        gate = 'active-candidate-path-authority'
        verdict = $audit.verdict
        started_at = $startedAt.ToString('O')
        finished_at = $finishedAt.ToString('O')
        duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
        contract = [ordered]@{ path=[System.IO.Path]::GetFullPath($Contract); expected_sha256=$ExpectedContractSha256.ToLowerInvariant(); observed_sha256=$contractHash; hash_match=$contractHash -ceq $ExpectedContractSha256.ToLowerInvariant() }
        plan = [ordered]@{ path=[System.IO.Path]::GetFullPath($Plan); expected_sha256=$ExpectedPlanSha256.ToLowerInvariant(); observed_sha256=$planHash; hash_match=$planHash -ceq $ExpectedPlanSha256.ToLowerInvariant() }
        source_snapshot = $audit.source_snapshot
        counts = $audit.counts
        candidates = $audit.candidates
        pending_contracts = $audit.pending_contracts
        git_verification = $audit.git_verification
        pending_probe = $audit.pending_probe
        errors = $audit.errors
    }
    $exitCode = if ($audit.verdict -eq 'PASS') { 0 } else { 1 }
}
catch {
    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{ schema_version=1; gate='active-candidate-path-authority'; verdict='FAIL'; started_at=$startedAt.ToString('O'); finished_at=$finishedAt.ToString('O'); source_snapshot=[ordered]@{freshness='HISTORICAL_DISCOVERY_ONLY';used_for_acceptance=$false;mutable_register_read=$false}; errors=@($_.Exception.Message) }
    $exitCode = 1
}

Write-Utf8NoBom -Path $Artifact -Text (($artifactObject | ConvertTo-Json -Depth 100) + "`n")
Write-Output "active-candidate-path-authority verdict=$($artifactObject.verdict) artifact=$Artifact"
exit $exitCode
