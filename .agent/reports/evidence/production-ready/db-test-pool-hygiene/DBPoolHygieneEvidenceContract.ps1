$DBPHManifestSchemaVersion = [int64]4
$DBPHInventorySchemaVersion = [int64]1
$DBPHVerifierProofSchemaVersion = [int64]4
$DBPHAdversarialProofSchemaVersion = [int64]4
$DBPHProductParentSHA = 'bd68c05baf4b7250096dd84f56bebea2aa555970'
$DBPHProductCandidateSHA = '276337b3e96aa5af6d2e7dd9a0002ff957e5ffc9'
$DBPHEvidenceParentSHA = '331b5b195a967e7f27dca94038a3480c9afcc84f'
$DBPHProductBlobPath = 'internal/db/gorm/candidate_store_test.go'
$DBPHProductBlobOID = '7337f1bd8da4fb315de842eea2e3cce5476250a3'
$DBPHProductBlobSHA256 = '62260c1a2e0705b065295322dd23fcf9b17fd47cb5ebc64134630788e2d23e09'
$DBPHRepresentationContract = 'git-blob-bytes-v1'

function Get-DBPHExactJsonProperties {
    param(
        [Parameter(Mandatory = $true)]
        [ValidateSet(
            'manifest',
            'manifest.evidence_delta',
            'manifest.representation_contract',
            'manifest.product_test_blob',
            'manifest.inventory',
            'manifest.entry',
            'inventory',
            'inventory.entry',
            'verifier-proof',
            'adversarial-proof',
            'adversarial-proof.case'
        )]
        [string]$Schema
    )

    switch ($Schema) {
        'manifest' {
            return [string[]]@(
                'schema_version', 'status', 'product_parent_sha',
                'product_candidate_sha', 'evidence_revision_parent_sha',
                'evidence_revision_target', 'evidence_delta',
                'representation_contract', 'product_test_blob', 'inventory',
                'entry_count', 'entries'
            )
        }
        'manifest.evidence_delta' {
            return [string[]]@(
                'comparison', 'changed_path_count',
                'directly_manifest_bound_count', 'product_or_test_paths_changed',
                'manifest_entry_self_excluded_paths',
                'checksum_self_excluded_paths'
            )
        }
        'manifest.representation_contract' {
            return [string[]]@(
                'id', 'digest', 'object_type', 'path_binding', 'contract_bytes',
                'working_tree_bytes', 'text_git_blob_line_endings',
                'manifest_self_reference', 'outer_checksum_path',
                'outer_checksum_generation_order',
                'outer_checksum_self_reference'
            )
        }
        'manifest.product_test_blob' {
            return [string[]]@(
                'path', 'git_blob_oid', 'sha256',
                'byte_identical_to_product_candidate'
            )
        }
        'manifest.inventory' {
            return [string[]]@('parent_sha', 'required_call_sites', 'required_files', 'path')
        }
        'manifest.entry' {
            return [string[]]@('path', 'git_blob_oid', 'bytes', 'sha256')
        }
        'inventory' {
            return [string[]]@(
                'schema_version', 'parent_sha', 'symbol', 'definition', 'method',
                'required_call_sites', 'required_files', 'actual_call_sites',
                'actual_files', 'entries'
            )
        }
        'inventory.entry' {
            return [string[]]@('path', 'count', 'lines')
        }
        'verifier-proof' {
            return [string[]]@(
                'schema_version', 'status', 'source_mode', 'revision',
                'product_candidate_sha', 'representation_contract',
                'changed_paths', 'directly_bound_changed_paths',
                'manifest_entries', 'checksum_entries', 'inventory_call_sites',
                'inventory_files', 'failures'
            )
        }
        'adversarial-proof' {
            return [string[]]@(
                'schema_version', 'status', 'source_mode', 'revision', 'cases',
                'temp_root_removed', 'cleanup'
            )
        }
        'adversarial-proof.case' {
            return [string[]]@(
                'name', 'expected_exit', 'actual_exit', 'verifier_status',
                'expected_failure', 'observed_failures', 'pass'
            )
        }
    }
}

