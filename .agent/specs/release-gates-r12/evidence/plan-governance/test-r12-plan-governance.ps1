[CmdletBinding()]
param(
    [string]$Plan = '.agent/plans/2026-07-10-engram-production-ready-master-plan.md',
    [string]$OwnershipState = '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json',
    [string]$ScopeMap = '.agent/plans/2026-07-10-engram-production-ready-scope-map.json',
    [string]$ActiveContracts = '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json',
    [string]$PathEnvelope = '.agent/specs/release-gates-r12/evidence/plan-governance/path-envelope.json',
    [string]$FixedPointProof = '.agent/specs/release-gates-r12/evidence/plan-governance/fixed-point-proof.json',
    [string]$AuthoritySnapshot = '.agent/specs/release-gates-r12/evidence/plan-governance/authority-snapshot.json'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Test-NoDuplicateJsonElement {
    param([System.Text.Json.JsonElement]$Element)

    if ($Element.ValueKind -eq [System.Text.Json.JsonValueKind]::Object) {
        $names = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
        foreach ($property in $Element.EnumerateObject()) {
            if (-not $names.Add($property.Name) -or -not (Test-NoDuplicateJsonElement $property.Value)) { return $false }
        }
    } elseif ($Element.ValueKind -eq [System.Text.Json.JsonValueKind]::Array) {
        foreach ($item in $Element.EnumerateArray()) {
            if (-not (Test-NoDuplicateJsonElement $item)) { return $false }
        }
    }
    return $true
}

function Test-NoDuplicateJsonProperties {
    param([string]$Text)

    try {
        $document = [System.Text.Json.JsonDocument]::Parse($Text)
    } catch {
        return $false
    }
    try {
        return Test-NoDuplicateJsonElement $document.RootElement
    } finally {
        $document.Dispose()
    }
}

function Read-Json {
    param([string]$Path)
    Assert-True (Test-Path -LiteralPath $Path -PathType Leaf) "required JSON file is missing: $Path"
    $text = Get-Content -LiteralPath $Path -Raw
    Assert-True (Test-NoDuplicateJsonProperties $text) "JSON contains invalid or duplicate property names: $Path"
    return $text | ConvertFrom-Json -Depth 100
}

function Copy-Json {
    param($Value)
    return $Value | ConvertTo-Json -Depth 100 | ConvertFrom-Json -Depth 100
}

function Test-HasProperties {
    param($Value, [string[]]$Names)
    if ($null -eq $Value) { return $false }
    foreach ($name in $Names) {
        if ($null -eq $Value.PSObject.Properties[$name]) { return $false }
    }
    return $true
}

function Test-JsonString {
    param($Value, [bool]$AllowEmpty = $false)
    return ($Value -is [string] -and ($AllowEmpty -or $Value.Length -gt 0))
}

function Test-JsonBoolean {
    param($Value)
    return ($Value -is [bool])
}

function Test-JsonInteger {
    param($Value)
    return ($Value -is [int] -or $Value -is [long])
}

function Test-JsonArray {
    param($Value)
    return ($Value -is [System.Array])
}

function Test-ExactJsonValue {
    param($Actual, $Expected)

    if ($null -eq $Actual -or $null -eq $Expected) { return ($null -eq $Actual -and $null -eq $Expected) }

    $actualIsArray = $Actual -is [System.Array]
    $expectedIsArray = $Expected -is [System.Array]
    if ($actualIsArray -or $expectedIsArray) {
        if (-not ($actualIsArray -and $expectedIsArray)) { return $false }
        $actualItems = @($Actual)
        $expectedItems = @($Expected)
        if ($actualItems.Count -ne $expectedItems.Count) { return $false }
        for ($index = 0; $index -lt $expectedItems.Count; $index++) {
            if (-not (Test-ExactJsonValue $actualItems[$index] $expectedItems[$index])) { return $false }
        }
        return $true
    }

    $actualIsObject = $Actual -is [System.Management.Automation.PSCustomObject]
    $expectedIsObject = $Expected -is [System.Management.Automation.PSCustomObject]
    if ($actualIsObject -or $expectedIsObject) {
        if (-not ($actualIsObject -and $expectedIsObject)) { return $false }
        $actualProperties = @($Actual.PSObject.Properties)
        $expectedProperties = @($Expected.PSObject.Properties)
        if ($actualProperties.Count -ne $expectedProperties.Count) { return $false }
        foreach ($expectedProperty in $expectedProperties) {
            $matches = @($actualProperties | Where-Object { $_.Name -ceq $expectedProperty.Name })
            if ($matches.Count -ne 1 -or -not (Test-ExactJsonValue $matches[0].Value $expectedProperty.Value)) { return $false }
        }
        return $true
    }

    if ($Expected -is [bool]) { return ($Actual -is [bool] -and $Actual -eq $Expected) }
    if ($Expected -is [string]) { return ($Actual -is [string] -and $Actual -ceq $Expected) }
    if ($Expected -is [int] -or $Expected -is [long]) {
        return (($Actual -is [int] -or $Actual -is [long]) -and [int64]$Actual -eq [int64]$Expected)
    }
    return ($Actual.GetType() -eq $Expected.GetType() -and [object]::Equals($Actual, $Expected))
}

function Test-StrictAuthoritySchema {
    param($Contracts, $ImmutableContracts)

    if (-not (Test-HasProperties $Contracts @('authority','external_enforcement','control_plane_maintenance','macro_batches')) -or
        -not (Test-HasProperties $ImmutableContracts @('authority','external_enforcement','control_plane_maintenance','macro_batches'))) { return $false }
    foreach ($surface in @('authority','external_enforcement','control_plane_maintenance','macro_batches')) {
        if (-not (Test-ExactJsonValue $Contracts.$surface $ImmutableContracts.$surface)) { return $false }
    }
    return $true
}

function Get-HostileScalar {
    param($Value)
    if ($null -eq $Value) { return '__unexpected_non_null__' }
    if ($Value -is [bool]) { return -not $Value }
    if ($Value -is [string]) { return "$Value#drift" }
    if ($Value -is [int] -or $Value -is [long]) { return [int64]$Value + 1 }
    if ($Value -is [datetime]) { return $Value.AddSeconds(1) }
    throw "unsupported canonical scalar type in closed-world selftest: $($Value.GetType().FullName)"
}

function Assert-ClosedWorldComparerSelfTest {
    param($Expected, [string]$Path)

    Assert-True (Test-ExactJsonValue $Expected $Expected) "closed-world baseline rejected at $Path"
    if ($Expected -is [System.Array]) {
        $items = @($Expected)
        if ($items.Count -gt 0) {
            Assert-True (-not (Test-ExactJsonValue (@($items) + @($items[0])) $Expected)) "duplicate array item accepted at $Path"
            Assert-True (-not (Test-ExactJsonValue @($items | Select-Object -Skip 1) $Expected)) "missing array item accepted at $Path"
            if ($items.Count -gt 1 -and -not (Test-ExactJsonValue $items[0] $items[1])) {
                $permuted = @($items)
                $permuted[0], $permuted[1] = $permuted[1], $permuted[0]
                Assert-True (-not (Test-ExactJsonValue $permuted $Expected)) "reordered array accepted at $Path"
            }
        }
        for ($index = 0; $index -lt $items.Count; $index++) {
            Assert-ClosedWorldComparerSelfTest $items[$index] "$Path[$index]"
        }
        return
    }
    if ($Expected -is [System.Management.Automation.PSCustomObject]) {
        $unknown = Copy-Json $Expected
        $unknown | Add-Member -NotePropertyName '__closed_world_unknown__' -NotePropertyValue $true
        Assert-True (-not (Test-ExactJsonValue $unknown $Expected)) "unknown property accepted at $Path"
        foreach ($property in @($Expected.PSObject.Properties)) {
            $missing = Copy-Json $Expected
            $missing.PSObject.Properties.Remove($property.Name)
            Assert-True (-not (Test-ExactJsonValue $missing $Expected)) "missing property accepted at $Path.$($property.Name)"
            Assert-ClosedWorldComparerSelfTest $property.Value "$Path.$($property.Name)"
        }
        return
    }
    Assert-True (-not (Test-ExactJsonValue (Get-HostileScalar $Expected) $Expected)) "scalar drift accepted at $Path"
}

function Test-StringArray {
    param($Value)
    if (-not (Test-JsonArray $Value)) { return $false }
    return (@($Value | Where-Object { -not (Test-JsonString $_) }).Count -eq 0)
}

function Get-CanonicalTextSha256 {
    param([string]$Path)
    $resolved = (Resolve-Path -LiteralPath $Path).Path
    $text = [System.IO.File]::ReadAllText($resolved)
    $canonical = $text.Replace("`r`n", "`n").Replace("`r", "`n")
    $bytes = [System.Text.UTF8Encoding]::new($false).GetBytes($canonical)
    $hash = [System.Security.Cryptography.SHA256]::Create()
    try {
        return ([System.BitConverter]::ToString($hash.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant()
    } finally {
        $hash.Dispose()
    }
}

function Set-EqualsOrdinal {
    param([object[]]$Actual, [string[]]$Expected)
    $left = @($Actual | ForEach-Object { [string]$_ } | Sort-Object -CaseSensitive -Unique)
    $right = @($Expected | Sort-Object -CaseSensitive -Unique)
    if ($left.Count -ne $right.Count) { return $false }
    return (@(Compare-Object -ReferenceObject $right -DifferenceObject $left -CaseSensitive).Count -eq 0)
}

function Test-ExactStringSequence {
    param($Actual, [string[]]$Expected)
    if (-not (Test-StringArray $Actual)) { return $false }
    $values = @($Actual)
    if ($values.Count -ne $Expected.Count) { return $false }
    for ($index = 0; $index -lt $Expected.Count; $index++) {
        if ($values[$index] -cne $Expected[$index]) { return $false }
    }
    return $true
}

function Get-CanonicalJsonEvidence {
    param([string]$Path)
    if (-not (Test-JsonString $Path) -or
        [System.IO.Path]::IsPathRooted($Path) -or
        $Path -match '(^|/|\\)\.\.($|/|\\)') { return $null }
    try {
        $fullPath = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).Path
        $canonical = [System.IO.File]::ReadAllText($fullPath).Replace("`r`n", "`n").Replace("`r", "`n")
        if (-not (Test-NoDuplicateJsonProperties $canonical)) { return $null }
        $json = $canonical | ConvertFrom-Json -Depth 100
    } catch {
        return $null
    }
    $bytes = [System.Text.UTF8Encoding]::new($false).GetBytes($canonical)
    $hash = [System.Security.Cryptography.SHA256]::Create()
    try {
        $sha256 = ([System.BitConverter]::ToString($hash.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant()
    } finally {
        $hash.Dispose()
    }
    return [pscustomobject]@{ sha256 = $sha256; json = $json }
}

function Test-SelfReferencePolicy {
    param($Contracts)
    if (-not (Test-HasProperties $Contracts @('control_plane_maintenance'))) { return $false }
    $maintenance = $Contracts.control_plane_maintenance
    if (-not (Test-HasProperties $maintenance @(
        'schema_version','transition_manifest','manifest_binding_policy',
        'trusted_execution_artifact_policy','head_execution_policy'
    ))) { return $false }
    $binding = $maintenance.manifest_binding_policy
    $trusted = $maintenance.trusted_execution_artifact_policy
    $head = $maintenance.head_execution_policy
    if (-not (Test-HasProperties $binding @(
        'committed_manifest_required_fields','committed_manifest_forbidden_self_references',
        'manifest_container_excluded_from_protected_head_blobs','all_other_protected_head_blobs_required',
        'canonical_manifest_sha256_required','external_mutable_payload_is_sole_authority'
    ))) { return $false }
    if (-not (Test-HasProperties $trusted @(
        'required_fields','binds_actual_event_head_after_checkout',
        'binds_actual_manifest_container_blob_after_checkout','candidate_artifact_is_data_only'
    ))) { return $false }
    if (-not (Test-HasProperties $head @(
        'pull_request_target_executes_trusted_base_only','pull_request_successor_selftest_is_unprivileged',
        'pull_request_successor_selftest_uses_secrets','trusted_execution_artifact_required',
        'candidate_supplied_execution_artifact_forbidden'
    ))) { return $false }
    $required = @($binding.committed_manifest_required_fields)
    $forbidden = @($binding.committed_manifest_forbidden_self_references)
    $trustedFields = @($trusted.required_fields)
    return (
        (Test-JsonInteger $maintenance.schema_version) -and $maintenance.schema_version -eq 2 -and
        $null -eq $maintenance.transition_manifest -and
        (Test-StringArray $binding.committed_manifest_required_fields) -and
        (Test-StringArray $binding.committed_manifest_forbidden_self_references) -and
        (Test-StringArray $trusted.required_fields) -and
        (Test-JsonBoolean $binding.manifest_container_excluded_from_protected_head_blobs) -and $binding.manifest_container_excluded_from_protected_head_blobs -eq $true -and
        (Test-JsonBoolean $binding.all_other_protected_head_blobs_required) -and $binding.all_other_protected_head_blobs_required -eq $true -and
        (Test-JsonBoolean $binding.canonical_manifest_sha256_required) -and $binding.canonical_manifest_sha256_required -eq $true -and
        (Test-JsonBoolean $binding.external_mutable_payload_is_sole_authority) -and $binding.external_mutable_payload_is_sole_authority -eq $false -and
        ('event_head_sha' -cnotin $required) -and
        ('manifest_container_git_blob' -cnotin $required) -and
        ('event_head_sha' -cin $forbidden) -and
        ('manifest_container_git_blob' -cin $forbidden) -and
        ('protected_head_blobs_except_manifest_container' -cin $required) -and
        ('event_head_sha' -cin $trustedFields) -and
        ('manifest_container_git_blob' -cin $trustedFields) -and
        (Test-JsonBoolean $trusted.binds_actual_event_head_after_checkout) -and $trusted.binds_actual_event_head_after_checkout -eq $true -and
        (Test-JsonBoolean $trusted.binds_actual_manifest_container_blob_after_checkout) -and $trusted.binds_actual_manifest_container_blob_after_checkout -eq $true -and
        (Test-JsonBoolean $trusted.candidate_artifact_is_data_only) -and $trusted.candidate_artifact_is_data_only -eq $true -and
        (Test-JsonBoolean $head.pull_request_target_executes_trusted_base_only) -and $head.pull_request_target_executes_trusted_base_only -eq $true -and
        (Test-JsonBoolean $head.pull_request_successor_selftest_is_unprivileged) -and $head.pull_request_successor_selftest_is_unprivileged -eq $true -and
        (Test-JsonBoolean $head.pull_request_successor_selftest_uses_secrets) -and $head.pull_request_successor_selftest_uses_secrets -eq $false -and
        (Test-JsonBoolean $head.trusted_execution_artifact_required) -and $head.trusted_execution_artifact_required -eq $true -and
        (Test-JsonBoolean $head.candidate_supplied_execution_artifact_forbidden) -and $head.candidate_supplied_execution_artifact_forbidden -eq $true
    )
}

function Test-EpochPolicy {
    param($Contracts)
    if (-not (Test-HasProperties $Contracts @('control_plane_maintenance'))) { return $false }
    $maintenance = $Contracts.control_plane_maintenance
    if (-not (Test-HasProperties $maintenance @('active_epoch','consumed_epochs','lifecycle_policy','replay_policy'))) { return $false }
    $active = $maintenance.active_epoch
    $lifecycle = $maintenance.lifecycle_policy
    $replay = $maintenance.replay_policy
    if (-not (Test-HasProperties $active @('epoch','state','reason','not_before','expires_at','max_manifest_ttl_seconds','approval','exact_changes'))) { return $false }
    if (-not (Test-HasProperties $active.approval @('actor_login','actor_id','actor_type','association','label','approval_epoch'))) { return $false }
    if (-not (Test-HasProperties $lifecycle @(
        'states','initial_state','active_to_consumed_requires_accepted_exact_transition',
        'active_to_expired_occurs_at_epoch_expiry','consumed_or_expired_epoch_cannot_reopen',
        'successor_epoch_must_be_monotonic_and_new','max_epoch_lifetime_seconds',
        'rotate_before_expiry_seconds','automatic_extension','expired_epoch_reseed_requires_audited_pr_only_recovery_actor'
    ))) { return $false }
    if (-not (Test-HasProperties $replay @(
        'strict_latest_base','require_event_base_equals_current_default_branch','require_consumed_epoch_transition',
        'require_bounded_successor_epoch','two_pr_race_loser_must_be_stale','new_base_rejects_consumed_epoch'
    ))) { return $false }

    $notBefore = [DateTimeOffset]::MinValue
    $expiresAt = [DateTimeOffset]::MinValue
    $timestampStyles = [Globalization.DateTimeStyles]::AssumeUniversal -bor [Globalization.DateTimeStyles]::AdjustToUniversal
    if ($active.not_before -is [datetime]) {
        $notBefore = [DateTimeOffset]::new($active.not_before.ToUniversalTime())
    } elseif (-not (Test-JsonString $active.not_before) -or
        -not [DateTimeOffset]::TryParse($active.not_before, [Globalization.CultureInfo]::InvariantCulture, $timestampStyles, [ref]$notBefore)) { return $false }
    if ($active.expires_at -is [datetime]) {
        $expiresAt = [DateTimeOffset]::new($active.expires_at.ToUniversalTime())
    } elseif (-not (Test-JsonString $active.expires_at) -or
        -not [DateTimeOffset]::TryParse($active.expires_at, [Globalization.CultureInfo]::InvariantCulture, $timestampStyles, [ref]$expiresAt)) { return $false }
    $lifetime = [int64]($expiresAt - $notBefore).TotalSeconds
    $expectedNotBefore = [DateTimeOffset]::Parse('2026-07-11T00:00:00Z', [Globalization.CultureInfo]::InvariantCulture, $timestampStyles)
    $expectedExpiresAt = [DateTimeOffset]::Parse('2026-10-09T00:00:00Z', [Globalization.CultureInfo]::InvariantCulture, $timestampStyles)

    if (-not (
        (Test-JsonString $active.epoch) -and $active.epoch -ceq 'r12-0001' -and
        (Test-JsonString $active.state) -and $active.state -ceq 'open' -and
        (Test-JsonString $active.reason) -and $active.reason -ceq 'self-reference-free control-plane evolution after rejected A11' -and
        (Test-JsonInteger $active.max_manifest_ttl_seconds) -and [int64]$active.max_manifest_ttl_seconds -eq 86400 -and
        (Test-JsonString $active.approval.actor_login) -and $active.approval.actor_login -ceq 'thebtf' -and
        (Test-JsonInteger $active.approval.actor_id) -and [int64]$active.approval.actor_id -eq 7106373 -and
        (Test-JsonString $active.approval.actor_type) -and $active.approval.actor_type -ceq 'User' -and
        (Test-JsonString $active.approval.association) -and $active.approval.association -ceq 'OWNER' -and
        (Test-JsonString $active.approval.label) -and (Test-JsonString $active.approval.approval_epoch) -and
        $active.approval.approval_epoch -ceq $active.epoch -and $active.approval.label -ceq "authority-maintenance:$($active.epoch)" -and
        (Test-JsonArray $active.exact_changes) -and @($active.exact_changes).Count -gt 0
    )) { return $false }
    $changeKeys = @()
    foreach ($change in @($active.exact_changes)) {
        if (-not (Test-HasProperties $change @('status','path')) -or
            -not (Test-JsonString $change.status) -or $change.status -cnotmatch '^[AMDR]$' -or
            -not (Test-JsonString $change.path)) { return $false }
        $changeKeys += "$($change.status)`t$($change.path)"
    }
    $expectedChangeKeys = @(
        "M`t.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json",
        "M`t.agent/plans/2026-07-10-engram-production-ready-master-plan.md",
        "M`t.agent/plans/2026-07-10-engram-production-ready-ownership-state.json",
        "M`t.agent/plans/2026-07-10-engram-production-ready-scope-map.json",
        "M`t.github/workflows/authority-guard.yml",
        "M`t.github/workflows/test.yml",
        "M`tscripts/production-gates/assert-active-candidate-path-authority.ps1",
        "M`tscripts/production-gates/assert-pr-authority-guard.ps1",
        "M`tscripts/production-gates/assert-pr-authority-maintenance.ps1"
    )
    if (@($changeKeys | Sort-Object -CaseSensitive -Unique).Count -ne $changeKeys.Count -or
        -not (Test-ExactStringSequence $changeKeys $expectedChangeKeys)) { return $false }

    if (-not (
        $notBefore -eq $expectedNotBefore -and $expiresAt -eq $expectedExpiresAt -and
        $notBefore -le [DateTimeOffset]::UtcNow -and $expiresAt -gt [DateTimeOffset]::UtcNow -and
        (Test-JsonInteger $lifecycle.max_epoch_lifetime_seconds) -and [int64]$lifecycle.max_epoch_lifetime_seconds -gt 0 -and
        $lifetime -le [int64]$lifecycle.max_epoch_lifetime_seconds -and
        [int64]$active.max_manifest_ttl_seconds -le $lifetime -and
        (Test-JsonInteger $lifecycle.rotate_before_expiry_seconds) -and [int64]$lifecycle.rotate_before_expiry_seconds -gt 0 -and
        [int64]$lifecycle.rotate_before_expiry_seconds -lt $lifetime -and
        (Test-StringArray $lifecycle.states) -and
        (@(Compare-Object @($lifecycle.states) @('active','consumed','expired') -SyncWindow 0 -CaseSensitive).Count -eq 0) -and
        (Test-JsonString $lifecycle.initial_state) -and $lifecycle.initial_state -ceq 'active'
    )) { return $false }

    foreach ($field in @(
        'active_to_consumed_requires_accepted_exact_transition','active_to_expired_occurs_at_epoch_expiry',
        'consumed_or_expired_epoch_cannot_reopen','successor_epoch_must_be_monotonic_and_new',
        'expired_epoch_reseed_requires_audited_pr_only_recovery_actor'
    )) {
        if (-not (Test-JsonBoolean $lifecycle.$field) -or $lifecycle.$field -ne $true) { return $false }
    }
    if (-not (Test-JsonBoolean $lifecycle.automatic_extension) -or $lifecycle.automatic_extension -ne $false) { return $false }
    foreach ($field in @(
        'strict_latest_base','require_event_base_equals_current_default_branch','require_consumed_epoch_transition',
        'require_bounded_successor_epoch','two_pr_race_loser_must_be_stale','new_base_rejects_consumed_epoch'
    )) {
        if (-not (Test-JsonBoolean $replay.$field) -or $replay.$field -ne $true) { return $false }
    }

    if (-not (Test-JsonArray $maintenance.consumed_epochs)) { return $false }
    $seenEpochs = @($active.epoch)
    foreach ($epoch in @($maintenance.consumed_epochs)) {
        if (-not (Test-HasProperties $epoch @('epoch','state')) -or
            -not (Test-JsonString $epoch.epoch) -or
            -not (Test-JsonString $epoch.state) -or
            $epoch.state -cnotin @('consumed','expired') -or
            $epoch.epoch -cin $seenEpochs) { return $false }
        $seenEpochs += $epoch.epoch
    }
    return $true
}

function Get-MacroErrors {
    param($Scope, $Contracts, $Snapshot)
    $errors = [System.Collections.Generic.List[string]]::new()
    if (-not (Test-HasProperties $Scope @('entries')) -or -not (Test-JsonArray $Scope.entries)) {
        return @('scope entries must be an array')
    }
    if (-not (Test-HasProperties $Contracts @('macro_batches')) -or
        -not (Test-HasProperties $Contracts.macro_batches @(
            'schema_version','source','slice_cardinality_before','slice_cardinality_after',
            'grouped_existing_slice_count','acceptance_unit','internal_commits_are_separate_acceptance_units',
            'dependency_order','batches','collision_serialization','undeclared_overlap_allowed',
            'member_branch_override_allowed'
        ))) { return @('macro contract shape is incomplete') }
    if (-not (Test-HasProperties $Snapshot @('macro_diagnostic')) -or
        -not (Test-HasProperties $Snapshot.macro_diagnostic @('path','sha256','artifact_candidate','verdict','source_candidate'))) {
        return @('macro source snapshot shape is incomplete')
    }
    $expected = [ordered]@{
        MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY = @(
            'DB-GOVERNANCE','DB-RULES-ISOLATION','CANDIDATE-REVIEW-SNAPSHOT-ROLLBACK',
            'INGEST-DOC-SNAPSHOT-DEMOLITION','MCP-STRUCTURED-INPUT-VALIDATION',
            'CRYSTALLIZATION-DREAM-CYCLE-CORRECTNESS','RETRIEVAL-VECTOR-CONTRACT',
            'STATIC-EMBED-CONTRACT','PRE-V5-UPGRADE-CONTRACT'
        )
        MB2_RUNTIME_SECURITY_PRIVACY_AND_V7 = @(
            'DB-AUTH','AUTH-BOOTSTRAP-SECURITY','DURABLE-AUDIT-BOUNDARIES',
            'PRIVACY-BOUNDARIES','REDACTION-LIVE-CONTRACT','OBSERVABILITY-OTLP',
            'S4B-CONTRACT','V7-S4B-BACKEND','V7-CORE-CALLPATH',
            'V7-TELEMETRY-WIRING','V7-RUNTIME-WIRING'
        )
        MB3_DELIVERY_OPERATIONS_UI_CUSTOMER_AND_COVERAGE = @(
            'IMAGE-REMEDIATION-R2','DEPLOYMENT-ROLLBACK','RECOVERY-DATA','UPDATE-LIFECYCLE',
            'OPENCLAW-RELEASE','LAUNCHER-FIRST-RUN','CRITICAL-HARNESS','OC-INTEGRATION',
            'ROADMAP-RECONCILIATION','CORE-PUBLIC-TRUTH','COVERAGE-CMD-ENGRAM',
            'COVERAGE-CMD-SERVER','COVERAGE-UPDATE','COVERAGE-WORKER','COVERAGE-MCP',
            'COVERAGE-GORM','COVERAGE-LOOM'
        )
        MB4_NORTHSTAR_CONTRACT_FREEZE = @(
            'NORTHSTAR-CI-A-CONTRACTS','NORTHSTAR-CI-B-CONTRACTS','NORTHSTAR-BOOK-CONTRACTS',
            'NORTHSTAR-MEM-CONTRACTS','NORTHSTAR-EFFECTIVENESS-CONTRACTS','NORTHSTAR-SETTINGS-CONTRACTS'
        )
    }
    $branches = [ordered]@{
        MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY = 'work/prc-data-integrity-macro-batch'
        MB2_RUNTIME_SECURITY_PRIVACY_AND_V7 = 'work/prc-runtime-security-v7-macro-batch'
        MB3_DELIVERY_OPERATIONS_UI_CUSTOMER_AND_COVERAGE = 'work/prc-delivery-operations-experience-macro-batch'
        MB4_NORTHSTAR_CONTRACT_FREEZE = 'work/prc-northstar-contracts-macro-batch'
    }
    $makers = [ordered]@{
        MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY = $expected.MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY
        MB2_RUNTIME_SECURITY_PRIVACY_AND_V7 = @($expected.MB2_RUNTIME_SECURITY_PRIVACY_AND_V7 | Where-Object { $_ -cne 'DB-AUTH' })
        MB3_DELIVERY_OPERATIONS_UI_CUSTOMER_AND_COVERAGE = @($expected.MB3_DELIVERY_OPERATIONS_UI_CUSTOMER_AND_COVERAGE | Where-Object { $_ -cne 'IMAGE-REMEDIATION-R2' })
        MB4_NORTHSTAR_CONTRACT_FREEZE = $expected.MB4_NORTHSTAR_CONTRACT_FREEZE
    }
    $dependencies = [ordered]@{
        MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY = @('exact:fef455bcf640f849c2d40c9bc26a459b5593e10a')
        MB2_RUNTIME_SECURITY_PRIVACY_AND_V7 = @('MB0_CONTROL_SUPPLY_PUBLIC','MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY','accepted:DB-AUTH')
        MB3_DELIVERY_OPERATIONS_UI_CUSTOMER_AND_COVERAGE = @('MB0_CONTROL_SUPPLY_PUBLIC','MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY','MB2_RUNTIME_SECURITY_PRIVACY_AND_V7')
        MB4_NORTHSTAR_CONTRACT_FREEZE = @('M5_PUBLISHED_POST_RELEASE_PROVED')
    }
    $predecessors = [ordered]@{
        MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY = @()
        MB2_RUNTIME_SECURITY_PRIVACY_AND_V7 = @('DB-AUTH')
        MB3_DELIVERY_OPERATIONS_UI_CUSTOMER_AND_COVERAGE = @('IMAGE-REMEDIATION-R2')
        MB4_NORTHSTAR_CONTRACT_FREEZE = @()
    }
    $annotated = @($Scope.entries | Where-Object { $null -ne $_.PSObject.Properties['macro_batch'] })
    if ($annotated.Count -ne 43) { $errors.Add("macro annotated scope count is $($annotated.Count), expected 43") }
    foreach ($macro in $expected.Keys) {
        $rows = @($annotated | Where-Object { $_.macro_batch -ceq $macro })
        if (-not (Set-EqualsOrdinal $rows.slice $expected[$macro])) { $errors.Add("$macro scope members drifted") }
        foreach ($row in $rows) {
            if ([string]$row.macro_branch -cne $branches[$macro]) { $errors.Add("$($row.slice) uses wrong macro branch") }
        }
        if (@($Scope.entries | Where-Object { $_.slice -ceq $macro }).Count -ne 0) { $errors.Add("macro id $macro was added as a replacement slice") }
    }
    $macroContract = $Contracts.macro_batches
    if (-not (Test-JsonInteger $macroContract.schema_version) -or $macroContract.schema_version -ne 1) { $errors.Add('macro schema version must be integer 1') }
    $expectedSourcePath = '.agent/specs/release-gates-r12/evidence/plan-governance/challenge-report.json'
    $expectedArtifactCandidate = '69b4b871911a84f82ba5050d4d567bdac39a8767'
    $expectedSourceCandidate = 'fef455bcf640f849c2d40c9bc26a459b5593e10a'
    $expectedSourceSha256 = '645bfc6817fe0f723df11398ee048bb1cfac004264cfa045d444289c51681e06'
    $sourceEvidence = Get-CanonicalJsonEvidence $expectedSourcePath
    if (-not (Test-HasProperties $macroContract.source @('path','sha256','artifact_candidate','source_candidate','verdict')) -or
        -not (Test-JsonString $macroContract.source.path) -or
        -not (Test-JsonString $macroContract.source.sha256) -or
        -not (Test-JsonString $macroContract.source.artifact_candidate) -or
        -not (Test-JsonString $macroContract.source.source_candidate) -or
        -not (Test-JsonString $macroContract.source.verdict) -or
        $macroContract.source.path -cne $expectedSourcePath -or
        $macroContract.source.sha256 -cne $expectedSourceSha256 -or
        $macroContract.source.artifact_candidate -cne $expectedArtifactCandidate -or
        $macroContract.source.source_candidate -cne $expectedSourceCandidate -or
        $macroContract.source.path -cne $Snapshot.macro_diagnostic.path -or
        $macroContract.source.sha256 -cnotmatch '^(?!0{64}$)[0-9a-f]{64}$' -or
        $macroContract.source.sha256 -cne $Snapshot.macro_diagnostic.sha256 -or
        $macroContract.source.artifact_candidate -cne $Snapshot.macro_diagnostic.artifact_candidate -or
        $macroContract.source.source_candidate -cne $Snapshot.macro_diagnostic.source_candidate -or
        $macroContract.source.verdict -cne 'feasible' -or
        $Snapshot.macro_diagnostic.verdict -cne 'DIAGNOSTIC_FREEZE_MACRO_BATCHING_FEASIBLE' -or
        $null -eq $sourceEvidence -or
        $sourceEvidence.sha256 -cne $expectedSourceSha256 -or
        -not (Test-HasProperties $sourceEvidence.json @('verdict','verified_live_facts','macro_rechallenge')) -or
        $sourceEvidence.json.verdict -cne 'GO' -or
        $sourceEvidence.json.macro_rechallenge.verdict -cne 'GO' -or
        $sourceEvidence.json.verified_live_facts.external_db_base -cne $expectedSourceCandidate -or
        $sourceEvidence.json.verified_live_facts.macro_diagnostic_sha256 -cne '34bcdf4d7a708b22a1e33a6e174206cc32e83aeb09c7b6f191ee7b5b6f90f061') {
        $errors.Add('macro diagnostic source binding drifted')
    }
    if (-not (Test-JsonInteger $macroContract.slice_cardinality_before) -or
        -not (Test-JsonInteger $macroContract.slice_cardinality_after) -or
        $macroContract.slice_cardinality_before -ne 69 -or $macroContract.slice_cardinality_after -ne 69) { $errors.Add('macro cardinality must remain integer 69/69') }
    if (-not (Test-JsonInteger $macroContract.grouped_existing_slice_count) -or $macroContract.grouped_existing_slice_count -ne 43) { $errors.Add('macro grouped slice count must remain integer 43') }
    if (-not (Test-JsonString $macroContract.acceptance_unit) -or
        $macroContract.acceptance_unit -cne 'one immutable macro head plus one fresh maker-distinct checker and root post-review') { $errors.Add('macro acceptance unit drifted') }
    if (-not (Test-JsonBoolean $macroContract.internal_commits_are_separate_acceptance_units) -or
        $macroContract.internal_commits_are_separate_acceptance_units -ne $false) { $errors.Add('internal commits became acceptance units') }
    $expectedOrder = @('MB0_CONTROL_SUPPLY_PUBLIC','MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY','MB2_RUNTIME_SECURITY_PRIVACY_AND_V7','MB3_DELIVERY_OPERATIONS_UI_CUSTOMER_AND_COVERAGE','M5_PUBLISHED_POST_RELEASE_PROVED','MB4_NORTHSTAR_CONTRACT_FREEZE')
    if (-not (Test-StringArray $macroContract.dependency_order) -or
        -not (Set-EqualsOrdinal $macroContract.dependency_order $expectedOrder) -or
        (@(Compare-Object @($macroContract.dependency_order) $expectedOrder -SyncWindow 0 -CaseSensitive).Count -ne 0)) { $errors.Add('macro dependency order drifted') }
    if (-not (Test-JsonBoolean $macroContract.undeclared_overlap_allowed) -or $macroContract.undeclared_overlap_allowed -ne $false -or
        -not (Test-JsonBoolean $macroContract.member_branch_override_allowed) -or $macroContract.member_branch_override_allowed -ne $false) { $errors.Add('macro overlap or branch override must fail closed') }
    if (-not (Test-JsonArray $macroContract.batches) -or @($macroContract.batches).Count -ne 4) {
        $errors.Add('macro batch list must contain exactly four objects')
    } else {
        foreach ($id in $expected.Keys) {
            $batchRows = @($macroContract.batches | Where-Object { $_.PSObject.Properties['id'] -and $_.id -ceq $id })
            if ($batchRows.Count -ne 1) { $errors.Add("$id must have exactly one batch contract"); continue }
            $batch = $batchRows[0]
            if (-not (Test-HasProperties $batch @('id','branch','dependencies','accepted_predecessors')) -or
                -not (Test-JsonString $batch.id) -or
                -not (Test-JsonString $batch.branch) -or $batch.branch -cne $branches[$id] -or
                -not (Test-StringArray $batch.dependencies) -or
                (@(Compare-Object @($batch.dependencies) @($dependencies[$id]) -SyncWindow 0 -CaseSensitive).Count -ne 0) -or
                -not (Test-StringArray $batch.accepted_predecessors) -or
                (@(Compare-Object @($batch.accepted_predecessors) @($predecessors[$id]) -SyncWindow 0 -CaseSensitive).Count -ne 0)) {
                $errors.Add("$id identity, branch, dependency, or predecessor contract drifted")
                continue
            }
            $makerProperty = if ($id -ceq 'MB4_NORTHSTAR_CONTRACT_FREEZE') { 'contract_makers' } else { 'makers' }
            if ($null -eq $batch.PSObject.Properties[$makerProperty] -or
                -not (Test-ExactStringSequence $batch.$makerProperty $makers[$id])) {
                $errors.Add("$id maker contract drifted")
            }
            if ($id -ceq 'MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY' -and
                ($null -eq $batch.PSObject.Properties['base_anchor'] -or
                 -not (Test-JsonString $batch.base_anchor) -or
                 $batch.base_anchor -cne 'fef455bcf640f849c2d40c9bc26a459b5593e10a')) { $errors.Add('MB1 base drifted') }
            if ($id -ceq 'MB4_NORTHSTAR_CONTRACT_FREEZE' -and
                ($null -eq $batch.PSObject.Properties['exact_disjoint_contract_path_count'] -or
                 -not (Test-JsonInteger $batch.exact_disjoint_contract_path_count) -or
                 $batch.exact_disjoint_contract_path_count -ne 31)) { $errors.Add('MB4 exact path count drifted') }
        }
    }
    $dbGovernance = @($Contracts.pending_namespaces | Where-Object slice -eq 'MB1_DATA_INTEGRITY_AND_MUTATION_SAFETY')[0].exact_r12_overrides | Where-Object slice -eq 'DB-GOVERNANCE'
    if (@($dbGovernance.exact_paths).Count -ne 4 -or 'internal/db/gorm/candidate_store.go' -cnotin @($dbGovernance.forbidden_paths) -or 'internal/db/gorm/candidate_store_test.go' -cnotin @($dbGovernance.forbidden_paths)) { $errors.Add('DB-GOVERNANCE overlap closure drifted') }
    $expectedCollisions = @(
        [pscustomobject]@{ paths=@('internal/db/gorm/candidate_store.go','internal/db/gorm/candidate_store_test.go'); order=@('CANDIDATE-REVIEW-SNAPSHOT-ROLLBACK'); source=@('DB-GOVERNANCE','CANDIDATE-REVIEW-SNAPSHOT-ROLLBACK') },
        [pscustomobject]@{ paths=@('internal/db/gorm/user_store.go'); order=@('DB-AUTH','AUTH-BOOTSTRAP-SECURITY','DURABLE-AUDIT-BOUNDARIES'); source=$null },
        [pscustomobject]@{ paths=@('internal/worker/auth_handlers.go'); order=@('DB-AUTH','AUTH-BOOTSTRAP-SECURITY','DURABLE-AUDIT-BOUNDARIES'); source=$null },
        [pscustomobject]@{ paths=@('internal/worker/service.go'); order=@('AUTH-BOOTSTRAP-SECURITY','REDACTION-LIVE-CONTRACT','V7-RUNTIME-WIRING'); source=$null },
        [pscustomobject]@{ paths=@('internal/db/gorm/memory_store.go'); order=@('DB-RULES-ISOLATION','PRIVACY-BOUNDARIES'); source=$null },
        [pscustomobject]@{ paths=@('docker-compose.yml','deploy/docker-compose.runtime.yml'); order=@('IMAGE-REMEDIATION-R2','DEPLOYMENT-ROLLBACK'); source=$null },
        [pscustomobject]@{ paths=@('docs/DEPLOYMENT.md','docs/PRODUCTION-TESTING-PLAYBOOK.md'); order=@('IMAGE-REMEDIATION-R2','CORE-PUBLIC-TRUTH'); source=$null }
    )
    if (-not (Test-JsonArray $macroContract.collision_serialization) -or @($macroContract.collision_serialization).Count -ne $expectedCollisions.Count) {
        $errors.Add('collision serialization list drifted')
    } else {
        foreach ($expectedCollision in $expectedCollisions) {
            $matches = @($macroContract.collision_serialization | Where-Object {
                $_.PSObject.Properties['paths'] -and (Test-ExactStringSequence $_.paths $expectedCollision.paths)
            })
            if ($matches.Count -ne 1) { $errors.Add("collision contract missing for $($expectedCollision.paths -join ',')"); continue }
            $collision = $matches[0]
            if (-not (Test-HasProperties $collision @('effective_order')) -or
                -not (Test-StringArray $collision.effective_order) -or
                (@(Compare-Object @($collision.effective_order) @($expectedCollision.order) -SyncWindow 0 -CaseSensitive).Count -ne 0)) {
                $errors.Add("collision order drifted for $($expectedCollision.paths -join ',')")
            }
            if ($null -ne $expectedCollision.source -and
                ($null -eq $collision.PSObject.Properties['source_order'] -or
                 -not (Test-StringArray $collision.source_order) -or
                 (@(Compare-Object @($collision.source_order) @($expectedCollision.source) -SyncWindow 0 -CaseSensitive).Count -ne 0))) {
                $errors.Add('candidate-store source order drifted')
            }
        }
    }
    return @($errors)
}

function Test-DocumentR3 {
    param($Scope, $Contracts, $State)
    $row = @($Scope.entries | Where-Object slice -eq 'DOCUMENT-INGEST-PUBLIC-TRUTH-R3')
    $pending = @($Contracts.pending_namespaces | Where-Object slice -eq 'DOCUMENT-INGEST-PUBLIC-TRUTH-R3')
    if ($row.Count -ne 1 -or $pending.Count -ne 1) { return $false }
    $document = $pending[0]
    $paths = @($document.exact_paths)
    if ($document.base_anchor -cne 'efe1ab5041c514f1a7d9ee8b36e07e5150cfc8e0') { return $false }
    if ($document.immutable_head -cne '9ea95b6adc3a838a459bf78e3ba7a0292153960f' -or $document.immutable_tree -cne 'c15a731de8ca8acac09bce8b209e2a27dd6303cb') { return $false }
    if ('f3bba8815c97cf2000e46fdb2fbe1414d61a6264' -cnotin @($document.forbidden_bases)) { return $false }
    $expectedPaths = @(
        '.agent/reports/evidence/production-ready/document-r3-atomic-remove-20260712-0142/summary.json',
        '.agent/specs/production-ready-document-r3-atomic-remove/evidence/DOCUMENT-R3-ATOMIC-REMOVE.red.json',
        '.agent/specs/production-ready-document-r3-atomic-remove/evidence/DOCUMENT-R3-ATOMIC-REMOVE.tdd.json',
        'internal/db/gorm/document_store.go',
        'internal/mcp/document_public_truth_contract_test.go',
        'internal/mcp/tools_documents.go'
    )
    if (-not (Set-EqualsOrdinal $paths $expectedPaths)) { return $false }
    $contract = $document.acceptance_contract
    if (-not $contract.remove_document_split_precheck_forbidden -or $contract.remove_document_transition -notmatch 'active=true' -or $contract.remove_document_transition -notmatch 'RowsAffected=1') { return $false }
    if ($contract.store_logic_and_schema_byte_identical -or -not $contract.schema_and_unrelated_store_logic_byte_identical) { return $false }
    $storeEpoch = @($State.path_epochs | Where-Object path -eq 'internal/db/gorm/document_store.go')
    $toolEpoch = @($State.path_epochs | Where-Object path -eq 'internal/mcp/tools_documents.go')
    return ($storeEpoch.Count -eq 1 -and $toolEpoch.Count -eq 1 -and $storeEpoch[0].current_owner -ceq 'DOCUMENT-INGEST-PUBLIC-TRUTH-R3' -and $toolEpoch[0].current_owner -ceq 'DOCUMENT-INGEST-PUBLIC-TRUTH-R3' -and $storeEpoch[0].change_constraint -match 'RowsAffected' -and $toolEpoch[0].mutation_edge_constraint -match 'remove the split GetDocument')
}

$planText = Get-Content -LiteralPath $Plan -Raw
$state = Read-Json $OwnershipState
$scope = Read-Json $ScopeMap
$contracts = Read-Json $ActiveContracts
$envelope = Read-Json $PathEnvelope
$fixedPoint = Read-Json $FixedPointProof
$snapshot = Read-Json $AuthoritySnapshot
$trustedAuthorityContractsSha256 = '9776e67bb8b2bc93de8fca2ec9296f765e64ef3d2380f40b306ca2d6b193a67a'
$immutableContractsPath = '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json'
Assert-True ((Get-CanonicalTextSha256 $ActiveContracts) -ceq $trustedAuthorityContractsSha256) 'release authority manifest drifted from trusted closed-world digest'
Assert-True (Test-StrictAuthoritySchema $contracts $contracts) 'release authority schema is incomplete'
$canonicalContractsPath = [System.IO.Path]::GetFullPath((Join-Path (Get-Location) $immutableContractsPath))
if ([System.IO.Path]::GetFullPath($ActiveContracts) -ceq $canonicalContractsPath) {
    Assert-True (-not (Test-NoDuplicateJsonProperties '{"outer":{"field":1,"field":2}}')) 'duplicate JSON property was accepted'
    Assert-True (-not (Test-NoDuplicateJsonProperties '{"outer":{"field":1,"Field":2}}')) 'case-alias JSON property was accepted'
    foreach ($surface in @('authority','external_enforcement','control_plane_maintenance','macro_batches')) {
        Assert-ClosedWorldComparerSelfTest $contracts.$surface $surface
    }
}
Assert-True ($planText -match 'Revision:\s*12') 'master plan does not declare revision 12'
Assert-True ($planText.Contains('RELEASE-GATES-CONTROL-PLANE-EVOLVABILITY-R12')) 'master plan lacks R12 owner'
Assert-True ($planText.Contains('R12 Macro-Batch Execution Overlay')) 'master plan lacks macro overlay'
Assert-True ($planText.Contains('DOCUMENT-INGEST-PUBLIC-TRUTH-R3')) 'master plan lacks Document R3 successor'
Assert-True (@($scope.entries).Count -eq 69) 'scope map must remain 69 rows'
Assert-True (@($scope.entries.slice | Sort-Object -CaseSensitive -Unique).Count -eq 69) 'scope slice identities must remain 69 unique rows'
Assert-True ([int]$contracts.revision -eq 12) 'active contracts revision must be 12'
Assert-True ($contracts.authority.r12_required_base -ceq '887b7c6144a09829a1a3f43ff5a19d3fb27fb7c1') 'A12 exact parent drifted'
Assert-True ($fixedPoint.verdict -ceq 'REJECT_A11_HOLD_B11') 'fixed-point proof must reject A11 and hold B11'
$fixedPointHash = Get-CanonicalTextSha256 $FixedPointProof
Assert-True ($fixedPointHash -ceq [string]$contracts.authority.a11_fixed_point_evidence_sha256) 'fixed-point proof hash does not match active authority'
$proofText = [System.IO.File]::ReadAllText((Resolve-Path -LiteralPath $FixedPointProof).Path).Replace("`r`n", "`n").Replace("`r", "`n")
$crlfProof = [System.IO.Path]::GetTempFileName()
try {
    [System.IO.File]::WriteAllText($crlfProof, $proofText.Replace("`n", "`r`n"), [System.Text.UTF8Encoding]::new($false))
    Assert-True ((Get-CanonicalTextSha256 $crlfProof) -ceq $fixedPointHash) 'fixed-point proof canonical hash changes under CRLF checkout'
} finally {
    Remove-Item -LiteralPath $crlfProof -Force -ErrorAction SilentlyContinue
}
Assert-True (Test-SelfReferencePolicy $contracts) 'self-reference-free manifest/trusted-artifact policy is incomplete'
Assert-True (Test-EpochPolicy $contracts) 'epoch approval, time, lifecycle, or replay policy is inconsistent'
Assert-True ($contracts.control_plane_maintenance.replay_policy.strict_latest_base) 'strict latest-base must remain enabled'
Assert-True ($contracts.control_plane_maintenance.replay_policy.require_consumed_epoch_transition) 'consumed epoch transition must remain required'
Assert-True ($contracts.control_plane_maintenance.replay_policy.two_pr_race_loser_must_be_stale) 'two-PR loser must remain stale'
Assert-True ($contracts.external_enforcement.required_status_context -ceq 'authority-guard') 'status context drifted'
Assert-True ((Test-JsonInteger $contracts.external_enforcement.required_status_integration_id) -and
    [int64]$contracts.external_enforcement.required_status_integration_id -eq 15368) 'status integration id type or value drifted'
Assert-True ($contracts.external_enforcement.reject_context_name_only_or_untrusted_integration) 'untrusted context-only status must be rejected'
Assert-True (@($envelope.commit_a12.paths).Count -eq 15) 'A12 exact path count must be 15'
Assert-True (@($envelope.commit_b12.paths).Count -eq 21) 'B12 exact path count must be 21'
Assert-True ($envelope.commit_a12.required_parent -ceq '887b7c6144a09829a1a3f43ff5a19d3fb27fb7c1') 'A12 envelope parent drifted'

$r12Paths = @(
    '.github/workflows/test.yml',
    '.github/workflows/authority-guard.yml',
    'scripts/production-gates/assert-active-candidate-path-authority.ps1',
    'scripts/production-gates/assert-pr-authority-guard.ps1',
    'scripts/production-gates/assert-pr-authority-maintenance.ps1',
    '.agent/plans/2026-07-10-engram-production-ready-master-plan.md',
    '.agent/plans/2026-07-10-engram-production-ready-ownership-state.json',
    '.agent/plans/2026-07-10-engram-production-ready-scope-map.json',
    '.agent/plans/2026-07-10-engram-production-ready-active-diff-contracts.json'
)
foreach ($path in $r12Paths) {
    $epoch = @($state.path_epochs | Where-Object path -eq $path)
    Assert-True ($epoch.Count -eq 1) "missing R12 epoch for $path"
    Assert-True ($epoch[0].current_owner -ceq 'RELEASE-GATES-CONTROL-PLANE-EVOLVABILITY-R12') "R12 is not current owner for $path"
}

$macroErrors = @(Get-MacroErrors $scope $contracts $snapshot)
Assert-True ($macroErrors.Count -eq 0) ("macro governance failed: " + ($macroErrors -join '; '))
Assert-True (Test-DocumentR3 $scope $contracts $state) 'Document R3 atomic successor contract is incomplete'

# Hostile flip matrix: each load-bearing guard must reject its own inverse.
$selfRefFlip = Copy-Json $contracts
$selfRefFlip.control_plane_maintenance.manifest_binding_policy.committed_manifest_required_fields += 'event_head_sha'
Assert-True (-not (Test-SelfReferencePolicy $selfRefFlip)) 'self-referential event head was accepted'
$branchFlipScope = Copy-Json $scope
(@($branchFlipScope.entries | Where-Object slice -eq 'DB-GOVERNANCE'))[0].macro_branch = 'work/wrong-branch'
Assert-True (@(Get-MacroErrors $branchFlipScope $contracts $snapshot).Count -gt 0) 'macro branch override was accepted'
$collisionFlip = Copy-Json $contracts
(@($collisionFlip.macro_batches.collision_serialization | Where-Object { 'internal/db/gorm/candidate_store.go' -cin @($_.paths) }))[0].effective_order = @('DB-GOVERNANCE')
Assert-True (@(Get-MacroErrors $scope $collisionFlip $snapshot).Count -gt 0) 'candidate-store overlap flip was accepted'
$documentFlip = Copy-Json $contracts
(@($documentFlip.pending_namespaces | Where-Object slice -eq 'DOCUMENT-INGEST-PUBLIC-TRUTH-R3'))[0].acceptance_contract.remove_document_split_precheck_forbidden = $false
Assert-True (-not (Test-DocumentR3 $scope $documentFlip $state)) 'Document R3 split-precheck flip was accepted'

$schemaTypeFlip = Copy-Json $contracts
$schemaTypeFlip.control_plane_maintenance.schema_version = '2'
Assert-True (-not (Test-SelfReferencePolicy $schemaTypeFlip)) 'string control-plane schema version was accepted'
$booleanTypeFlip = Copy-Json $contracts
$booleanTypeFlip.control_plane_maintenance.head_execution_policy.candidate_supplied_execution_artifact_forbidden = 'false'
Assert-True (-not (Test-SelfReferencePolicy $booleanTypeFlip)) 'string false security policy was accepted'
$approvalFlip = Copy-Json $contracts
$approvalFlip.control_plane_maintenance.active_epoch.approval.approval_epoch = 'r12-wrong'
Assert-True (-not (Test-EpochPolicy $approvalFlip)) 'approval epoch mismatch was accepted'
$expiryFlip = Copy-Json $contracts
$expiryFlip.control_plane_maintenance.active_epoch.expires_at = '2026-01-01T00:00:00Z'
Assert-True (-not (Test-EpochPolicy $expiryFlip)) 'epoch expiry before not-before was accepted'
$reopenedFlip = Copy-Json $contracts
$reopenedFlip.control_plane_maintenance.active_epoch.state = 'consumed'
Assert-True (-not (Test-EpochPolicy $reopenedFlip)) 'consumed active epoch was reopened'
$consumedDuplicateFlip = Copy-Json $contracts
$consumedDuplicateFlip.control_plane_maintenance.consumed_epochs = @([pscustomobject]@{ epoch = 'r12-0001'; state = 'consumed' })
Assert-True (-not (Test-EpochPolicy $consumedDuplicateFlip)) 'active epoch was also accepted as consumed'
$staleLoserFlip = Copy-Json $contracts
$staleLoserFlip.control_plane_maintenance.replay_policy.two_pr_race_loser_must_be_stale = $false
Assert-True (-not (Test-EpochPolicy $staleLoserFlip)) 'two-PR stale-loser guard was disabled'
$mb2MakersFlip = Copy-Json $contracts
(@($mb2MakersFlip.macro_batches.batches | Where-Object id -eq 'MB2_RUNTIME_SECURITY_PRIVACY_AND_V7'))[0].makers = @()
Assert-True (@(Get-MacroErrors $scope $mb2MakersFlip $snapshot).Count -gt 0) 'empty MB2 maker set was accepted'
$mb3BranchFlip = Copy-Json $contracts
(@($mb3BranchFlip.macro_batches.batches | Where-Object id -eq 'MB3_DELIVERY_OPERATIONS_UI_CUSTOMER_AND_COVERAGE'))[0].branch = 'work/wrong'
Assert-True (@(Get-MacroErrors $scope $mb3BranchFlip $snapshot).Count -gt 0) 'wrong MB3 branch was accepted'
$mb4PathFlip = Copy-Json $contracts
(@($mb4PathFlip.macro_batches.batches | Where-Object id -eq 'MB4_NORTHSTAR_CONTRACT_FREEZE'))[0].exact_disjoint_contract_path_count = 0
Assert-True (@(Get-MacroErrors $scope $mb4PathFlip $snapshot).Count -gt 0) 'zero MB4 path count was accepted'
$composeOrderFlip = Copy-Json $contracts
(@($composeOrderFlip.macro_batches.collision_serialization | Where-Object { 'docker-compose.yml' -cin @($_.paths) }))[0].effective_order = @('DEPLOYMENT-ROLLBACK','IMAGE-REMEDIATION-R2')
Assert-True (@(Get-MacroErrors $scope $composeOrderFlip $snapshot).Count -gt 0) 'reversed compose collision order was accepted'
$sourceHashFlip = Copy-Json $contracts
$sourceHashFlip.macro_batches.source.sha256 = '0' * 64
Assert-True (@(Get-MacroErrors $scope $sourceHashFlip $snapshot).Count -gt 0) 'zero macro diagnostic source hash was accepted'
$integrationTypeFlip = Copy-Json $contracts
$integrationTypeFlip.external_enforcement.required_status_integration_id = '15368'
Assert-True (-not (Test-JsonInteger $integrationTypeFlip.external_enforcement.required_status_integration_id)) 'string status integration id was accepted'
$futureNotBeforeFlip = Copy-Json $contracts
$futureNotBeforeFlip.control_plane_maintenance.active_epoch.not_before = '2026-07-14T00:00:00Z'
Assert-True (-not (Test-EpochPolicy $futureNotBeforeFlip)) 'future or noncanonical epoch not-before was accepted'
$approvalActorFlip = Copy-Json $contracts
$approvalActorFlip.control_plane_maintenance.active_epoch.approval.actor_login = 'attacker'
$approvalActorFlip.control_plane_maintenance.active_epoch.approval.actor_id = 1
$approvalActorFlip.control_plane_maintenance.active_epoch.approval.actor_type = 'Bot'
$approvalActorFlip.control_plane_maintenance.active_epoch.approval.association = 'NONE'
Assert-True (-not (Test-EpochPolicy $approvalActorFlip)) 'unauthorized approval actor was accepted'
$exactChangesFlip = Copy-Json $contracts
$exactChangesFlip.control_plane_maintenance.active_epoch.exact_changes = @([pscustomobject]@{ status = 'M'; path = 'README.md' })
Assert-True (-not (Test-EpochPolicy $exactChangesFlip)) 'unauthorized exact-change set was accepted'
$coordinatedSourceHashFlip = Copy-Json $contracts
$coordinatedSourceSnapshot = Copy-Json $snapshot
$coordinatedSourceHashFlip.macro_batches.source.sha256 = '1' * 64
$coordinatedSourceSnapshot.macro_diagnostic.sha256 = '1' * 64
Assert-True (@(Get-MacroErrors $scope $coordinatedSourceHashFlip $coordinatedSourceSnapshot).Count -gt 0) 'coordinated bogus source hash was accepted'
$coordinatedSourcePathFlip = Copy-Json $contracts
$coordinatedPathSnapshot = Copy-Json $snapshot
$coordinatedSourcePathFlip.macro_batches.source.path = '.agent/definitely-missing-source.json'
$coordinatedPathSnapshot.macro_diagnostic.path = '.agent/definitely-missing-source.json'
Assert-True (@(Get-MacroErrors $scope $coordinatedSourcePathFlip $coordinatedPathSnapshot).Count -gt 0) 'coordinated missing source path was accepted'
$duplicateMakerFlip = Copy-Json $contracts
$duplicateMakerRow = (@($duplicateMakerFlip.macro_batches.batches | Where-Object id -eq 'MB2_RUNTIME_SECURITY_PRIVACY_AND_V7'))[0]
$duplicateMakerRow.makers = @($duplicateMakerRow.makers) + @($duplicateMakerRow.makers[0])
Assert-True (@(Get-MacroErrors $scope $duplicateMakerFlip $snapshot).Count -gt 0) 'duplicate MB2 maker was accepted'

$closedWorldUnknownFlip = Copy-Json $contracts
$closedWorldUnknownFlip.control_plane_maintenance.manifest_binding_policy | Add-Member -NotePropertyName manifest_container_blob_sha -NotePropertyValue 'alias'
Assert-True (-not (Test-StrictAuthoritySchema $closedWorldUnknownFlip $contracts)) 'unknown manifest alias was accepted'
$closedWorldMissingFlip = Copy-Json $contracts
$closedWorldMissingFlip.control_plane_maintenance.trusted_execution_artifact_policy.PSObject.Properties.Remove('producer')
Assert-True (-not (Test-StrictAuthoritySchema $closedWorldMissingFlip $contracts)) 'missing trusted producer was accepted'
$closedWorldCollisionFlip = Copy-Json $contracts
$closedWorldCollisionFlip.macro_batches.collision_serialization[0].paths = @($closedWorldCollisionFlip.macro_batches.collision_serialization[0].paths) + @($closedWorldCollisionFlip.macro_batches.collision_serialization[0].paths[0])
Assert-True (-not (Test-StrictAuthoritySchema $closedWorldCollisionFlip $contracts)) 'duplicate collision path was accepted'

Write-Output 'R12 PLAN-GOVERNANCE PASS'
