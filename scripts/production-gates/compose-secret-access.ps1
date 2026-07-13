Set-StrictMode -Version Latest

function Assert-ComposeSecretPathAccess {
    param(
        [Parameter(Mandatory)][string]$Path,
        [switch]$Directory
    )

    if ($IsWindows) {
        $current = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
        $system = [System.Security.Principal.SecurityIdentifier]::new(
            [System.Security.Principal.WellKnownSidType]::LocalSystemSid,
            $null
        )
        $info = if ($Directory) { [System.IO.DirectoryInfo]::new($Path) } else { [System.IO.FileInfo]::new($Path) }
        $acl = [System.IO.FileSystemAclExtensions]::GetAccessControl($info)
        if (-not $acl.AreAccessRulesProtected) {
            throw "compose secret path still inherits Windows ACLs: $Path"
        }
        $required = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
        [void]$required.Add($current.Value)
        [void]$required.Add($system.Value)
        foreach ($rule in $acl.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier])) {
            $identity = [string]$rule.IdentityReference.Value
            if ($rule.IsInherited -or $rule.AccessControlType -ne [System.Security.AccessControl.AccessControlType]::Allow -or -not $required.Contains($identity)) {
                throw "compose secret path has an unexpected Windows ACL entry for '$identity': $Path"
            }
            if (($rule.FileSystemRights -band [System.Security.AccessControl.FileSystemRights]::FullControl) -ne [System.Security.AccessControl.FileSystemRights]::FullControl) {
                throw "compose secret path ACL is not FullControl for '$identity': $Path"
            }
            [void]$required.Remove($identity)
        }
        if ($required.Count -ne 0) {
            throw "compose secret path ACL lacks owner or LocalSystem access: $Path"
        }
        return
    }

    $expected = if ($Directory) {
        [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite -bor [System.IO.UnixFileMode]::UserExecute
    } else {
        [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite -bor [System.IO.UnixFileMode]::GroupRead -bor [System.IO.UnixFileMode]::OtherRead
    }
    $actual = [System.IO.File]::GetUnixFileMode($Path)
    if ($actual -ne $expected) {
        throw "compose secret path mode is '$actual', expected '$expected': $Path"
    }
}

function Set-ComposeSecretPathAccess {
    param(
        [Parameter(Mandatory)][string]$Path,
        [switch]$Directory
    )

    if (-not (Test-Path -LiteralPath $Path -PathType $(if ($Directory) { 'Container' } else { 'Leaf' }))) {
        throw "compose secret path does not exist: $Path"
    }
    if ($IsWindows) {
        $current = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
        $system = [System.Security.Principal.SecurityIdentifier]::new(
            [System.Security.Principal.WellKnownSidType]::LocalSystemSid,
            $null
        )
        $acl = if ($Directory) { [System.Security.AccessControl.DirectorySecurity]::new() } else { [System.Security.AccessControl.FileSecurity]::new() }
        $acl.SetAccessRuleProtection($true, $false)
        $acl.SetOwner($current)
        foreach ($identity in @($current, $system)) {
            $rule = if ($Directory) {
                [System.Security.AccessControl.FileSystemAccessRule]::new(
                    $identity,
                    [System.Security.AccessControl.FileSystemRights]::FullControl,
                    [System.Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [System.Security.AccessControl.InheritanceFlags]::ObjectInherit,
                    [System.Security.AccessControl.PropagationFlags]::None,
                    [System.Security.AccessControl.AccessControlType]::Allow
                )
            } else {
                [System.Security.AccessControl.FileSystemAccessRule]::new(
                    $identity,
                    [System.Security.AccessControl.FileSystemRights]::FullControl,
                    [System.Security.AccessControl.AccessControlType]::Allow
                )
            }
            [void]$acl.AddAccessRule($rule)
        }
        if ($Directory) {
            [System.IO.FileSystemAclExtensions]::SetAccessControl([System.IO.DirectoryInfo]::new($Path), $acl)
        } else {
            [System.IO.FileSystemAclExtensions]::SetAccessControl([System.IO.FileInfo]::new($Path), $acl)
        }
    } elseif ($Directory) {
        [System.IO.File]::SetUnixFileMode(
            $Path,
            [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite -bor [System.IO.UnixFileMode]::UserExecute
        )
    } else {
        # Compose file-backed secrets are bind mounts: uid/gid/mode remapping is
        # ignored. Keep the parent 0700 on the host, while 0644 lets non-root
        # container UIDs read the exact file through the read-only bind mount.
        [System.IO.File]::SetUnixFileMode(
            $Path,
            [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite -bor [System.IO.UnixFileMode]::GroupRead -bor [System.IO.UnixFileMode]::OtherRead
        )
    }
    Assert-ComposeSecretPathAccess -Path $Path -Directory:$Directory
}