function Get-DBPHAdversarialCaseSpecs {
    $specs = @(
        @('baseline', $false, ''),
        @('missing_changed_path', $true, 'manifest missing changed path'),
        @('unsorted_manifest_and_sums', $true, 'not canonical ordinal order'),
        @('duplicate_manifest_and_sums', $true, 'duplicate manifest path'),
        @('wrong_type_representation_id_array', $true, 'must be JSON string, got array'),
        @('null_representation_id', $true, 'must be JSON string, got null'),
        @('wrong_type_exclusions_scalar', $true, 'must be JSON array, got string'),
        @('wrong_type_inventory_numeric_strings', $true, 'must be JSON number, got string'),
        @('null_inventory_count', $true, 'must be JSON number, got null'),
        @('crlf_raw_representation', $true, 'manifest bytes contain CR'),
        @('incorrect_representation_contract', $true, 'unsupported representation contract'),
        @('false_inventory_76_6', $true, 'inventory acceptance constants must be 83/8'),
        @('mixed_eol_representation', $true, 'manifest bytes contain CR'),
        @('unknown_manifest_key', $true, 'manifest has unknown JSON property'),
        @('unknown_manifest_nested_key', $true, 'manifest.representation_contract has unknown JSON property'),
        @('unknown_manifest_entry_key', $true, 'manifest.entries[0] has unknown JSON property'),
        @('unknown_inventory_key', $true, 'inventory has unknown JSON property'),
        @('unknown_inventory_entry_key', $true, 'inventory.entries[0] has unknown JSON property'),
        @('manifest_schema_99', $true, 'manifest schema_version must be 4'),
        @('inventory_schema_99', $true, 'inventory schema_version must be 1'),
        @('adversarial_proof_schema_99', $true, 'adversarial proof schema_version must be 4'),
        @('verifier_proof_schema_99', $true, 'verifier proof schema_version must be 4'),
        @('unknown_adversarial_proof_key', $true, 'adversarial proof has unknown JSON property'),
        @('unknown_adversarial_case_key', $true, 'adversarial proof.cases[0] has unknown JSON property'),
        @('unknown_verifier_proof_key', $true, 'verifier proof has unknown JSON property'),
        @('stale_adversarial_proof', $true, 'adversarial proof case count mismatch'),
        @('stale_verifier_proof', $true, 'verifier proof changed_paths mismatch'),
        @('false_adversarial_case_order', $true, 'adversarial proof case order mismatch'),
        @('false_adversarial_status', $true, 'adversarial proof status must be PASS'),
        @('false_adversarial_actual_status', $true, 'adversarial proof case result mismatch'),
        @('false_adversarial_required_diagnostic', $true, 'adversarial proof case missing required diagnostic'),
        @('false_verifier_counts', $true, 'verifier proof manifest_entries mismatch'),
        @('false_verifier_status', $true, 'verifier proof status must be PASS'),
        @('false_verifier_source_revision', $true, 'verifier proof source/revision contract mismatch'),
        @('duplicate_json_property', $true, 'duplicate JSON property')
    )

    $result = [Collections.Generic.List[object]]::new()
    foreach ($spec in $specs) {
        $result.Add([pscustomobject]@{
            name = [string]$spec[0]
            expect_nonzero = [bool]$spec[1]
            expected_failure = [string]$spec[2]
        })
    }
    return [object[]]@($result)
}

function New-DBPHSeedAdversarialProof {
    $cases = [Collections.Generic.List[object]]::new()
    foreach ($spec in Get-DBPHAdversarialCaseSpecs) {
        $isBaseline = -not $spec.expect_nonzero
        $observedFailures = [object[]]@()
        if (-not $isBaseline) { $observedFailures = [object[]]@($spec.expected_failure) }
        $cases.Add([ordered]@{
            name = $spec.name
            expected_exit = if ($isBaseline) { 0 } else { 'nonzero' }
            actual_exit = if ($isBaseline) { 0 } else { 1 }
            verifier_status = if ($isBaseline) { 'PASS' } else { 'FAIL' }
            expected_failure = $spec.expected_failure
            observed_failures = $observedFailures
            pass = $true
        })
    }
    return [ordered]@{
        schema_version = $DBPHAdversarialProofSchemaVersion
        status = 'PASS'
        source_mode = 'GitIndex'
        revision = 'INDEX'
        cases = [object[]]@($cases)
        temp_root_removed = $true
        cleanup = 'PASS'
    }
}

function New-DBPHSeedVerifierProof {
    param(
        [Parameter(Mandatory = $true)][int64]$ChangedPaths,
        [Parameter(Mandatory = $true)][int64]$DirectlyBoundChangedPaths,
        [Parameter(Mandatory = $true)][int64]$ManifestEntries,
        [Parameter(Mandatory = $true)][int64]$ChecksumEntries
    )

    return [ordered]@{
        schema_version = $DBPHVerifierProofSchemaVersion
        status = 'PASS'
        source_mode = 'GitIndex'
        revision = 'INDEX'
        product_candidate_sha = $DBPHProductCandidateSHA
        representation_contract = $DBPHRepresentationContract
        changed_paths = $ChangedPaths
        directly_bound_changed_paths = $DirectlyBoundChangedPaths
        manifest_entries = $ManifestEntries
        checksum_entries = $ChecksumEntries
        inventory_call_sites = [int64]83
        inventory_files = [int64]8
        failures = [object[]]@()
    }
}
