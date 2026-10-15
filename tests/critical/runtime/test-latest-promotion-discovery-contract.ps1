param(

  [Parameter(Mandatory)][string]$RepositoryRoot,

  [Parameter(Mandatory)][string]$ArtifactRoot

)



$ErrorActionPreference = 'Stop'

Set-StrictMode -Version Latest



function Assert-That([bool]$Condition, [string]$Message)

{

  if (-not $Condition)

  { throw $Message 

  }

}



# Retained input: recovery 38705632156, skipped promotion 38705625670, no check runs.

$env:REPOSITORY_NAME = 'thebtf/engram'

$env:ORIGINAL_RUN_ID = '38705625670'

$env:ORIGINAL_RUN_ATTEMPT = '1'

$env:ORIGINAL_HEAD_SHA = '40ede6e004caa6f3f72923cf1ea360b27434275a'

$env:GITHUB_SERVER_URL = 'https://github.com'

$env:GITHUB_REPOSITORY = $env:REPOSITORY_NAME

$global:response = '{"total_count":0,"check_runs":[]}'

$global:apiExit = 0




function global:gh

{

  param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)

  $request = $Arguments -join ' '

  $expected = "repos/$env:REPOSITORY_NAME/commits/$env:ORIGINAL_HEAD_SHA/check-runs?check_name=latest-promotion-journal&filter=all&per_page=100"

  Assert-That ($Arguments[0] -ceq 'api' -and $Arguments[-1] -ceq $expected -and '--method' -cnotin $Arguments) "unexpected or mutating GitHub request: $request"


  $global:LASTEXITCODE = $global:apiExit

  return $global:response

}



function global:docker

{

  throw 'discovery must not login to or mutate the registry'

}



$workflow = [System.IO.File]::ReadAllText((Join-Path $RepositoryRoot '.github/workflows/recover-latest-promotion-journal.yml'))

$match = [regex]::Match($workflow, '(?ms)^      - name: Discover owned promotion journal\r?\n.*?        run: \|\r?\n(?<body>.*?)(?=\r?\n      - name: Login to GHCR for owned rollback only)')

Assert-That $match.Success 'could not locate the actual workflow discovery entrypoint'

$entrypoint = [scriptblock]::Create(($match.Groups['body'].Value -replace '(?m)^          ', ''))

New-Item -ItemType Directory -Path $ArtifactRoot -Force | Out-Null

$env:GITHUB_OUTPUT = Join-Path $ArtifactRoot 'discovery-output.txt'



function Invoke-Discovery

{

  Set-Content -LiteralPath $env:GITHUB_OUTPUT -Value '' -NoNewline

  & $entrypoint

  return [System.IO.File]::ReadAllText($env:GITHUB_OUTPUT).TrimEnd("`r", "`n")

}



function Expect-Failure([scriptblock]$Action, [string]$Expected)

{

  $failure = $null

  try

  { & $Action | Out-Null 

  } catch

  { $failure = $_.Exception.Message 

  }

  Assert-That ($null -ne $failure -and $failure.Contains($Expected)) "expected '$Expected', got '$failure'"

}



Push-Location $RepositoryRoot

try

{

  Assert-That ((Invoke-Discovery) -ceq 'journal_id=') 'missing journal must produce an explicit empty output'

  $gate = Join-Path $RepositoryRoot 'scripts/production-gates/latest-promotion-journal.ps1'

  Expect-Failure { & $gate -Mode Discover -RepositoryName $env:REPOSITORY_NAME -RunId $env:ORIGINAL_RUN_ID -RunAttempt $env:ORIGINAL_RUN_ATTEMPT -HeadSha $env:ORIGINAL_HEAD_SHA } 'latest-promotion journal was not found'

  & $gate -Mode Reconcile -RepositoryName $env:REPOSITORY_NAME -RunId $env:ORIGINAL_RUN_ID -RunAttempt $env:ORIGINAL_RUN_ATTEMPT -HeadSha $env:ORIGINAL_HEAD_SHA -AllowMissingJournal



  $owned = [ordered]@{

    id = '41'

    name = 'latest-promotion-journal'

    head_sha = $env:ORIGINAL_HEAD_SHA

    external_id = "latest-promotion:$($env:ORIGINAL_RUN_ID):$($env:ORIGINAL_RUN_ATTEMPT)"

    details_url = "https://github.com/thebtf/engram/actions/runs/$env:ORIGINAL_RUN_ID"

  }

  $global:response = @{ check_runs = @($owned) } | ConvertTo-Json -Compress -Depth 5

  Assert-That ((Invoke-Discovery) -ceq 'journal_id=41') 'owned journal must reach the existing reconciliation condition'



  foreach ($field in @('external_id', 'head_sha'))

  {

    $foreign = [ordered]@{} + $owned

    $foreign[$field] = if ($field -ceq 'head_sha')

    { '1' * 40 

    } else

    { 'latest-promotion:38705625670:2' 

    }

    $global:response = @{ check_runs = @($foreign) } | ConvertTo-Json -Compress -Depth 5

    Assert-That ((Invoke-Discovery) -ceq 'journal_id=') "foreign $field must not authorize recovery"

  }



  $duplicate = [ordered]@{} + $owned

  $duplicate.id = '42'

  $global:response = @{ check_runs = @($owned, $duplicate) } | ConvertTo-Json -Compress -Depth 5

  Expect-Failure { Invoke-Discovery } 'discovery found duplicates: 2'

  Assert-That ([System.IO.File]::ReadAllText($env:GITHUB_OUTPUT) -ceq '') 'duplicate journals must not publish a successful workflow output'



  $global:response = '{malformed'

  Expect-Failure { Invoke-Discovery } 'could not discover latest-promotion journal check run: invalid JSON'

  Assert-That ([System.IO.File]::ReadAllText($env:GITHUB_OUTPUT) -ceq '') 'malformed API response must not publish a successful workflow output'



  $global:apiExit = 1

  $global:response = 'API permission or transport failure'

  Expect-Failure { Invoke-Discovery } 'could not discover latest-promotion journal check run'

  Assert-That ([System.IO.File]::ReadAllText($env:GITHUB_OUTPUT) -ceq '') 'API failure must not publish a successful workflow output'


  'PASS: latest-promotion discovery; scenarios=9; no journal or registry mutations'

} finally

{

  Pop-Location

}

