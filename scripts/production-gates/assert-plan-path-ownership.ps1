[CmdletBinding()]
param(
    [ValidateSet('Ledger', 'Diff')]
    [string]$Mode = 'Ledger',
    [string]$Slice,
    [string]$Base,
    [string]$Head,
    [string]$Plan = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md',
    [string]$ExpectedPlanSha256,
    [string]$State = '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json',
    [string]$ScopeMap = '.agent/plans/2026-07-10-engram-production-ready-scope-map.json',
    [string]$ExpectedScopeMapSha256,
    [string]$Register,
    [string]$EvidenceNamespace,
    [string]$ReportNamespace,
    [string]$Artifact = '.agent/reports/evidence/production-ready/ownership/path-ledger.json',
    [switch]$PrintCanonicalPlanSha256,
    [switch]$SelfTest,
    [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Show-Help {
    @'
assert-plan-path-ownership.ps1

Ledger mode parses the production-ready master-plan ownership matrix. Only
literal repository paths and explicit directory/** prefixes are accepted.
Cross-owner exact and exact/prefix overlap requires one ownership epoch whose
ordered owner sequence exactly matches the effective owners. An explicitly
tracked single-owner exact path may use an em dash in the Next epoch column.
Prefix/prefix overlap always fails. The tracked ownership-state JSON must match
the challenged canonical UTF-8/LF plan hash and every ordered epoch.

Diff mode additionally enumerates git diff --name-status Base..Head and proves
that every changed path belongs to the named slice or to its validated evidence
or maker-report namespace. For repeated paths the slice must be the state-file
current owner; ordinary successor bases must descend from the exact independently
checked, post-reviewed, integrated predecessor SHA. Base and Head must be full
commit object IDs.

Usage:
  pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 -Mode Ledger `
    -Plan .agent/plans/2026-07-10-engram-production-ready-master-plan.md `
    -ExpectedPlanSha256 <64-hex-sha256> `
    -State .agent/plans/2026-07-10-engram-production-ready-ownership-state.json `
    -ScopeMap .agent/plans/2026-07-10-engram-production-ready-scope-map.json `
    -ExpectedScopeMapSha256 <64-hex-sha256> `
    -Register <optional-live-evidence-register.json> `
    -Artifact .agent/reports/evidence/production-ready/ownership/path-ledger.json

  pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 -Mode Diff `
    -Slice DB-BULKOPS -Base <40-hex-commit> -Head <40-hex-commit> `
    -EvidenceNamespace '.agent/specs/production-ready-db-bulkops/evidence/**' `
    -ReportNamespace .agent/reports/db-bulkops-maker.md -Plan <plan> `
    -ExpectedPlanSha256 <64-hex-sha256> -State <ownership-state.json> `
    -ScopeMap <scope-map.json> -ExpectedScopeMapSha256 <64-hex-sha256> `
    -Register <optional-live-evidence-register.json> -Artifact <json>

  pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 `
    -Plan <plan> -PrintCanonicalPlanSha256
'@ | Write-Output
}

function Write-Utf8NoBom {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][AllowEmptyString()][string]$Content
    )

    $parent = Split-Path -Parent $Path
    if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    [System.IO.File]::WriteAllText(
        [System.IO.Path]::GetFullPath($Path),
        $Content,
        [System.Text.UTF8Encoding]::new($false)
    )
}

function Get-CanonicalUtf8LfFileSha256 {
    param([Parameter(Mandatory)][string]$Path)

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $text = [System.IO.File]::ReadAllText($fullPath)
    $canonicalText = ($text -replace "`r`n", "`n") -replace "`r", "`n"
    $canonicalBytes = [System.Text.UTF8Encoding]::new($false).GetBytes($canonicalText)
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($canonicalBytes)).ToLowerInvariant()
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

function Normalize-OwnershipPath {
    param([Parameter(Mandatory)][string]$Token)

    $value = $Token.Trim().Replace('\', '/')
    if ([string]::IsNullOrWhiteSpace($value)) { throw 'empty ownership path token' }
    $isPrefix = $value.EndsWith('/**', [System.StringComparison]::Ordinal)
    $basePath = if ($isPrefix) { $value.Substring(0, $value.Length - 3) } else { $value }
    if ($basePath.StartsWith('/') -or $basePath -match '^[A-Za-z]:' -or $basePath.Contains('//')) {
        throw "ownership path must be repository-relative: '$value'"
    }
    if ($basePath -match '(^|/)\.\.?(?:/|$)') { throw "ownership path contains dot traversal: '$value'" }
    if ($basePath -notmatch '^[A-Za-z0-9._/-]+$') { throw "unknown descriptive or wildcard ownership scope '$value'" }
    if ($value -match '[*?\[\]{}]' -and -not $isPrefix) {
        throw "only a terminal /** prefix wildcard is allowed: '$value'"
    }
    if ($isPrefix -and ([string]::IsNullOrWhiteSpace($basePath) -or $basePath -match '[*?\[\]{}]')) {
        throw "invalid declared ownership prefix '$value'"
    }

    $normalizedBase = $basePath.TrimEnd('/')
    return [pscustomobject][ordered]@{
        path = $normalizedBase
        display = if ($isPrefix) { $normalizedBase + '/**' } else { $normalizedBase }
        kind = if ($isPrefix) { 'prefix' } else { 'exact' }
    }
}

function Normalize-GitPath {
    param([Parameter(Mandatory)][string]$Token)

    if ($Token -cne $Token.Trim()) { throw "git diff path has leading or trailing whitespace and cannot be normalized safely: '$Token'" }
    $value = $Token.Replace('\', '/')
    if ([string]::IsNullOrWhiteSpace($value)) { throw 'git diff emitted an empty path' }
    if ($value.StartsWith('/') -or $value -match '^[A-Za-z]:' -or $value.Contains("`0")) {
        throw "git diff path is not repository-relative: '$value'"
    }
    if ($value -match '(^|/)\.\.?(?:/|$)') { throw "git diff path contains dot traversal: '$value'" }
    while ($value.StartsWith('./', [System.StringComparison]::Ordinal)) {
        $value = $value.Substring(2)
    }
    return $value
}

function Get-MarkdownSection {
    param(
        [Parameter(Mandatory)][string]$Text,
        [Parameter(Mandatory)][string]$StartPattern,
        [Parameter(Mandatory)][string]$EndPattern
    )

    $start = [regex]::Match($Text, $StartPattern, [System.Text.RegularExpressions.RegexOptions]::Multiline)
    if (-not $start.Success) { throw "plan section '$StartPattern' is missing" }
    $tail = $Text.Substring($start.Index + $start.Length)
    $end = [regex]::Match($tail, $EndPattern, [System.Text.RegularExpressions.RegexOptions]::Multiline)
    if (-not $end.Success) { return $tail }
    return $tail.Substring(0, $end.Index)
}

function Get-TableRows {
    param(
        [Parameter(Mandatory)][string]$Section,
        [Parameter(Mandatory)][string]$HeaderFirstCell
    )

    $lines = $Section -split "`r?`n"
    $headerIndex = -1
    for ($index = 0; $index -lt $lines.Count; $index++) {
        if (-not $lines[$index].TrimStart().StartsWith('|')) { continue }
        $cells = Split-MarkdownRow $lines[$index]
        if ($cells.Count -gt 0 -and $cells[0].Trim() -ceq $HeaderFirstCell) {
            $headerIndex = $index
            break
        }
    }
    if ($headerIndex -lt 0) { throw "Markdown table '$HeaderFirstCell' is missing" }

    $rows = [System.Collections.Generic.List[object]]::new()
    for ($index = $headerIndex + 1; $index -lt $lines.Count; $index++) {
        $line = $lines[$index]
        if ([string]::IsNullOrWhiteSpace($line)) {
            if ($rows.Count -gt 0) { break }
            continue
        }
        if (-not $line.TrimStart().StartsWith('|')) {
            if ($rows.Count -gt 0) { break }
            continue
        }
        $cells = Split-MarkdownRow $line
        if ($cells.Count -gt 0 -and $cells[0] -match '^:?-{3,}:?$') { continue }
        $rows.Add([pscustomobject][ordered]@{
            line_number = $index + 1
            cells = $cells
            raw = $line
        })
    }
    return @($rows)
}

function Test-PathInsidePrefix {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Prefix
    )
    return $Path.StartsWith($Prefix.TrimEnd('/') + '/', [System.StringComparison]::Ordinal)
}

function Test-DeclarationMatchesPath {
    param(
        [Parameter(Mandatory)]$Declaration,
        [Parameter(Mandatory)][string]$Path
    )

    if ($Declaration.kind -eq 'exact') {
        return [string]::Equals($Declaration.path, $Path, [System.StringComparison]::Ordinal)
    }
    return Test-PathInsidePrefix $Path $Declaration.path
}

function Test-SameStringSet {
    param(
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Left,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Right
    )

    $leftSet = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    $rightSet = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($item in $Left) { [void]$leftSet.Add([string]$item) }
    foreach ($item in $Right) { [void]$rightSet.Add([string]$item) }
    return $leftSet.SetEquals($rightSet)
}

function Test-SameStringSequence {
    param(
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Left,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Right
    )

    if ($Left.Count -ne $Right.Count) { return $false }
    for ($index = 0; $index -lt $Left.Count; $index++) {
        if (-not [string]::Equals([string]$Left[$index], [string]$Right[$index], [System.StringComparison]::Ordinal)) { return $false }
    }
    return $true
}

function Test-ExpectedPlanHash {
    param(
        [Parameter(Mandatory)][string]$ObservedSha256,
        [Parameter(Mandatory)][string]$ExpectedSha256
    )

    if ($ObservedSha256 -notmatch '^[0-9A-Fa-f]{64}$' -or $ExpectedSha256 -notmatch '^[0-9A-Fa-f]{64}$') { return $false }
    return [string]::Equals($ObservedSha256, $ExpectedSha256, [System.StringComparison]::OrdinalIgnoreCase)
}

function Get-UniqueOwnersForExactPath {
    param(
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Declarations,
        [Parameter(Mandatory)][string]$Path
    )

    $owners = [System.Collections.Generic.List[string]]::new()
    foreach ($declaration in $Declarations) {
        $matches = if ($declaration.kind -eq 'exact') {
            [string]::Equals($declaration.path, $Path, [System.StringComparison]::Ordinal)
        }
        else {
            Test-PathInsidePrefix $Path $declaration.path
        }
        if ($matches -and -not $owners.Contains($declaration.owner)) { $owners.Add($declaration.owner) }
    }
    return @($owners)
}

function Get-ScopeRemainder {
    param([Parameter(Mandatory)][string]$ScopeCell)

    $remainder = [regex]::Replace($ScopeCell, '`[^`\r\n]+`', ' ')
    $allowedPhrases = @(
        'no versioned release-note path is authorized yet',
        'legacy evidence prefix',
        'legacy exact report',
        'generated',
        'new',
        'only'
    )
    foreach ($phrase in $allowedPhrases) {
        $remainder = [regex]::Replace($remainder, '(?i)(?<![A-Za-z0-9_-])' + [regex]::Escape($phrase) + '(?![A-Za-z0-9_-])', ' ')
    }
    $remainder = ($remainder -replace '[,;]', ' ' -replace '\s+', ' ').Trim()
    return $remainder
}

function Invoke-OwnershipAudit {
    param(
        [Parameter(Mandatory)][string]$Text,
        [string]$Source = '<memory>'
    )

    $errors = [System.Collections.Generic.List[string]]::new()
    $declarations = [System.Collections.Generic.List[object]]::new()
    $slices = [System.Collections.Generic.List[object]]::new()
    $epochs = [System.Collections.Generic.List[object]]::new()
    $repeatedExactPaths = [System.Collections.Generic.List[object]]::new()
    $prefixIntersections = [System.Collections.Generic.List[object]]::new()

    try {
        $matrixSection = Get-MarkdownSection $Text '^## 4\. Worktree and Ownership Matrix\s*$' '^### 4\.1\s+'
        $matrixRows = @(Get-TableRows $matrixSection 'Slice')
        foreach ($row in $matrixRows) {
            if ($row.cells.Count -lt 5) {
                $errors.Add("line $($row.line_number): ownership row has $($row.cells.Count) cells, expected at least 5")
                continue
            }

            $sliceName = $row.cells[0].Trim().Trim('`')
            $branchSpans = @(Get-CodeSpans $row.cells[1])
            $branch = if ($branchSpans.Count -gt 0) { $branchSpans[0] } else { $row.cells[1].Trim() }
            if ($branch -notmatch '^work/[A-Za-z0-9._/-]+$') { continue }
            if ([string]::IsNullOrWhiteSpace($sliceName)) {
                $errors.Add("line $($row.line_number): maker slice name is empty")
                continue
            }

            $pathTokens = @(Get-CodeSpans $row.cells[2])
            if ($pathTokens.Count -eq 0) {
                $errors.Add("line $($row.line_number): maker '$sliceName' has no exact/prefix code-spanned paths")
                continue
            }
            $scopeRemainder = Get-ScopeRemainder $row.cells[2]
            if (-not [string]::IsNullOrWhiteSpace($scopeRemainder)) {
                $errors.Add("line $($row.line_number): maker '$sliceName' has unknown descriptive ownership scope '$scopeRemainder'")
            }

            $slicePaths = [System.Collections.Generic.List[string]]::new()
            foreach ($token in $pathTokens) {
                try {
                    $normalized = Normalize-OwnershipPath $token
                    $duplicateWithinOwner = @($declarations | Where-Object {
                        $_.owner -ceq $sliceName -and
                        $_.kind -ceq $normalized.kind -and
                        [string]::Equals($_.path, $normalized.path, [System.StringComparison]::Ordinal)
                    }).Count -gt 0
                    if ($duplicateWithinOwner) {
                        $errors.Add("line $($row.line_number): maker '$sliceName' declares '$($normalized.display)' more than once")
                        continue
                    }
                    $declaration = [pscustomobject][ordered]@{
                        owner = $sliceName
                        branch = $branch
                        path = $normalized.path
                        display = $normalized.display
                        kind = $normalized.kind
                        line = $row.line_number
                    }
                    $declarations.Add($declaration)
                    $slicePaths.Add($normalized.display)
                }
                catch {
                    $errors.Add("line $($row.line_number): $($_.Exception.Message)")
                }
            }
            $slices.Add([pscustomobject][ordered]@{
                slice = $sliceName
                branch = $branch
                paths = @($slicePaths)
                line = $row.line_number
            })
        }

        foreach ($sliceGroup in @($slices | Group-Object slice | Where-Object Count -gt 1)) {
            $errors.Add("maker slice '$($sliceGroup.Name)' appears $($sliceGroup.Count) times")
        }

        $epochSection = Get-MarkdownSection $Text '^### 4\.1 Ownership Epochs and Automated Overlap Gate\s*$' '^### 4\.2\s+'
        $epochRows = @(Get-TableRows $epochSection 'Exact path')
        foreach ($row in $epochRows) {
            if ($row.cells.Count -lt 4) {
                $errors.Add("line $($row.line_number): epoch row has $($row.cells.Count) cells, expected 4")
                continue
            }
            $paths = @(Get-CodeSpans $row.cells[0])
            if ($paths.Count -eq 0) {
                $errors.Add("line $($row.line_number): epoch row has no exact path")
                continue
            }

            $current = ($row.cells[1].Trim() -replace '`', '')
            $next = ($row.cells[2].Trim() -replace '`', '')
            $chain = [System.Collections.Generic.List[string]]::new()
            if (-not [string]::IsNullOrWhiteSpace($current)) { $chain.Add($current) }
            $hasSuccessor = $next -notmatch '^(?:—|–|-|none|n/?a)$'
            if ($hasSuccessor) {
                foreach ($owner in ($next -split '\s*->\s*')) {
                    $trimmedOwner = $owner.Trim()
                    if (-not [string]::IsNullOrWhiteSpace($trimmedOwner)) { $chain.Add($trimmedOwner) }
                }
            }
            if ($chain.Count -lt 1) {
                $errors.Add("line $($row.line_number): epoch chain must contain a current owner")
            }
            if (@($chain | Select-Object -Unique).Count -ne $chain.Count) {
                $errors.Add("line $($row.line_number): epoch chain contains a duplicate owner")
            }

            $gate = $row.cells[3].Trim()
            $hasTransferSignal = $gate -match '(?i)\b(?:PASS|integrat(?:e|ed|ion)?|rebas(?:e|ed)|commit|SHA|stack|publish(?:ed)?|review|checker|regression|artifact|scan|proof|successor|worktree)\b'
            if ([string]::IsNullOrWhiteSpace($gate) -or $gate.Length -lt 20 -or $gate -match '(?i)^\s*(?:TBD|TODO|UNKNOWN|LATER)\s*$' -or -not $hasTransferSignal) {
                $errors.Add("line $($row.line_number): epoch transfer gate is blank, placeholder, or not concrete")
            }

            foreach ($pathToken in $paths) {
                try {
                    $normalized = Normalize-OwnershipPath $pathToken
                    if ($normalized.kind -ne 'exact') {
                        throw "epoch path must be exact, got '$($normalized.display)'"
                    }
                    if (@($epochs | Where-Object {
                        [string]::Equals($_.path, $normalized.path, [System.StringComparison]::Ordinal)
                    }).Count -gt 0) {
                        throw "duplicate epoch declaration for '$($normalized.path)'"
                    }
                    $epochs.Add([pscustomobject][ordered]@{
                        path = $normalized.path
                        owners = @($chain)
                        transfer_gate = $gate
                        line = $row.line_number
                    })
                }
                catch {
                    $errors.Add("line $($row.line_number): $($_.Exception.Message)")
                }
            }
        }

        $knownOwners = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
        foreach ($sliceEntry in $slices) { [void]$knownOwners.Add($sliceEntry.slice) }
        foreach ($epoch in $epochs) {
            foreach ($owner in $epoch.owners) {
                if (-not $knownOwners.Contains($owner)) {
                    $errors.Add("epoch '$($epoch.path)' references unknown maker '$owner'")
                }
            }
        }

        for ($leftIndex = 0; $leftIndex -lt $declarations.Count; $leftIndex++) {
            $left = $declarations[$leftIndex]
            for ($rightIndex = $leftIndex + 1; $rightIndex -lt $declarations.Count; $rightIndex++) {
                $right = $declarations[$rightIndex]
                if ($left.owner -ceq $right.owner) { continue }

                if ($left.kind -eq 'prefix' -and $right.kind -eq 'prefix') {
                    $intersects =
                        [string]::Equals($left.path, $right.path, [System.StringComparison]::Ordinal) -or
                        (Test-PathInsidePrefix $left.path $right.path) -or
                        (Test-PathInsidePrefix $right.path $left.path)
                    if ($intersects) {
                        $entry = [pscustomobject][ordered]@{
                            left_owner = $left.owner
                            left = $left.display
                            right_owner = $right.owner
                            right = $right.display
                            exact_path = $null
                            declared_epoch = $false
                        }
                        $prefixIntersections.Add($entry)
                        $errors.Add("undeclared prefix/prefix intersection: $($left.owner) '$($left.display)' vs $($right.owner) '$($right.display)'")
                    }
                    continue
                }

                if ($left.kind -eq 'exact' -and $right.kind -eq 'exact') { continue }
                $exact = if ($left.kind -eq 'exact') { $left } else { $right }
                $prefix = if ($left.kind -eq 'prefix') { $left } else { $right }
                if (Test-PathInsidePrefix $exact.path $prefix.path) {
                    $declaredEpoch = @($epochs | Where-Object {
                        [string]::Equals($_.path, $exact.path, [System.StringComparison]::Ordinal)
                    }).Count -eq 1
                    $prefixIntersections.Add([pscustomobject][ordered]@{
                        left_owner = $left.owner
                        left = $left.display
                        right_owner = $right.owner
                        right = $right.display
                        exact_path = $exact.path
                        declared_epoch = $declaredEpoch
                    })
                }
            }
        }

        $exactPaths = @($declarations | Where-Object kind -eq 'exact' | ForEach-Object path | Sort-Object -Unique)
        foreach ($path in $exactPaths) {
            $exactOwners = @($declarations | Where-Object {
                $_.kind -eq 'exact' -and [string]::Equals($_.path, $path, [System.StringComparison]::Ordinal)
            } | ForEach-Object owner | Select-Object -Unique)
            $prefixOwners = @($declarations | Where-Object {
                $_.kind -eq 'prefix' -and (Test-PathInsidePrefix $path $_.path)
            } | ForEach-Object owner | Select-Object -Unique)
            $effectiveOwners = @(Get-UniqueOwnersForExactPath @($declarations) $path)
            $matchingEpoch = @($epochs | Where-Object {
                [string]::Equals($_.path, $path, [System.StringComparison]::Ordinal)
            })

            if ($effectiveOwners.Count -lt 2) {
                if ($matchingEpoch.Count -gt 0) {
                    if ($effectiveOwners.Count -eq 1 -and $matchingEpoch.Count -eq 1) {
                        $epochOwners = @($matchingEpoch[0].owners)
                        if (-not (Test-SameStringSequence $effectiveOwners $epochOwners)) {
                            $errors.Add("single-owner epoch '$path' differs: effective=$($effectiveOwners -join ' -> '), epoch=$($epochOwners -join ' -> ')")
                        }
                    }
                    else {
                        $errors.Add("declared epoch '$path' is not an actual exact ownership declaration")
                    }
                }
                continue
            }

            $entry = [pscustomobject][ordered]@{
                path = $path
                exact_owners = $exactOwners
                prefix_owners = $prefixOwners
                effective_owners = $effectiveOwners
                declared_epoch = $matchingEpoch.Count -eq 1
                epoch_owners = if ($matchingEpoch.Count -eq 1) { @($matchingEpoch[0].owners) } else { @() }
            }
            $repeatedExactPaths.Add($entry)

            if ($matchingEpoch.Count -ne 1) {
                $errors.Add("undeclared ownership overlap '$path' across $($effectiveOwners -join ', ')")
                continue
            }
            $epochOwners = @($matchingEpoch[0].owners)
            if (-not (Test-SameStringSequence $effectiveOwners $epochOwners)) {
                $errors.Add("epoch '$path' owner order differs: effective=$($effectiveOwners -join ' -> '), epoch=$($epochOwners -join ' -> ')")
            }
        }

        foreach ($epoch in $epochs) {
            if (-not (@($exactPaths | Where-Object {
                [string]::Equals($_, $epoch.path, [System.StringComparison]::Ordinal)
            }).Count -eq 1)) {
                $errors.Add("declared epoch '$($epoch.path)' has no exact ownership declaration")
            }
        }
    }
    catch {
        $errors.Add($_.Exception.Message)
    }

    $undeclaredPrefixIntersections = @($prefixIntersections | Where-Object { -not $_.declared_epoch })
    return [pscustomobject][ordered]@{
        schema_version = 2
        gate = 'plan-path-ownership'
        mode = 'Ledger'
        source = $Source
        verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        counts = [ordered]@{
            maker_slices = $slices.Count
            declarations = $declarations.Count
            exact_paths = @($declarations | Where-Object kind -eq 'exact').Count
            prefixes = @($declarations | Where-Object kind -eq 'prefix').Count
            repeated_exact_paths = $repeatedExactPaths.Count
            prefix_intersections = $prefixIntersections.Count
            undeclared_prefix_intersections = $undeclaredPrefixIntersections.Count
            declared_epochs = $epochs.Count
            errors = $errors.Count
        }
        slices = @($slices)
        declarations = @($declarations)
        repeated_exact_paths = @($repeatedExactPaths)
        prefix_intersections = @($prefixIntersections)
        epochs = @($epochs)
        errors = @($errors)
    }
}

function Resolve-DiffNamespace {
    param(
        [Parameter(Mandatory)][ValidateSet('evidence', 'report')][string]$Kind,
        [Parameter(Mandatory)][string]$Token,
        [Parameter(Mandatory)][string]$SliceName,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$SliceDeclarations
    )

    $normalized = Normalize-OwnershipPath $Token
    if (-not ($normalized.path -eq '.agent' -or (Test-PathInsidePrefix $normalized.path '.agent'))) {
        throw "$Kind namespace must remain under .agent/: '$($normalized.display)'"
    }
    if ($Kind -eq 'evidence' -and $normalized.kind -ne 'prefix') {
        throw "evidence namespace must be a terminal /** prefix: '$($normalized.display)'"
    }

    $sliceSlug = $SliceName.ToLowerInvariant()
    $canonical = if ($Kind -eq 'evidence') {
        ".agent/reports/evidence/production-ready/$sliceSlug/**"
    }
    else {
        ".agent/reports/production-ready/$sliceSlug/**"
    }
    $isCanonical = [string]::Equals($normalized.display, $canonical, [System.StringComparison]::Ordinal)
    $isLiteralException = @($SliceDeclarations | Where-Object {
        [string]::Equals($_.display, $normalized.display, [System.StringComparison]::Ordinal)
    }).Count -eq 1
    if (-not $isCanonical -and -not $isLiteralException) {
        throw "$Kind namespace '$($normalized.display)' is neither canonical '$canonical' nor a literal declaration in slice '$SliceName'"
    }

    return [pscustomobject][ordered]@{
        kind = $Kind
        path = $normalized.path
        display = $normalized.display
        match_kind = $normalized.kind
        policy = if ($isCanonical) { 'canonical-derived-default' } else { 'literal-row-exception' }
    }
}

function ConvertFrom-GitNameStatusLines {
    param([Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Lines)

    $entries = [System.Collections.Generic.List[object]]::new()
    $errors = [System.Collections.Generic.List[string]]::new()
    foreach ($lineObject in $Lines) {
        $line = [string]$lineObject
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = @($line -split "`t")
        if ($parts.Count -lt 2) {
            $errors.Add("malformed git diff --name-status line: '$line'")
            continue
        }
        $status = $parts[0].Trim()
        $kind = if ($status.Length -gt 0) { $status.Substring(0, 1).ToUpperInvariant() } else { '' }
        $expectedPaths = if ($kind -in @('R', 'C')) { 2 } else { 1 }
        if ($kind -notin @('A', 'M', 'D', 'T', 'R', 'C')) {
            $errors.Add("unsupported or unresolved git diff status '$status'")
        }
        if (($parts.Count - 1) -ne $expectedPaths) {
            $errors.Add("git diff status '$status' emitted $($parts.Count - 1) paths, expected $expectedPaths")
            continue
        }

        $paths = [System.Collections.Generic.List[string]]::new()
        try {
            for ($index = 1; $index -lt $parts.Count; $index++) {
                $paths.Add((Normalize-GitPath $parts[$index]))
            }
            $entries.Add([pscustomobject][ordered]@{
                status = $status
                paths = @($paths)
                raw = $line
            })
        }
        catch {
            $errors.Add($_.Exception.Message)
        }
    }
    return [pscustomobject][ordered]@{ entries = @($entries); errors = @($errors) }
}

function Invoke-DiffEntryAudit {
    param(
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Entries,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$SliceDeclarations,
        [Parameter(Mandatory)]$Evidence,
        [Parameter(Mandatory)]$Report
    )

    $changedPaths = [System.Collections.Generic.List[object]]::new()
    $violations = [System.Collections.Generic.List[object]]::new()
    foreach ($entry in $Entries) {
        foreach ($path in $entry.paths) {
            $ownershipMatches = @($SliceDeclarations | Where-Object { Test-DeclarationMatchesPath $_ $path })
            $evidenceMatch = if ($Evidence.match_kind -eq 'exact') {
                [string]::Equals($Evidence.path, $path, [System.StringComparison]::Ordinal)
            }
            else { Test-PathInsidePrefix $path $Evidence.path }
            $reportMatch = if ($Report.match_kind -eq 'exact') {
                [string]::Equals($Report.path, $path, [System.StringComparison]::Ordinal)
            }
            else { Test-PathInsidePrefix $path $Report.path }

            $allowedBy = [System.Collections.Generic.List[string]]::new()
            if ($ownershipMatches.Count -gt 0) { $allowedBy.Add('slice-declaration') }
            if ($evidenceMatch) { $allowedBy.Add('evidence-namespace') }
            if ($reportMatch) { $allowedBy.Add('report-namespace') }
            $pathEntry = [pscustomobject][ordered]@{
                status = $entry.status
                path = $path
                allowed = $allowedBy.Count -gt 0
                allowed_by = @($allowedBy)
                ownership_matches = @($ownershipMatches | ForEach-Object display)
            }
            $changedPaths.Add($pathEntry)
            if ($allowedBy.Count -eq 0) {
                $violations.Add([pscustomobject][ordered]@{
                    status = $entry.status
                    path = $path
                    reason = 'changed path is outside the named slice and validated evidence/report namespaces'
                })
            }
        }
    }
    return [pscustomobject][ordered]@{
        changed_paths = @($changedPaths)
        violations = @($violations)
    }
}

function Resolve-ExactCommit {
    param(
        [Parameter(Mandatory)][string]$Repository,
        [Parameter(Mandatory)][string]$Revision,
        [Parameter(Mandatory)][string]$Label
    )

    if ($Revision -notmatch '^[0-9A-Fa-f]{40}$') {
        throw "$Label must be a full 40-hex commit object ID"
    }
    $output = @(& git -C $Repository rev-parse --verify "$Revision^{commit}" 2>&1)
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) { throw "$Label commit '$Revision' is not resolvable: $($output -join ' ')" }
    $resolved = ([string]$output[-1]).Trim().ToLowerInvariant()
    if ($resolved -notmatch '^[0-9a-f]{40}$') { throw "$Label resolved to invalid commit identity '$resolved'" }
    return $resolved
}

function Get-PropertyValue {
    param(
        [AllowNull()]$Object,
        [Parameter(Mandatory)][string]$Name
    )
    if ($null -eq $Object) { return $null }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Get-EpochEvidenceErrors {
    param([Parameter(Mandatory)]$Epoch)

    $errors = [System.Collections.Generic.List[string]]::new()
    $path = [string](Get-PropertyValue $Epoch 'path')
    [object[]]$owners = @((Get-PropertyValue $Epoch 'ordered_owners') | ForEach-Object { [string]$_ })
    $currentOwner = [string](Get-PropertyValue $Epoch 'current_owner')
    $transitionKind = [string](Get-PropertyValue $Epoch 'transition_kind')
    [object[]]$predecessors = @((Get-PropertyValue $Epoch 'completed_predecessors'))
    $requiredBase = [string](Get-PropertyValue $Epoch 'required_successor_base_sha')
    $currentIndex = [array]::IndexOf($owners, $currentOwner)
    $ownerCount = @($owners).Count
    $predecessorCount = @($predecessors).Count

    if ($ownerCount -lt 1) { $errors.Add("state epoch '$path' must contain at least one ordered owner") }
    if (@($owners | Select-Object -Unique).Count -ne $ownerCount) { $errors.Add("state epoch '$path' contains duplicate owners") }
    if ($currentIndex -lt 0) { $errors.Add("state epoch '$path' current owner '$currentOwner' is not in its ordered owners") }
    if ($transitionKind -notin @('integration', 'rework')) { $errors.Add("state epoch '$path' has unsupported transition_kind '$transitionKind'") }

    if ($transitionKind -eq 'integration' -and $currentIndex -ge 0) {
        [object[]]$expectedPredecessors = if ($currentIndex -eq 0) { @() } else { @($owners[0..($currentIndex - 1)]) }
        $expectedPredecessorCount = $currentIndex
        if ($predecessorCount -ne $expectedPredecessorCount) {
            $errors.Add("state epoch '$path' predecessor evidence count is $predecessorCount, expected $expectedPredecessorCount")
        }
        $integrationShas = [System.Collections.Generic.List[string]]::new()
        foreach ($expectedOwner in $expectedPredecessors) {
            $matches = @($predecessors | Where-Object { [string](Get-PropertyValue $_ 'owner') -ceq $expectedOwner })
            if ($matches.Count -ne 1) {
                $errors.Add("state epoch '$path' predecessor '$expectedOwner' evidence count is $($matches.Count), expected 1")
                continue
            }
            $entry = $matches[0]
            $checkerVerdict = [string](Get-PropertyValue $entry 'checker_verdict')
            $checkerArtifact = [string](Get-PropertyValue $entry 'checker_artifact')
            $postReviewVerdict = [string](Get-PropertyValue $entry 'post_review_verdict')
            $postReviewArtifact = [string](Get-PropertyValue $entry 'post_review_artifact')
            $integrationSha = [string](Get-PropertyValue $entry 'integration_sha')
            if ($checkerVerdict -cne 'PASS' -or [string]::IsNullOrWhiteSpace($checkerArtifact)) { $errors.Add("state epoch '$path' predecessor '$expectedOwner' lacks checker PASS evidence") }
            if ($postReviewVerdict -cne 'PASS' -or [string]::IsNullOrWhiteSpace($postReviewArtifact)) { $errors.Add("state epoch '$path' predecessor '$expectedOwner' lacks post-review PASS evidence") }
            if ($integrationSha -notmatch '^[0-9a-fA-F]{40}$') { $errors.Add("state epoch '$path' predecessor '$expectedOwner' lacks a full integration SHA") }
            else { $integrationShas.Add($integrationSha.ToLowerInvariant()) }
        }
        if ($expectedPredecessorCount -eq 0) {
            if (-not [string]::IsNullOrWhiteSpace($requiredBase)) { $errors.Add("state epoch '$path' first owner must not require a predecessor base") }
        }
        elseif ($requiredBase -notmatch '^[0-9a-fA-F]{40}$') {
            $errors.Add("state epoch '$path' successor base requirement is missing or not a full SHA")
        }
        elseif ($integrationShas.Count -eq $expectedPredecessorCount -and -not [string]::Equals($requiredBase, $integrationShas[$integrationShas.Count - 1], [System.StringComparison]::OrdinalIgnoreCase)) {
            $errors.Add("state epoch '$path' successor base '$requiredBase' does not equal the latest predecessor integration '$($integrationShas[$integrationShas.Count - 1])'")
        }
    }
    elseif ($transitionKind -eq 'rework' -and $currentIndex -ge 0) {
        if ($currentIndex -eq 0) { $errors.Add("state epoch '$path' rework transition has no rejected predecessor") }
        if ($requiredBase -notmatch '^[0-9a-fA-F]{40}$') { $errors.Add("state epoch '$path' rework base must be a full SHA") }
        if ($predecessorCount -ne $currentIndex) { $errors.Add("state epoch '$path' rework predecessor evidence count is $predecessorCount, expected $currentIndex") }
        $immediateOwner = if ($currentIndex -gt 0) { $owners[$currentIndex - 1] } else { $null }
        for ($ownerIndex = 0; $ownerIndex -lt [math]::Max(0, $currentIndex - 1); $ownerIndex++) {
            $acceptedOwner = $owners[$ownerIndex]
            $acceptedMatches = @($predecessors | Where-Object { [string](Get-PropertyValue $_ 'owner') -ceq $acceptedOwner })
            if ($acceptedMatches.Count -ne 1) { $errors.Add("state epoch '$path' accepted predecessor '$acceptedOwner' evidence count is $($acceptedMatches.Count), expected 1"); continue }
            $accepted = $acceptedMatches[0]
            if ([string](Get-PropertyValue $accepted 'checker_verdict') -cne 'PASS' -or [string]::IsNullOrWhiteSpace([string](Get-PropertyValue $accepted 'checker_artifact'))) { $errors.Add("state epoch '$path' accepted predecessor '$acceptedOwner' lacks checker PASS evidence") }
            if ([string](Get-PropertyValue $accepted 'post_review_verdict') -cne 'PASS' -or [string]::IsNullOrWhiteSpace([string](Get-PropertyValue $accepted 'post_review_artifact'))) { $errors.Add("state epoch '$path' accepted predecessor '$acceptedOwner' lacks post-review PASS evidence") }
            if ([string](Get-PropertyValue $accepted 'integration_sha') -notmatch '^[0-9a-fA-F]{40}$') { $errors.Add("state epoch '$path' accepted predecessor '$acceptedOwner' lacks a full integration SHA") }
        }
        $matches = @($predecessors | Where-Object { [string](Get-PropertyValue $_ 'owner') -ceq $immediateOwner })
        if ($matches.Count -ne 1) { $errors.Add("state epoch '$path' rework predecessor '$immediateOwner' evidence count is $($matches.Count), expected 1") }
        else {
            $entry = $matches[0]
            $checkerVerdict = [string](Get-PropertyValue $entry 'checker_verdict')
            $checkerArtifact = [string](Get-PropertyValue $entry 'checker_artifact')
            $checkerSha = [string](Get-PropertyValue $entry 'checker_sha256')
            $rejectedHead = [string](Get-PropertyValue $entry 'rejected_head_sha')
            $integrationSha = [string](Get-PropertyValue $entry 'integration_sha')
            if ($checkerVerdict -notin @('FAIL', 'REVISE_HOLD') -or [string]::IsNullOrWhiteSpace($checkerArtifact) -or $checkerSha -notmatch '^[0-9a-fA-F]{64}$') {
                $errors.Add("state epoch '$path' rework predecessor '$immediateOwner' lacks exact rejected-checker evidence")
            }
            if ($rejectedHead -notmatch '^[0-9a-fA-F]{40}$' -or -not [string]::Equals($rejectedHead, $requiredBase, [System.StringComparison]::OrdinalIgnoreCase)) { $errors.Add("state epoch '$path' rework base does not equal the rejected predecessor head") }
            if (-not [string]::IsNullOrWhiteSpace($integrationSha)) { $errors.Add("state epoch '$path' rejected rework predecessor '$immediateOwner' must not claim integration") }
        }
    }

    return @($errors)
}

function Invoke-StateContractAudit {
    param(
        [Parameter(Mandatory)]$StateObject,
        [Parameter(Mandatory)]$Ledger,
        [Parameter(Mandatory)][string]$ObservedPlanSha256,
        [Parameter(Mandatory)][string]$ExpectedPlanSha256
    )

    $errors = [System.Collections.Generic.List[string]]::new()
    if ((Get-PropertyValue $StateObject 'schema_version') -ne 1) { $errors.Add('ownership state schema_version must be 1') }
    $statePlan = Get-PropertyValue $StateObject 'plan'
    $statePlanPath = [string](Get-PropertyValue $statePlan 'path')
    $statePlanSha = [string](Get-PropertyValue $statePlan 'sha256')
    if ($statePlanPath -cne '.agent/plans/2026-07-10-engram-production-ready-master-plan.md') { $errors.Add("ownership state plan path '$statePlanPath' is not the canonical tracked master plan") }
    if (-not (Test-ExpectedPlanHash -ObservedSha256 $ObservedPlanSha256 -ExpectedSha256 $ExpectedPlanSha256)) { $errors.Add("observed plan SHA256 '$ObservedPlanSha256' does not match expected '$ExpectedPlanSha256'") }
    if (-not (Test-ExpectedPlanHash -ObservedSha256 $statePlanSha -ExpectedSha256 $ExpectedPlanSha256)) { $errors.Add("ownership state plan SHA256 '$statePlanSha' does not match expected '$ExpectedPlanSha256'") }

    [object[]]$stateEpochs = @((Get-PropertyValue $StateObject 'path_epochs'))
    $seenPaths = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($stateEpoch in $stateEpochs) {
        $statePath = [string](Get-PropertyValue $stateEpoch 'path')
        try {
            $normalized = Normalize-OwnershipPath $statePath
            if ($normalized.kind -ne 'exact' -or $normalized.path -cne $statePath) { throw "state epoch path must be normalized exact path: '$statePath'" }
        }
        catch { $errors.Add($_.Exception.Message); continue }
        if (-not $seenPaths.Add($statePath)) { $errors.Add("ownership state repeats path '$statePath'") }
        $ledgerMatches = @($Ledger.epochs | Where-Object { $_.path -ceq $statePath })
        if ($ledgerMatches.Count -ne 1) { $errors.Add("ownership state path '$statePath' has $($ledgerMatches.Count) matching plan epochs, expected 1") }
        else {
            $stateOwners = @((Get-PropertyValue $stateEpoch 'ordered_owners') | ForEach-Object { [string]$_ })
            if (-not (Test-SameStringSequence $ledgerMatches[0].owners $stateOwners)) {
                $errors.Add("ownership state path '$statePath' order differs from plan: plan=$($ledgerMatches[0].owners -join ' -> '), state=$($stateOwners -join ' -> ')")
            }
        }
        foreach ($evidenceError in @(Get-EpochEvidenceErrors $stateEpoch)) { $errors.Add($evidenceError) }
    }
    foreach ($planEpoch in $Ledger.epochs) {
        if (-not $seenPaths.Contains($planEpoch.path)) { $errors.Add("plan epoch '$($planEpoch.path)' is missing from ownership state") }
    }

    return [pscustomobject][ordered]@{
        schema_version = 1
        verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        plan_sha256 = $statePlanSha
        path_epochs = $stateEpochs
        errors = @($errors)
    }
}

function Invoke-DiffEpochAuthority {
    param(
        [Parameter(Mandatory)][string]$Slice,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$ChangedPaths,
        [Parameter(Mandatory)]$State,
        [Parameter(Mandatory)][string]$Repository,
        [Parameter(Mandatory)][string]$BaseResolved
    )

    $errors = [System.Collections.Generic.List[string]]::new()
    $evaluated = [System.Collections.Generic.List[object]]::new()
    [object[]]$stateEpochs = @((Get-PropertyValue $State 'path_epochs'))
    foreach ($path in @($ChangedPaths | Sort-Object -Unique)) {
        $matches = @($stateEpochs | Where-Object { [string](Get-PropertyValue $_ 'path') -ceq [string]$path })
        if ($matches.Count -eq 0) { continue }
        if ($matches.Count -ne 1) { $errors.Add("changed epoch path '$path' has $($matches.Count) state records"); continue }
        $epoch = $matches[0]
        foreach ($evidenceError in @(Get-EpochEvidenceErrors $epoch)) { $errors.Add($evidenceError) }
        $currentOwner = [string](Get-PropertyValue $epoch 'current_owner')
        $transitionKind = [string](Get-PropertyValue $epoch 'transition_kind')
        $requiredBase = [string](Get-PropertyValue $epoch 'required_successor_base_sha')
        $ownerPass = $currentOwner -ceq $Slice
        if (-not $ownerPass) { $errors.Add("changed epoch path '$path' current owner is '$currentOwner', not '$Slice'") }
        $basePass = if ($ownerPass) { $true } else { $null }
        if ($ownerPass -and -not [string]::IsNullOrWhiteSpace($requiredBase)) {
            if ($transitionKind -eq 'rework') {
                $basePass = [string]::Equals($requiredBase, $BaseResolved, [System.StringComparison]::OrdinalIgnoreCase)
                if (-not $basePass) { $errors.Add("rework slice '$Slice' base '$BaseResolved' must equal rejected predecessor '$requiredBase' for '$path'") }
            }
            else {
                & git -C $Repository merge-base --is-ancestor $requiredBase $BaseResolved 2>$null
                $ancestorExit = $LASTEXITCODE
                $basePass = $ancestorExit -eq 0
                if ($ancestorExit -eq 1) { $errors.Add("slice '$Slice' base '$BaseResolved' does not descend from predecessor integration '$requiredBase' for '$path'") }
                elseif ($ancestorExit -ne 0) { $errors.Add("predecessor ancestry check failed with exit $ancestorExit for '$path'") }
            }
        }
        $evaluated.Add([pscustomobject][ordered]@{ path = $path; current_owner = $currentOwner; owner_pass = $ownerPass; transition_kind = $transitionKind; required_base_sha = $requiredBase; base_pass = $basePass })
    }
    return [pscustomobject][ordered]@{ verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }; evaluated = @($evaluated); errors = @($errors) }
}

function New-SyntheticPlan {
    param(
        [Parameter(Mandatory)][string]$Rows,
        [Parameter(Mandatory)][AllowEmptyString()][string]$EpochRows
    )
    return @"
## 4. Worktree and Ownership Matrix

| Slice | Branch | Exclusive maker paths | Dependencies | Required proof |
| --- | --- | --- | --- | --- |
$Rows

### 4.1 Ownership Epochs and Automated Overlap Gate

| Exact path | Current/first epoch | Next epoch | Transfer gate |
| --- | --- | --- | --- |
$EpochRows

### 4.2 Exact Ownership of Failures
"@
}

function Assert-SelfTestCondition {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "SELFTEST FAIL: $Message" }
}

function New-SyntheticOwnershipState {
    param(
        [Parameter(Mandatory)][string]$PlanSha256,
        [Parameter(Mandatory)][string]$RequiredIntegrationSha,
        [switch]$MissingPredecessorEvidence
    )

    $predecessors = if ($MissingPredecessorEvidence) { @() } else {
        @([pscustomobject][ordered]@{
            owner = 'A'
            checker_verdict = 'PASS'
            checker_artifact = '.agent/reviews/a-check.md'
            post_review_verdict = 'PASS'
            post_review_artifact = '.agent/reviews/a-post-review.md'
            integration_sha = $RequiredIntegrationSha
        })
    }
    return [pscustomobject][ordered]@{
        schema_version = 1
        plan = [pscustomobject][ordered]@{ path = '.agent/plans/synthetic.md'; sha256 = $PlanSha256 }
        path_epochs = @([pscustomobject][ordered]@{
            path = 'src/shared.go'
            ordered_owners = @('A', 'B')
            current_owner = 'B'
            transition_kind = 'integration'
            completed_predecessors = $predecessors
            required_successor_base_sha = $RequiredIntegrationSha
        })
    }
}

function Invoke-SelfTest {
    $singleOwnerEpoch = New-SyntheticPlan -Rows '| SOLO | `work/solo` | `src/single.go` | none | proof |' -EpochRows '| `src/single.go` | SOLO | — | SOLO checker and post-review PASS, commit integrated before any successor |'
    $singleOwnerResult = Invoke-OwnershipAudit $singleOwnerEpoch 'selftest-single-owner-epoch'
    Assert-SelfTestCondition ($singleOwnerResult.verdict -eq 'PASS') ("single-owner tracked epoch was rejected: " + ($singleOwnerResult.errors -join '; '))

    $hashFixtureRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("engram-plan-hash-selftest-" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $hashFixtureRoot -Force | Out-Null
    try {
        $lfFixture = Join-Path $hashFixtureRoot 'plan-lf.md'
        $crlfFixture = Join-Path $hashFixtureRoot 'plan-crlf.md'
        $semanticFixture = Join-Path $hashFixtureRoot 'plan-semantic.md'
        $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
        [System.IO.File]::WriteAllText($lfFixture, "alpha`nbeta`n", $utf8NoBom)
        [System.IO.File]::WriteAllText($crlfFixture, "alpha`r`nbeta`r`n", $utf8NoBom)
        [System.IO.File]::WriteAllText($semanticFixture, "alpha`ngamma`n", $utf8NoBom)
        $lfHash = Get-CanonicalUtf8LfFileSha256 -Path $lfFixture
        $crlfHash = Get-CanonicalUtf8LfFileSha256 -Path $crlfFixture
        $semanticHash = Get-CanonicalUtf8LfFileSha256 -Path $semanticFixture
        Assert-SelfTestCondition ($lfHash -ceq $crlfHash) 'canonical authority hash differs between LF and CRLF checkout forms'
        Assert-SelfTestCondition ($lfHash -cne $semanticHash) 'canonical authority hash accepted a semantic mutation'
    }
    finally {
        Remove-Item -LiteralPath $hashFixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
    }

    $reorderedEpoch = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/shared.go`` | none | proof |`n| B | ``work/b`` | ``src/shared.go`` | A integrated | proof |" -EpochRows '| `src/shared.go` | B | A | B checker and post-review PASS, commit integrated, A rebased |'
    $reorderedResult = Invoke-OwnershipAudit $reorderedEpoch 'selftest-reordered-epoch'
    Assert-SelfTestCondition ($reorderedResult.verdict -eq 'FAIL') 'reversed epoch order was accepted'

    $orderedEpoch = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/shared.go`` | none | proof |`n| B | ``work/b`` | ``src/shared.go`` | A integrated | proof |" -EpochRows '| `src/shared.go` | A | B | A checker and post-review PASS, commit integrated, B rebased |'
    $orderedResult = Invoke-OwnershipAudit $orderedEpoch 'selftest-ordered-epoch'
    Assert-SelfTestCondition ($orderedResult.verdict -eq 'PASS') ("correct epoch order was rejected: " + ($orderedResult.errors -join '; '))

    $repository = ([string]@(& git rev-parse --show-toplevel 2>&1)[-1]).Trim()
    $positiveBase = ([string]@(& git -C $repository rev-parse HEAD 2>&1)[-1]).Trim().ToLowerInvariant()
    $requiredIntegration = ([string]@(& git -C $repository rev-parse HEAD^ 2>&1)[-1]).Trim().ToLowerInvariant()
    $wrongBase = ([string]@(& git -C $repository rev-list --max-parents=0 HEAD 2>&1)[0]).Trim().ToLowerInvariant()
    $syntheticPlanHash = ('a' * 64)
    $state = New-SyntheticOwnershipState -PlanSha256 $syntheticPlanHash -RequiredIntegrationSha $requiredIntegration
    $missingEvidenceState = New-SyntheticOwnershipState -PlanSha256 $syntheticPlanHash -RequiredIntegrationSha $requiredIntegration -MissingPredecessorEvidence
    $firstOwnerState = [pscustomobject][ordered]@{
        path = 'src/shared.go'; ordered_owners = @('A', 'B'); current_owner = 'A'; transition_kind = 'integration'
        completed_predecessors = @(); required_successor_base_sha = $null
    }
    Assert-SelfTestCondition (@(Get-EpochEvidenceErrors $firstOwnerState).Count -eq 0) 'first owner with an empty predecessor list was rejected or raised under StrictMode'
    $singleOwnerState = [pscustomobject][ordered]@{
        path = 'src/single.go'; ordered_owners = @('SOLO'); current_owner = 'SOLO'; transition_kind = 'integration'
        completed_predecessors = @(); required_successor_base_sha = $null
    }
    Assert-SelfTestCondition (@(Get-EpochEvidenceErrors $singleOwnerState).Count -eq 0) 'single-owner state epoch was rejected'
    $reworkEpoch = [pscustomobject][ordered]@{
        path = 'src/shared.go'; ordered_owners = @('A', 'B'); current_owner = 'B'; transition_kind = 'rework'
        completed_predecessors = @([pscustomobject][ordered]@{
            owner = 'A'; checker_verdict = 'FAIL'; checker_artifact = '.agent/reviews/a-check.md'
            checker_sha256 = ('c' * 64); rejected_head_sha = $requiredIntegration; integration_sha = $null
        })
        required_successor_base_sha = $requiredIntegration
    }
    Assert-SelfTestCondition (@(Get-EpochEvidenceErrors $reworkEpoch).Count -eq 0) 'valid rejected-head rework evidence was rejected'
    $reworkWrongHead = $reworkEpoch.PSObject.Copy(); $reworkWrongHead.completed_predecessors = @($reworkEpoch.completed_predecessors | ForEach-Object { $_.PSObject.Copy() }); $reworkWrongHead.completed_predecessors[0].rejected_head_sha = $wrongBase
    Assert-SelfTestCondition (@(Get-EpochEvidenceErrors $reworkWrongHead).Count -gt 0) 'rework state whose required base differs from the rejected head was accepted'
    Assert-SelfTestCondition (Test-ExpectedPlanHash -ObservedSha256 $syntheticPlanHash -ExpectedSha256 $syntheticPlanHash) 'matching expected plan hash was rejected'
    Assert-SelfTestCondition (-not (Test-ExpectedPlanHash -ObservedSha256 $syntheticPlanHash -ExpectedSha256 ('b' * 64))) 'mismatched expected plan hash was accepted'
    $nonCurrentOwner = Invoke-DiffEpochAuthority -Slice A -ChangedPaths @('src/shared.go') -State $state -Repository $repository -BaseResolved $positiveBase
    Assert-SelfTestCondition ($nonCurrentOwner.verdict -eq 'FAIL') 'non-current epoch owner was accepted'
    Assert-SelfTestCondition (@($nonCurrentOwner.errors).Count -eq 1 -and $null -eq $nonCurrentOwner.evaluated[0].base_pass) 'non-current owner incorrectly evaluated the current successor base contract'
    $missingEvidence = Invoke-DiffEpochAuthority -Slice B -ChangedPaths @('src/shared.go') -State $missingEvidenceState -Repository $repository -BaseResolved $positiveBase
    Assert-SelfTestCondition ($missingEvidence.verdict -eq 'FAIL') 'successor without checker/post-review/integration evidence was accepted'
    $wrongBaseResult = Invoke-DiffEpochAuthority -Slice B -ChangedPaths @('src/shared.go') -State $state -Repository $repository -BaseResolved $wrongBase
    Assert-SelfTestCondition ($wrongBaseResult.verdict -eq 'FAIL') 'correct owner on a base that omits the predecessor integration was accepted'
    $descendantBaseResult = Invoke-DiffEpochAuthority -Slice B -ChangedPaths @('src/shared.go') -State $state -Repository $repository -BaseResolved $positiveBase
    Assert-SelfTestCondition ($descendantBaseResult.verdict -eq 'PASS') ("descendant successor base was rejected: " + ($descendantBaseResult.errors -join '; '))

    $undeclared = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/shared.go`` | none | proof |`n| B | ``work/b`` | ``src/shared.go`` | none | proof |" -EpochRows ''
    Assert-SelfTestCondition ((Invoke-OwnershipAudit $undeclared 'selftest-undeclared').verdict -eq 'FAIL') 'undeclared exact overlap was accepted'

    $declaredPrefix = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/shared.go`` | none | proof |`n| B | ``work/b`` | ``src/**`` only | A integrated | proof |" -EpochRows '| `src/shared.go` | A | B | A checker and post-review PASS, commit integrated, B rebased |'
    $declaredPrefixResult = Invoke-OwnershipAudit $declaredPrefix 'selftest-declared-prefix'
    Assert-SelfTestCondition ($declaredPrefixResult.verdict -eq 'PASS' -and $declaredPrefixResult.counts.prefix_intersections -eq 1) ("declared exact/prefix epoch failed: " + ($declaredPrefixResult.errors -join '; '))

    $prefix = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/**`` | none | proof |`n| B | ``work/b`` | ``src/child.go`` | none | proof |" -EpochRows ''
    $prefixResult = Invoke-OwnershipAudit $prefix 'selftest-prefix'
    Assert-SelfTestCondition ($prefixResult.verdict -eq 'FAIL' -and $prefixResult.counts.undeclared_prefix_intersections -eq 1) 'undeclared prefix/exact overlap was accepted'

    $prefixPrefix = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/**`` | none | proof |`n| B | ``work/b`` | ``src/child/**`` | none | proof |" -EpochRows ''
    Assert-SelfTestCondition ((Invoke-OwnershipAudit $prefixPrefix 'selftest-prefix-prefix').verdict -eq 'FAIL') 'prefix/prefix overlap was accepted'

    $wildcard = New-SyntheticPlan '| A | `work/a` | `src/*.go` | none | proof |' ''
    Assert-SelfTestCondition ((Invoke-OwnershipAudit $wildcard 'selftest-wildcard').verdict -eq 'FAIL') 'unknown wildcard ownership scope was accepted'

    $descriptive = New-SyntheticPlan '| A | `work/a` | `src/a.go` and friends | none | proof |' ''
    Assert-SelfTestCondition ((Invoke-OwnershipAudit $descriptive 'selftest-descriptive').verdict -eq 'FAIL') 'descriptive ownership scope outside code spans was accepted'

    $qualifiers = New-SyntheticPlan '| A | `work/a` | new `src/a.go`, generated `src/generated.go`, legacy exact report `.agent/reports/a.md`, legacy evidence prefix `.agent/specs/a/evidence/**` only | none | proof |' ''
    $qualifierResult = Invoke-OwnershipAudit $qualifiers 'selftest-qualifiers'
    Assert-SelfTestCondition ($qualifierResult.verdict -eq 'PASS') ("literal qualifier grammar failed: " + ($qualifierResult.errors -join '; '))

    $wrongEpoch = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/shared.go`` | none | proof |`n| B | ``work/b`` | ``src/shared.go`` | none | proof |" -EpochRows '| `src/shared.go` | A | C | A checker and post-review PASS, commit integrated, C rebased |'
    Assert-SelfTestCondition ((Invoke-OwnershipAudit $wrongEpoch 'selftest-wrong-epoch').verdict -eq 'FAIL') 'wrong declared epoch owner set was accepted'

    $vagueEpoch = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/shared.go`` | none | proof |`n| B | ``work/b`` | ``src/shared.go`` | none | proof |" -EpochRows '| `src/shared.go` | A | B | Someone will take care of this eventually when circumstances permit |'
    Assert-SelfTestCondition ((Invoke-OwnershipAudit $vagueEpoch 'selftest-vague-epoch').verdict -eq 'FAIL') 'vague epoch transfer prose was accepted as a concrete gate'

    $diffPlan = New-SyntheticPlan '| DB-X | `work/db-x` | `src/owned.go`, legacy exact report `.agent/reports/legacy-maker.md`, legacy evidence prefix `.agent/specs/db-x/evidence/**` | none | proof |' ''
    $diffLedger = Invoke-OwnershipAudit $diffPlan 'selftest-diff-ledger'
    Assert-SelfTestCondition ($diffLedger.verdict -eq 'PASS') ("diff ledger fixture failed: " + ($diffLedger.errors -join '; '))
    $diffDeclarations = @($diffLedger.declarations | Where-Object owner -ceq 'DB-X')
    $evidence = Resolve-DiffNamespace -Kind evidence -Token '.agent/specs/db-x/evidence/**' -SliceName DB-X -SliceDeclarations $diffDeclarations
    $report = Resolve-DiffNamespace -Kind report -Token '.agent/reports/legacy-maker.md' -SliceName DB-X -SliceDeclarations $diffDeclarations

    $validEntries = @(
        [pscustomobject]@{ status = 'M'; paths = @('src/owned.go'); raw = "M`tsrc/owned.go" },
        [pscustomobject]@{ status = 'A'; paths = @('.agent/specs/db-x/evidence/proof.json'); raw = "A`t.agent/specs/db-x/evidence/proof.json" },
        [pscustomobject]@{ status = 'A'; paths = @('.agent/reports/legacy-maker.md'); raw = "A`t.agent/reports/legacy-maker.md" }
    )
    $validDiff = Invoke-DiffEntryAudit $validEntries $diffDeclarations $evidence $report
    Assert-SelfTestCondition ($validDiff.violations.Count -eq 0) 'declared diff paths were rejected'

    $undeclaredEntries = @(
        [pscustomobject]@{ status = 'M'; paths = @('pkg/models/snapshot.go'); raw = "M`tpkg/models/snapshot.go" },
        [pscustomobject]@{ status = 'A'; paths = @('.agent/reports/undeclared.md'); raw = "A`t.agent/reports/undeclared.md" }
    )
    $undeclaredDiff = Invoke-DiffEntryAudit $undeclaredEntries $diffDeclarations $evidence $report
    Assert-SelfTestCondition ($undeclaredDiff.violations.Count -eq 2) 'undeclared product or .agent path was accepted'

    $wrongCaseDiff = Invoke-DiffEntryAudit @([pscustomobject]@{ status = 'M'; paths = @('Src/owned.go'); raw = "M`tSrc/owned.go" }) $diffDeclarations $evidence $report
    Assert-SelfTestCondition ($wrongCaseDiff.violations.Count -eq 1) 'case-mismatched Git path was accepted as an exact declaration'

    $renameEntry = @([pscustomobject]@{ status = 'R100'; paths = @('src/owned.go', 'src/not-owned.go'); raw = "R100`tsrc/owned.go`tsrc/not-owned.go" })
    $renameDiff = Invoke-DiffEntryAudit $renameEntry $diffDeclarations $evidence $report
    Assert-SelfTestCondition ($renameDiff.violations.Count -eq 1 -and $renameDiff.violations[0].path -eq 'src/not-owned.go') 'rename destination escaped ownership validation'

    $badNamespaceRejected = $false
    try {
        $null = Resolve-DiffNamespace -Kind evidence -Token '.agent/other/**' -SliceName DB-X -SliceDeclarations $diffDeclarations
    }
    catch { $badNamespaceRejected = $true }
    Assert-SelfTestCondition $badNamespaceRejected 'undeclared evidence namespace was accepted'

    $canonicalEvidence = Resolve-DiffNamespace -Kind evidence -Token '.agent/reports/evidence/production-ready/db-x/**' -SliceName DB-X -SliceDeclarations $diffDeclarations
    Assert-SelfTestCondition ($canonicalEvidence.policy -eq 'canonical-derived-default') 'canonical evidence namespace was rejected'

    $parsedRename = ConvertFrom-GitNameStatusLines @("R100`tsrc/owned.go`tsrc/not-owned.go")
    Assert-SelfTestCondition ($parsedRename.errors.Count -eq 0 -and $parsedRename.entries.Count -eq 1 -and $parsedRename.entries[0].paths.Count -eq 2) 'name-status rename parsing failed'

    $parsedAgentPath = ConvertFrom-GitNameStatusLines @("A`t.agent/specs/db-x/evidence/proof.json")
    Assert-SelfTestCondition ($parsedAgentPath.errors.Count -eq 0 -and $parsedAgentPath.entries[0].paths[0] -eq '.agent/specs/db-x/evidence/proof.json') '.agent path normalization stripped its leading dot'

    $scopeSha = ('d' * 64)
    $rejectedHead = ('e' * 40)
    $scopePlan = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/shared.go`` | none | proof |`n| B | ``work/b`` | ``src/shared.go`` | A integrated | proof |`n| ROOT | root-owned | ``.agent/root/**`` only | A and B accepted | proof |" -EpochRows '| `src/shared.go` | A | B | A checker and post-review PASS, commit integrated, B rebased |'
    $scopeState = [pscustomobject][ordered]@{
        schema_version = 1
        scope_map = [pscustomobject][ordered]@{ path = '.agent/plans/2026-07-10-engram-production-ready-scope-map.json'; sha256 = $scopeSha }
        path_epochs = @()
    }
    $scopeFixture = [pscustomobject][ordered]@{
        schema_version = 1
        kind = 'production-ready-scope-map'
        plan_path = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md'
        ownership_state_path = '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json'
        register_snapshot = [pscustomobject][ordered]@{ source_path = '.agent/reports/register.json'; sha256 = ('f' * 64); updated_at = '2026-07-10T00:00:00Z'; row_count = 5; unique_slice_count = 5; goal_status = 'ACTIVE' }
        allowed_classifications = @('maker', 'checker-evidence', 'meta-fold', 'historical', 'root-integration')
        live_conformance_policy = [pscustomobject][ordered]@{
            mode = 'structural-projection'
            exact_fields = @('slice', 'classification', 'plan_owners')
            snapshot_only_fields = @('register_snapshot.sha256', 'register_snapshot.updated_at', 'register_status', 'register_head', 'register_notes')
            load_bearing_entry_field = 'load_bearing'
            acceptance_tokens = @('PASS', 'READY_FOR_INTEGRATION', 'PRODUCT_ACCEPTED', 'ACCEPTED', 'INTEGRATED', 'COMPLETE')
            rejection_tokens = @('REVISE', 'REJECT', 'FAIL', 'DIAGNOSTIC', 'HOLD', 'BLOCKED', 'PENDING', 'UNACCEPTED', 'NOT_ACCEPTED')
        }
        entries = @(
            [pscustomobject][ordered]@{ slice = 'A'; classification = 'maker'; plan_owners = @('A'); register_status = 'PENDING'; register_head = '' },
            [pscustomobject][ordered]@{ slice = 'B'; classification = 'checker-evidence'; plan_owners = @('B'); register_status = 'PENDING'; register_head = '' },
            [pscustomobject][ordered]@{ slice = 'META'; classification = 'meta-fold'; plan_owners = @('A', 'B'); register_status = 'PENDING'; register_head = '' },
            [pscustomobject][ordered]@{ slice = 'OLD'; classification = 'historical'; plan_owners = @('A'); register_status = 'REVISE_HOLD'; register_head = $rejectedHead; load_bearing = [pscustomobject][ordered]@{ policy = 'rejected_heads_must_not_be_accepted'; rejected_heads = @($rejectedHead) } },
            [pscustomobject][ordered]@{ slice = 'ROOT'; classification = 'root-integration'; plan_owners = @('ROOT'); register_status = 'BLOCKED'; register_head = '' }
        )
    }
    $registerFixture = [pscustomobject][ordered]@{
        updated_at = '2026-07-10T01:00:00Z'
        criteria = @(
            [pscustomobject][ordered]@{ slice = 'A'; status = 'PENDING'; head = ''; notes = 'mutable' },
            [pscustomobject][ordered]@{ slice = 'B'; status = 'PENDING'; head = ''; notes = 'mutable' },
            [pscustomobject][ordered]@{ slice = 'META'; status = 'PENDING'; head = ''; notes = 'mutable' },
            [pscustomobject][ordered]@{ slice = 'OLD'; status = 'REVISE_HOLD'; head = $rejectedHead; notes = 'mutable' },
            [pscustomobject][ordered]@{ slice = 'ROOT'; status = 'BLOCKED'; head = ''; notes = 'mutable' }
        )
    }
    $validScope = Invoke-ScopeContractAudit -ScopeMapObject $scopeFixture -ObservedScopeMapSha256 $scopeSha -ExpectedScopeMapSha256 $scopeSha -StateObject $scopeState -PlanText $scopePlan -RegisterObject $registerFixture
    Assert-SelfTestCondition ($validScope.verdict -eq 'PASS') ("valid structural scope fixture was rejected: " + ($validScope.errors -join '; '))

    $deletedPlanAndEpoch = New-SyntheticPlan -Rows '| A | `work/a` | `src/shared.go` | none | proof |' -EpochRows ''
    $deletedState = $scopeState | ConvertTo-Json -Depth 20 | ConvertFrom-Json -Depth 20
    $deletedState.path_epochs = @()
    $deletedScopeResult = Invoke-ScopeContractAudit -ScopeMapObject $scopeFixture -ObservedScopeMapSha256 $scopeSha -ExpectedScopeMapSha256 $scopeSha -StateObject $deletedState -PlanText $deletedPlanAndEpoch -RegisterObject $registerFixture
    Assert-SelfTestCondition ($deletedScopeResult.verdict -eq 'FAIL') 'plan row plus state epoch deletion was accepted while the frozen scope still required its owner'

    $missingEntry = $scopeFixture | ConvertTo-Json -Depth 20 | ConvertFrom-Json -Depth 20
    $missingEntry.entries = @($missingEntry.entries | Where-Object slice -CNE 'B')
    $missingEntry.register_snapshot.row_count = 4
    $missingEntry.register_snapshot.unique_slice_count = 4
    $missingEntryResult = Invoke-ScopeContractAudit -ScopeMapObject $missingEntry -ObservedScopeMapSha256 $scopeSha -ExpectedScopeMapSha256 $scopeSha -StateObject $scopeState -PlanText $scopePlan -RegisterObject $registerFixture
    Assert-SelfTestCondition ($missingEntryResult.verdict -eq 'FAIL') 'live register slice missing from the scope map was accepted'

    $missingFoldOwner = $scopeFixture | ConvertTo-Json -Depth 20 | ConvertFrom-Json -Depth 20
    @($missingFoldOwner.entries | Where-Object slice -CEQ 'META')[0].plan_owners = @('A', 'MISSING')
    $missingFoldResult = Invoke-ScopeContractAudit -ScopeMapObject $missingFoldOwner -ObservedScopeMapSha256 $scopeSha -ExpectedScopeMapSha256 $scopeSha -StateObject $scopeState -PlanText $scopePlan -RegisterObject $registerFixture
    Assert-SelfTestCondition ($missingFoldResult.verdict -eq 'FAIL') 'fold targeting a missing owner was accepted'

    $staleAcceptedRegister = $registerFixture | ConvertTo-Json -Depth 20 | ConvertFrom-Json -Depth 20
    @($staleAcceptedRegister.criteria | Where-Object slice -CEQ 'OLD')[0].status = 'READY_FOR_INTEGRATION'
    $staleAcceptedResult = Invoke-ScopeContractAudit -ScopeMapObject $scopeFixture -ObservedScopeMapSha256 $scopeSha -ExpectedScopeMapSha256 $scopeSha -StateObject $scopeState -PlanText $scopePlan -RegisterObject $staleAcceptedRegister
    Assert-SelfTestCondition ($staleAcceptedResult.verdict -eq 'FAIL') 'explicitly rejected historical head was accepted as current'

    $ordinaryProgressRegister = $registerFixture | ConvertTo-Json -Depth 20 | ConvertFrom-Json -Depth 20
    $ordinaryProgressRegister.updated_at = '2026-07-10T02:00:00Z'
    $progressRow = @($ordinaryProgressRegister.criteria | Where-Object slice -CEQ 'B')[0]
    $progressRow.status = 'READY_FOR_INTEGRATION'
    $progressRow.head = ('1' * 40)
    $progressRow.notes = 'ordinary same-lane progress changed evidence text'
    $ordinaryProgressResult = Invoke-ScopeContractAudit -ScopeMapObject $scopeFixture -ObservedScopeMapSha256 $scopeSha -ExpectedScopeMapSha256 $scopeSha -StateObject $scopeState -PlanText $scopePlan -RegisterObject $ordinaryProgressRegister
    Assert-SelfTestCondition ($ordinaryProgressResult.verdict -eq 'PASS') ("ordinary same-lane register progress was rejected: " + ($ordinaryProgressResult.errors -join '; '))

    $newRegisterSlice = $registerFixture | ConvertTo-Json -Depth 20 | ConvertFrom-Json -Depth 20
    $newRegisterSlice.criteria = @($newRegisterSlice.criteria) + [pscustomobject][ordered]@{ slice = 'NEW'; status = 'PENDING'; head = ''; notes = '' }
    $newRegisterResult = Invoke-ScopeContractAudit -ScopeMapObject $scopeFixture -ObservedScopeMapSha256 $scopeSha -ExpectedScopeMapSha256 $scopeSha -StateObject $scopeState -PlanText $scopePlan -RegisterObject $newRegisterSlice
    Assert-SelfTestCondition ($newRegisterResult.verdict -eq 'FAIL') 'new live register slice without a refreshed scope map was accepted'

    $scopeHashMismatch = Invoke-ScopeContractAudit -ScopeMapObject $scopeFixture -ObservedScopeMapSha256 ('0' * 64) -ExpectedScopeMapSha256 $scopeSha -StateObject $scopeState -PlanText $scopePlan -RegisterObject $registerFixture
    Assert-SelfTestCondition ($scopeHashMismatch.verdict -eq 'FAIL') 'scope map hash mismatch was accepted'

    Write-Output 'SELFTEST PASS: assert-plan-path-ownership.ps1'
}

function Get-PlanRowNames {
    param([Parameter(Mandatory)][string]$Text)

    $matrixSection = Get-MarkdownSection $Text '^## 4\. Worktree and Ownership Matrix\s*$' '^### 4\.1\s+'
    $matrixRows = @(Get-TableRows $matrixSection 'Slice')
    $names = [System.Collections.Generic.List[string]]::new()
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($row in $matrixRows) {
        if ($row.cells.Count -lt 5) { throw "line $($row.line_number): ownership row has $($row.cells.Count) cells, expected at least 5" }
        $name = $row.cells[0].Trim().Trim('`')
        if ($name -notmatch '^[A-Z0-9][A-Z0-9-]*$') { throw "line $($row.line_number): plan row identity '$name' is not canonical" }
        if (-not $seen.Add($name)) { throw "plan row identity '$name' appears more than once" }
        $names.Add($name)
    }
    return @($names)
}

function Test-StatusContainsPolicyToken {
    param(
        [Parameter(Mandatory)][string]$Status,
        [Parameter(Mandatory)][string]$Token
    )

    if ([string]::IsNullOrWhiteSpace($Status) -or [string]::IsNullOrWhiteSpace($Token)) { return $false }
    return [regex]::IsMatch(
        $Status,
        '(^|_)' + [regex]::Escape($Token) + '(_|$)',
        [System.Text.RegularExpressions.RegexOptions]::IgnoreCase
    )
}

function Test-RegisterStatusAccepted {
    param(
        [Parameter(Mandatory)][string]$Status,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$AcceptanceTokens,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$RejectionTokens
    )

    $accepted = @($AcceptanceTokens | Where-Object { Test-StatusContainsPolicyToken -Status $Status -Token ([string]$_) }).Count -gt 0
    $rejected = @($RejectionTokens | Where-Object { Test-StatusContainsPolicyToken -Status $Status -Token ([string]$_) }).Count -gt 0
    return $accepted -and -not $rejected
}

function Invoke-ScopeContractAudit {
    param(
        [Parameter(Mandatory)]$ScopeMapObject,
        [Parameter(Mandatory)][string]$ObservedScopeMapSha256,
        [Parameter(Mandatory)][string]$ExpectedScopeMapSha256,
        [Parameter(Mandatory)]$StateObject,
        [Parameter(Mandatory)][string]$PlanText,
        [AllowNull()]$RegisterObject = $null
    )

    $errors = [System.Collections.Generic.List[string]]::new()
    $expectedClassifications = @('maker', 'checker-evidence', 'meta-fold', 'historical', 'root-integration')
    $expectedExactFields = @('slice', 'classification', 'plan_owners')
    $expectedSnapshotFields = @('register_snapshot.sha256', 'register_snapshot.updated_at', 'register_status', 'register_head', 'register_notes')
    $expectedScopePath = '.agent/plans/2026-07-10-engram-production-ready-scope-map.json'

    if (-not (Test-ExpectedPlanHash -ObservedSha256 $ObservedScopeMapSha256 -ExpectedSha256 $ExpectedScopeMapSha256)) {
        $errors.Add("observed scope-map SHA256 '$ObservedScopeMapSha256' does not match expected '$ExpectedScopeMapSha256'")
    }

    $stateScope = Get-PropertyValue $StateObject 'scope_map'
    $stateScopePath = [string](Get-PropertyValue $stateScope 'path')
    $stateScopeSha = [string](Get-PropertyValue $stateScope 'sha256')
    if ($stateScopePath -cne $expectedScopePath) { $errors.Add("ownership state scope-map path '$stateScopePath' is not canonical") }
    if (-not (Test-ExpectedPlanHash -ObservedSha256 $stateScopeSha -ExpectedSha256 $ExpectedScopeMapSha256)) {
        $errors.Add("ownership state scope-map SHA256 '$stateScopeSha' does not match expected '$ExpectedScopeMapSha256'")
    }

    if ((Get-PropertyValue $ScopeMapObject 'schema_version') -ne 1) { $errors.Add('scope map schema_version must be 1') }
    if ([string](Get-PropertyValue $ScopeMapObject 'kind') -cne 'production-ready-scope-map') { $errors.Add('scope map kind must be production-ready-scope-map') }
    $scopePlanPath = [string](Get-PropertyValue $ScopeMapObject 'plan_path')
    $scopeStatePath = [string](Get-PropertyValue $ScopeMapObject 'ownership_state_path')
    if ($scopePlanPath -cne '.agent/plans/2026-07-10-engram-production-ready-master-plan.md') { $errors.Add("scope map plan_path '$scopePlanPath' is not canonical") }
    if ($scopeStatePath -cne '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json') { $errors.Add("scope map ownership_state_path '$scopeStatePath' is not canonical") }

    [object[]]$allowedClassifications = @((Get-PropertyValue $ScopeMapObject 'allowed_classifications') | ForEach-Object { [string]$_ })
    if (-not (Test-SameStringSequence $expectedClassifications $allowedClassifications)) { $errors.Add('scope map allowed_classifications drifted') }

    $policy = Get-PropertyValue $ScopeMapObject 'live_conformance_policy'
    if ([string](Get-PropertyValue $policy 'mode') -cne 'structural-projection') { $errors.Add('scope map live policy must use structural-projection mode') }
    [object[]]$exactFields = @((Get-PropertyValue $policy 'exact_fields') | ForEach-Object { [string]$_ })
    [object[]]$snapshotFields = @((Get-PropertyValue $policy 'snapshot_only_fields') | ForEach-Object { [string]$_ })
    [object[]]$acceptanceTokens = @((Get-PropertyValue $policy 'acceptance_tokens') | ForEach-Object { [string]$_ })
    [object[]]$rejectionTokens = @((Get-PropertyValue $policy 'rejection_tokens') | ForEach-Object { [string]$_ })
    if (-not (Test-SameStringSequence $expectedExactFields $exactFields)) { $errors.Add('scope map exact structural fields drifted') }
    if (-not (Test-SameStringSequence $expectedSnapshotFields $snapshotFields)) { $errors.Add('scope map snapshot-only fields drifted') }
    if ([string](Get-PropertyValue $policy 'load_bearing_entry_field') -cne 'load_bearing') { $errors.Add('scope map load-bearing field name drifted') }
    if ($acceptanceTokens.Count -lt 1 -or @($acceptanceTokens | Select-Object -Unique).Count -ne $acceptanceTokens.Count) { $errors.Add('scope map acceptance tokens are empty or duplicated') }
    if ($rejectionTokens.Count -lt 1 -or @($rejectionTokens | Select-Object -Unique).Count -ne $rejectionTokens.Count) { $errors.Add('scope map rejection tokens are empty or duplicated') }

    $snapshot = Get-PropertyValue $ScopeMapObject 'register_snapshot'
    $snapshotSha = [string](Get-PropertyValue $snapshot 'sha256')
    $snapshotUpdatedAt = [string](Get-PropertyValue $snapshot 'updated_at')
    $snapshotRowCount = [int](Get-PropertyValue $snapshot 'row_count')
    $snapshotUniqueCount = [int](Get-PropertyValue $snapshot 'unique_slice_count')
    if ($snapshotSha -notmatch '^[0-9A-Fa-f]{64}$') { $errors.Add('scope map register freeze SHA256 is missing or invalid') }
    if ([string]::IsNullOrWhiteSpace($snapshotUpdatedAt)) { $errors.Add('scope map register freeze updated_at is missing') }
    if ($snapshotRowCount -lt 1 -or $snapshotUniqueCount -lt 1 -or $snapshotRowCount -ne $snapshotUniqueCount) { $errors.Add('scope map register freeze row/unique counts are invalid') }

    $planRows = @()
    try { $planRows = @(Get-PlanRowNames -Text $PlanText) }
    catch { $errors.Add($_.Exception.Message) }
    $planRowSet = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($planRow in $planRows) { [void]$planRowSet.Add([string]$planRow) }

    [object[]]$entries = @((Get-PropertyValue $ScopeMapObject 'entries'))
    $entryBySlice = @{}
    foreach ($entry in $entries) {
        $slice = [string](Get-PropertyValue $entry 'slice')
        $classification = [string](Get-PropertyValue $entry 'classification')
        [object[]]$owners = @((Get-PropertyValue $entry 'plan_owners') | ForEach-Object { [string]$_ })
        if ($slice -notmatch '^[A-Z0-9][A-Z0-9-]*$') { $errors.Add("scope entry slice '$slice' is blank or non-canonical") }
        elseif ($entryBySlice.ContainsKey($slice)) { $errors.Add("scope map repeats slice '$slice'") }
        else { $entryBySlice[$slice] = $entry }
        if ($classification -notin $expectedClassifications) { $errors.Add("scope entry '$slice' has unsupported classification '$classification'") }
        if ($owners.Count -lt 1 -or @($owners | Select-Object -Unique).Count -ne $owners.Count) { $errors.Add("scope entry '$slice' has empty or duplicate plan owners") }
        if ($classification -in @('maker', 'checker-evidence', 'root-integration') -and ($owners.Count -ne 1 -or $owners[0] -cne $slice)) {
            $errors.Add("scope entry '$slice' classification '$classification' must map directly to its same-named plan row")
        }
        if ($classification -in @('meta-fold', 'historical') -and $planRowSet.Contains($slice)) {
            $errors.Add("scope entry '$slice' classification '$classification' must not masquerade as a direct plan row")
        }
        foreach ($owner in $owners) {
            if (-not $planRowSet.Contains($owner)) { $errors.Add("scope entry '$slice' points to missing plan owner '$owner'") }
        }

        $statusProperty = $entry.PSObject.Properties['register_status']
        $headProperty = $entry.PSObject.Properties['register_head']
        if ($null -eq $statusProperty -or $null -eq $headProperty) { $errors.Add("scope entry '$slice' omits frozen register status/head facts") }
        $snapshotEntryHead = [string](Get-PropertyValue $entry 'register_head')
        if (-not [string]::IsNullOrWhiteSpace($snapshotEntryHead) -and $snapshotEntryHead -notmatch '^[0-9A-Fa-f]{40}$') { $errors.Add("scope entry '$slice' has invalid frozen register head '$snapshotEntryHead'") }

        $loadBearing = Get-PropertyValue $entry 'load_bearing'
        if ($null -ne $loadBearing) {
            if ([string](Get-PropertyValue $loadBearing 'policy') -cne 'rejected_heads_must_not_be_accepted') { $errors.Add("scope entry '$slice' has unsupported load-bearing policy") }
            [object[]]$rejectedHeads = @((Get-PropertyValue $loadBearing 'rejected_heads') | ForEach-Object { [string]$_ })
            if ($rejectedHeads.Count -lt 1 -or @($rejectedHeads | Select-Object -Unique).Count -ne $rejectedHeads.Count -or @($rejectedHeads | Where-Object { $_ -notmatch '^[0-9A-Fa-f]{40}$' }).Count -gt 0) {
                $errors.Add("scope entry '$slice' has invalid rejected-head policy data")
            }
        }
    }
    if ($entries.Count -ne $snapshotRowCount -or $entryBySlice.Count -ne $snapshotUniqueCount) {
        $errors.Add("scope map entry cardinality $($entries.Count)/$($entryBySlice.Count) does not match frozen $snapshotRowCount/$snapshotUniqueCount")
    }

    $liveRows = @()
    if ($null -ne $RegisterObject) {
        [object[]]$liveRows = @((Get-PropertyValue $RegisterObject 'criteria'))
        $liveBySlice = @{}
        foreach ($row in $liveRows) {
            $slice = [string](Get-PropertyValue $row 'slice')
            if ($slice -notmatch '^[A-Z0-9][A-Z0-9-]*$') { $errors.Add("live register slice '$slice' is blank or non-canonical"); continue }
            if ($liveBySlice.ContainsKey($slice)) { $errors.Add("live register repeats slice '$slice'"); continue }
            $liveBySlice[$slice] = $row
        }
        if ($liveRows.Count -ne $liveBySlice.Count) { $errors.Add("live register row/unique counts differ: $($liveRows.Count)/$($liveBySlice.Count)") }
        if (-not (Test-SameStringSet @($entryBySlice.Keys) @($liveBySlice.Keys))) { $errors.Add('live register unique slice set differs from the frozen scope map') }

        foreach ($slice in $entryBySlice.Keys) {
            if (-not $liveBySlice.ContainsKey($slice)) { continue }
            $entry = $entryBySlice[$slice]
            $loadBearing = Get-PropertyValue $entry 'load_bearing'
            if ($null -eq $loadBearing) { continue }
            $liveRow = $liveBySlice[$slice]
            $liveHead = [string](Get-PropertyValue $liveRow 'head')
            $liveStatus = [string](Get-PropertyValue $liveRow 'status')
            [object[]]$rejectedHeads = @((Get-PropertyValue $loadBearing 'rejected_heads') | ForEach-Object { [string]$_ })
            if ($liveHead -in $rejectedHeads -and (Test-RegisterStatusAccepted -Status $liveStatus -AcceptanceTokens $acceptanceTokens -RejectionTokens $rejectionTokens)) {
                $errors.Add("live register slice '$slice' presents rejected head '$liveHead' as accepted with status '$liveStatus'")
            }
        }
    }

    return [pscustomobject][ordered]@{
        schema_version = 1
        verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        observed_sha256 = $ObservedScopeMapSha256
        expected_sha256 = $ExpectedScopeMapSha256
        snapshot_sha256 = $snapshotSha
        snapshot_updated_at = $snapshotUpdatedAt
        entries = $entries.Count
        unique_slices = $entryBySlice.Count
        plan_rows = $planRows.Count
        live_register_checked = $null -ne $RegisterObject
        live_rows = $liveRows.Count
        errors = @($errors)
    }
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }
if ($PrintCanonicalPlanSha256) {
    if (-not (Test-Path -LiteralPath $Plan -PathType Leaf)) { throw "ownership plan does not exist: $Plan" }
    Write-Output (Get-CanonicalUtf8LfFileSha256 -Path $Plan)
    exit 0
}

$startedAt = [DateTimeOffset]::UtcNow
$planHash = $null
$planPath = if (Test-Path -LiteralPath $Plan) { [System.IO.Path]::GetFullPath($Plan) } else { $Plan }
$stateHash = $null
$statePath = if (Test-Path -LiteralPath $State) { [System.IO.Path]::GetFullPath($State) } else { $State }
$stateObject = $null
$stateAudit = $null
$scopeMapHash = $null
$scopeMapPath = if (Test-Path -LiteralPath $ScopeMap) { [System.IO.Path]::GetFullPath($ScopeMap) } else { $ScopeMap }
$scopeMapObject = $null
$scopeAudit = $null
$registerHash = $null
$registerPath = if (-not [string]::IsNullOrWhiteSpace($Register) -and (Test-Path -LiteralPath $Register)) { [System.IO.Path]::GetFullPath($Register) } else { $Register }
$registerObject = $null
$artifactObject = $null
$exitCode = 1

try {
    if (-not (Test-Path -LiteralPath $Plan -PathType Leaf)) { throw "ownership plan does not exist: $Plan" }
    if ([string]::IsNullOrWhiteSpace($ExpectedPlanSha256) -or $ExpectedPlanSha256 -notmatch '^[0-9A-Fa-f]{64}$') { throw '-ExpectedPlanSha256 is required and must be a full 64-hex SHA256' }
    if (-not (Test-Path -LiteralPath $State -PathType Leaf)) { throw "ownership state does not exist: $State" }
    if (-not (Test-Path -LiteralPath $ScopeMap -PathType Leaf)) { throw "scope map does not exist: $ScopeMap" }
    if ([string]::IsNullOrWhiteSpace($ExpectedScopeMapSha256) -or $ExpectedScopeMapSha256 -notmatch '^[0-9A-Fa-f]{64}$') { throw '-ExpectedScopeMapSha256 is required and must be a full 64-hex SHA256' }
    if (-not [string]::IsNullOrWhiteSpace($Register) -and -not (Test-Path -LiteralPath $Register -PathType Leaf)) { throw "live register does not exist: $Register" }
    $planHash = Get-CanonicalUtf8LfFileSha256 -Path $Plan
    $stateHash = Get-CanonicalUtf8LfFileSha256 -Path $State
    $scopeMapHash = Get-CanonicalUtf8LfFileSha256 -Path $ScopeMap
    $text = [System.IO.File]::ReadAllText([System.IO.Path]::GetFullPath($Plan))
    $ledger = Invoke-OwnershipAudit $text $planPath
    try { $stateObject = Get-Content -LiteralPath $State -Raw | ConvertFrom-Json -Depth 100 }
    catch { throw "ownership state is invalid JSON: $($_.Exception.Message)" }
    try { $scopeMapObject = Get-Content -LiteralPath $ScopeMap -Raw | ConvertFrom-Json -Depth 100 }
    catch { throw "scope map is invalid JSON: $($_.Exception.Message)" }
    if (-not [string]::IsNullOrWhiteSpace($Register)) {
        $registerHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $Register).Hash.ToLowerInvariant()
        try { $registerObject = Get-Content -LiteralPath $Register -Raw | ConvertFrom-Json -Depth 100 }
        catch { throw "live register is invalid JSON: $($_.Exception.Message)" }
    }
    $stateAudit = Invoke-StateContractAudit -StateObject $stateObject -Ledger $ledger -ObservedPlanSha256 $planHash -ExpectedPlanSha256 $ExpectedPlanSha256
    $scopeAudit = Invoke-ScopeContractAudit -ScopeMapObject $scopeMapObject -ObservedScopeMapSha256 $scopeMapHash -ExpectedScopeMapSha256 $ExpectedScopeMapSha256 -StateObject $stateObject -PlanText $text -RegisterObject $registerObject

    if ($Mode -eq 'Ledger') {
        $authorityErrors = @($ledger.errors) + @($stateAudit.errors) + @($scopeAudit.errors)
        $finishedAt = [DateTimeOffset]::UtcNow
        $artifactObject = [ordered]@{
            schema_version = 2
            gate = 'plan-path-ownership'
            mode = 'Ledger'
            verdict = if ($authorityErrors.Count -eq 0) { 'PASS' } else { 'FAIL' }
            started_at = $startedAt.ToString('O')
            finished_at = $finishedAt.ToString('O')
            duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
            plan = [ordered]@{ path = $planPath; expected_sha256 = $ExpectedPlanSha256.ToLowerInvariant(); observed_sha256 = $planHash; hash_match = (Test-ExpectedPlanHash $planHash $ExpectedPlanSha256) }
            state = [ordered]@{ path = $statePath; sha256 = $stateHash; verdict = $stateAudit.verdict; plan_sha256 = $stateAudit.plan_sha256 }
            scope_map = [ordered]@{ path = $scopeMapPath; expected_sha256 = $ExpectedScopeMapSha256.ToLowerInvariant(); observed_sha256 = $scopeMapHash; verdict = $scopeAudit.verdict; entries = $scopeAudit.entries; unique_slices = $scopeAudit.unique_slices }
            live_register = [ordered]@{ supplied = -not [string]::IsNullOrWhiteSpace($Register); path = $registerPath; sha256 = $registerHash; checked = $scopeAudit.live_register_checked; rows = $scopeAudit.live_rows }
            counts = [ordered]@{
                maker_slices = $ledger.counts.maker_slices
                declarations = $ledger.counts.declarations
                exact_paths = $ledger.counts.exact_paths
                prefixes = $ledger.counts.prefixes
                repeated_exact_paths = $ledger.counts.repeated_exact_paths
                prefix_intersections = $ledger.counts.prefix_intersections
                undeclared_prefix_intersections = $ledger.counts.undeclared_prefix_intersections
                declared_epochs = $ledger.counts.declared_epochs
                state_epochs = @($stateAudit.path_epochs).Count
                errors = $authorityErrors.Count
            }
            slices = $ledger.slices
            declarations = $ledger.declarations
            repeated_exact_paths = $ledger.repeated_exact_paths
            prefix_intersections = $ledger.prefix_intersections
            epochs = $ledger.epochs
            errors = $authorityErrors
        }
        $exitCode = if ($authorityErrors.Count -eq 0) { 0 } else { 1 }
    }
    else {
        $errors = [System.Collections.Generic.List[string]]::new()
        foreach ($ledgerError in $ledger.errors) { $errors.Add("ledger: $ledgerError") }
        foreach ($stateError in $stateAudit.errors) { $errors.Add("state: $stateError") }
        foreach ($scopeError in $scopeAudit.errors) { $errors.Add("scope: $scopeError") }
        if ([string]::IsNullOrWhiteSpace($Slice)) { $errors.Add('Diff mode requires -Slice') }
        if ([string]::IsNullOrWhiteSpace($Base)) { $errors.Add('Diff mode requires -Base') }
        if ([string]::IsNullOrWhiteSpace($Head)) { $errors.Add('Diff mode requires -Head') }
        if ([string]::IsNullOrWhiteSpace($EvidenceNamespace)) { $errors.Add('Diff mode requires -EvidenceNamespace') }
        if ([string]::IsNullOrWhiteSpace($ReportNamespace)) { $errors.Add('Diff mode requires -ReportNamespace') }

        [object[]]$sliceRows = @(
            if (-not [string]::IsNullOrWhiteSpace($Slice)) {
                $ledger.slices | Where-Object slice -ceq $Slice
            }
        )
        if ($sliceRows.Count -ne 1) {
            $errors.Add("Diff mode requires exactly one maker row for slice '$Slice'; found $($sliceRows.Count)")
        }
        [object[]]$sliceDeclarations = @(
            if ($sliceRows.Count -eq 1) {
                $ledger.declarations | Where-Object owner -ceq $Slice
            }
        )

        $evidence = $null
        $report = $null
        if ($sliceRows.Count -eq 1 -and -not [string]::IsNullOrWhiteSpace($EvidenceNamespace)) {
            try { $evidence = Resolve-DiffNamespace -Kind evidence -Token $EvidenceNamespace -SliceName $Slice -SliceDeclarations $sliceDeclarations }
            catch { $errors.Add($_.Exception.Message) }
        }
        if ($sliceRows.Count -eq 1 -and -not [string]::IsNullOrWhiteSpace($ReportNamespace)) {
            try { $report = Resolve-DiffNamespace -Kind report -Token $ReportNamespace -SliceName $Slice -SliceDeclarations $sliceDeclarations }
            catch { $errors.Add($_.Exception.Message) }
        }

        $repoRoot = $null
        $baseResolved = $null
        $headResolved = $null
        $baseIsAncestor = $null
        $rawDiff = @()
        $parsedDiff = [pscustomobject][ordered]@{ entries = @(); errors = @() }
        try {
            $rootOutput = @(& git rev-parse --show-toplevel 2>&1)
            if ($LASTEXITCODE -ne 0) { throw "cannot resolve git repository root: $($rootOutput -join ' ')" }
            $repoRoot = [System.IO.Path]::GetFullPath(([string]$rootOutput[-1]).Trim())
            if (-not [string]::IsNullOrWhiteSpace($Base)) { $baseResolved = Resolve-ExactCommit $repoRoot $Base 'Base' }
            if (-not [string]::IsNullOrWhiteSpace($Head)) { $headResolved = Resolve-ExactCommit $repoRoot $Head 'Head' }
            if ($baseResolved -and $headResolved) {
                if ($baseResolved -ceq $headResolved) { $errors.Add('Base and Head resolve to the same commit; maker diff is empty') }
                & git -C $repoRoot merge-base --is-ancestor $baseResolved $headResolved 2>$null
                $ancestorExit = $LASTEXITCODE
                $baseIsAncestor = $ancestorExit -eq 0
                if ($ancestorExit -eq 1) { $errors.Add("Base '$baseResolved' is not an ancestor of Head '$headResolved'") }
                elseif ($ancestorExit -ne 0) { $errors.Add("git merge-base --is-ancestor failed with exit $ancestorExit") }
                $rawDiff = @(& git -C $repoRoot -c core.quotepath=false diff --name-status --find-renames --find-copies "$baseResolved..$headResolved" -- 2>&1)
                if ($LASTEXITCODE -ne 0) { throw "git diff failed: $($rawDiff -join ' ')" }
                $parsedDiff = ConvertFrom-GitNameStatusLines $rawDiff
                foreach ($parseError in $parsedDiff.errors) { $errors.Add($parseError) }
                if ($parsedDiff.entries.Count -eq 0) { $errors.Add('maker diff contains zero changed paths') }
            }
        }
        catch { $errors.Add($_.Exception.Message) }

        $diffAudit = [pscustomobject][ordered]@{ changed_paths = @(); violations = @() }
        if ($evidence -and $report -and $parsedDiff.errors.Count -eq 0) {
            $diffAudit = Invoke-DiffEntryAudit $parsedDiff.entries $sliceDeclarations $evidence $report
            foreach ($violation in $diffAudit.violations) {
                $errors.Add("undeclared diff path [$($violation.status)] '$($violation.path)'")
            }
        }

        $epochAuthority = [pscustomobject][ordered]@{ verdict = 'FAIL'; evaluated = @(); errors = @('epoch authority was not evaluated') }
        if ($repoRoot -and $baseResolved -and $stateObject -and $parsedDiff.errors.Count -eq 0) {
            $epochAuthority = Invoke-DiffEpochAuthority -Slice $Slice -ChangedPaths @($diffAudit.changed_paths | ForEach-Object path) -State $stateObject -Repository $repoRoot -BaseResolved $baseResolved
            foreach ($epochError in $epochAuthority.errors) { $errors.Add("epoch: $epochError") }
        }

        $finishedAt = [DateTimeOffset]::UtcNow
        $verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        $artifactObject = [ordered]@{
            schema_version = 2
            gate = 'plan-path-ownership'
            mode = 'Diff'
            verdict = $verdict
            started_at = $startedAt.ToString('O')
            finished_at = $finishedAt.ToString('O')
            duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
            plan = [ordered]@{ path = $planPath; expected_sha256 = $ExpectedPlanSha256.ToLowerInvariant(); observed_sha256 = $planHash; hash_match = (Test-ExpectedPlanHash $planHash $ExpectedPlanSha256); ledger_verdict = $ledger.verdict }
            state = [ordered]@{ path = $statePath; sha256 = $stateHash; verdict = $stateAudit.verdict; plan_sha256 = $stateAudit.plan_sha256 }
            scope_map = [ordered]@{ path = $scopeMapPath; expected_sha256 = $ExpectedScopeMapSha256.ToLowerInvariant(); observed_sha256 = $scopeMapHash; verdict = $scopeAudit.verdict; entries = $scopeAudit.entries; unique_slices = $scopeAudit.unique_slices }
            live_register = [ordered]@{ supplied = -not [string]::IsNullOrWhiteSpace($Register); path = $registerPath; sha256 = $registerHash; checked = $scopeAudit.live_register_checked; rows = $scopeAudit.live_rows }
            slice = [ordered]@{
                name = $Slice
                row_count = $sliceRows.Count
                declarations = $sliceDeclarations
                evidence_namespace = $evidence
                report_namespace = $report
            }
            git = [ordered]@{
                repository = $repoRoot
                requested_base = $Base
                resolved_base = $baseResolved
                requested_head = $Head
                resolved_head = $headResolved
                base_is_ancestor = $baseIsAncestor
                name_status_command = if ($baseResolved -and $headResolved) { "git -c core.quotepath=false diff --name-status --find-renames --find-copies $baseResolved..$headResolved --" } else { $null }
                raw_name_status = @($rawDiff)
            }
            counts = [ordered]@{
                diff_entries = @($parsedDiff.entries).Count
                changed_paths = @($diffAudit.changed_paths).Count
                violations = @($diffAudit.violations).Count
                errors = $errors.Count
            }
            diff_entries = @($parsedDiff.entries)
            changed_paths = @($diffAudit.changed_paths)
            violations = @($diffAudit.violations)
            epoch_authority = $epochAuthority
            errors = @($errors)
        }
        $exitCode = if ($verdict -eq 'PASS') { 0 } else { 1 }
    }
}
catch {
    $finishedAt = [DateTimeOffset]::UtcNow
    $artifactObject = [ordered]@{
        schema_version = 2
        gate = 'plan-path-ownership'
        mode = $Mode
        verdict = 'FAIL'
        started_at = $startedAt.ToString('O')
        finished_at = $finishedAt.ToString('O')
        duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
        plan = [ordered]@{ path = $planPath; expected_sha256 = $ExpectedPlanSha256; observed_sha256 = $planHash }
        state = [ordered]@{ path = $statePath; sha256 = $stateHash }
        scope_map = [ordered]@{ path = $scopeMapPath; expected_sha256 = $ExpectedScopeMapSha256; observed_sha256 = $scopeMapHash }
        live_register = [ordered]@{ supplied = -not [string]::IsNullOrWhiteSpace($Register); path = $registerPath; sha256 = $registerHash }
        errors = @($_.Exception.Message)
    }
    $exitCode = 1
}

Write-Utf8NoBom $Artifact (($artifactObject | ConvertTo-Json -Depth 30) + "`n")
if ($Mode -eq 'Ledger' -and $artifactObject.Contains('counts')) {
    Write-Host ("plan-path-ownership mode=Ledger verdict={0} slices={1} declarations={2} repeated_exact={3} prefix_intersections={4} errors={5}" -f $artifactObject.verdict, $artifactObject.counts.maker_slices, $artifactObject.counts.declarations, $artifactObject.counts.repeated_exact_paths, $artifactObject.counts.prefix_intersections, $artifactObject.counts.errors)
}
elseif ($Mode -eq 'Diff' -and $artifactObject.Contains('counts')) {
    Write-Host ("plan-path-ownership mode=Diff verdict={0} slice={1} changed_paths={2} violations={3} errors={4}" -f $artifactObject.verdict, $Slice, $artifactObject.counts.changed_paths, $artifactObject.counts.violations, $artifactObject.counts.errors)
}
else {
    Write-Host ("plan-path-ownership mode={0} verdict=FAIL" -f $Mode)
}
exit $exitCode
