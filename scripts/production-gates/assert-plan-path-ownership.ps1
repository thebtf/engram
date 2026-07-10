[CmdletBinding()]
param(
    [ValidateSet('Ledger', 'Diff')]
    [string]$Mode = 'Ledger',
    [string]$Slice,
    [string]$Base,
    [string]$Head,
    [string]$Plan = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md',
    [string]$EvidenceNamespace,
    [string]$ReportNamespace,
    [string]$Artifact = '.agent/reports/evidence/production-ready/ownership/path-ledger.json',
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
owner set exactly matches the effective owners. Prefix/prefix overlap always
fails.

Diff mode additionally enumerates git diff --name-status Base..Head and proves
that every changed path belongs to the named slice or to its validated evidence
or maker-report namespace. Base and Head must be full commit object IDs.

Usage:
  pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 -Mode Ledger `
    -Plan .agent/plans/2026-07-10-engram-production-ready-master-plan.md `
    -Artifact .agent/reports/evidence/production-ready/ownership/path-ledger.json

  pwsh ./scripts/production-gates/assert-plan-path-ownership.ps1 -Mode Diff `
    -Slice DB-BULKOPS -Base <40-hex-commit> -Head <40-hex-commit> `
    -EvidenceNamespace '.agent/specs/production-ready-db-bulkops/evidence/**' `
    -ReportNamespace .agent/reports/db-bulkops-maker.md -Plan <plan> -Artifact <json>
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
            foreach ($owner in ($next -split '\s*->\s*')) {
                $trimmedOwner = $owner.Trim()
                if (-not [string]::IsNullOrWhiteSpace($trimmedOwner)) { $chain.Add($trimmedOwner) }
            }
            if ($chain.Count -lt 2) {
                $errors.Add("line $($row.line_number): epoch chain must contain at least two owners")
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
                    $errors.Add("declared epoch '$path' is not an actual repeated exact or exact/prefix path")
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
            if (-not (Test-SameStringSet $effectiveOwners $epochOwners)) {
                $errors.Add("epoch '$path' owner set differs: effective=$($effectiveOwners -join ', '), epoch=$($epochOwners -join ' -> ')")
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

function Invoke-SelfTest {
    $reorderedEpoch = New-SyntheticPlan -Rows "| A | ``work/a`` | ``src/shared.go`` | none | proof |`n| B | ``work/b`` | ``src/shared.go`` | A integrated | proof |" -EpochRows '| `src/shared.go` | B | A | B checker and post-review PASS, commit integrated, A rebased |'
    $reorderedResult = Invoke-OwnershipAudit $reorderedEpoch 'selftest-reordered-epoch'
    Assert-SelfTestCondition ($reorderedResult.verdict -eq 'PASS') ("epoch owner set fixture failed: " + ($reorderedResult.errors -join '; '))

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

    Write-Output 'SELFTEST PASS: assert-plan-path-ownership.ps1'
}

if ($Help) { Show-Help; exit 0 }
if ($SelfTest) { Invoke-SelfTest; exit 0 }

$startedAt = [DateTimeOffset]::UtcNow
$planHash = $null
$planPath = if (Test-Path -LiteralPath $Plan) { [System.IO.Path]::GetFullPath($Plan) } else { $Plan }
$artifactObject = $null
$exitCode = 1

try {
    if (-not (Test-Path -LiteralPath $Plan -PathType Leaf)) { throw "ownership plan does not exist: $Plan" }
    $planHash = (Get-FileHash -LiteralPath $Plan -Algorithm SHA256).Hash.ToLowerInvariant()
    $text = Get-Content -LiteralPath $Plan -Raw
    $ledger = Invoke-OwnershipAudit $text $planPath

    if ($Mode -eq 'Ledger') {
        $finishedAt = [DateTimeOffset]::UtcNow
        $artifactObject = [ordered]@{
            schema_version = 2
            gate = 'plan-path-ownership'
            mode = 'Ledger'
            verdict = $ledger.verdict
            started_at = $startedAt.ToString('O')
            finished_at = $finishedAt.ToString('O')
            duration_seconds = [math]::Round(($finishedAt - $startedAt).TotalSeconds, 3)
            plan = [ordered]@{ path = $planPath; sha256 = $planHash }
            counts = $ledger.counts
            slices = $ledger.slices
            declarations = $ledger.declarations
            repeated_exact_paths = $ledger.repeated_exact_paths
            prefix_intersections = $ledger.prefix_intersections
            epochs = $ledger.epochs
            errors = $ledger.errors
        }
        $exitCode = if ($ledger.verdict -eq 'PASS') { 0 } else { 1 }
    }
    else {
        $errors = [System.Collections.Generic.List[string]]::new()
        foreach ($ledgerError in $ledger.errors) { $errors.Add("ledger: $ledgerError") }
        if ([string]::IsNullOrWhiteSpace($Slice)) { $errors.Add('Diff mode requires -Slice') }
        if ([string]::IsNullOrWhiteSpace($Base)) { $errors.Add('Diff mode requires -Base') }
        if ([string]::IsNullOrWhiteSpace($Head)) { $errors.Add('Diff mode requires -Head') }
        if ([string]::IsNullOrWhiteSpace($EvidenceNamespace)) { $errors.Add('Diff mode requires -EvidenceNamespace') }
        if ([string]::IsNullOrWhiteSpace($ReportNamespace)) { $errors.Add('Diff mode requires -ReportNamespace') }

        $sliceRows = if ([string]::IsNullOrWhiteSpace($Slice)) { @() } else { @($ledger.slices | Where-Object slice -ceq $Slice) }
        if ($sliceRows.Count -ne 1) {
            $errors.Add("Diff mode requires exactly one maker row for slice '$Slice'; found $($sliceRows.Count)")
        }
        $sliceDeclarations = if ($sliceRows.Count -eq 1) {
            @($ledger.declarations | Where-Object owner -ceq $Slice)
        }
        else { @() }

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
            plan = [ordered]@{ path = $planPath; sha256 = $planHash; ledger_verdict = $ledger.verdict }
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
        plan = [ordered]@{ path = $planPath; sha256 = $planHash }
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
