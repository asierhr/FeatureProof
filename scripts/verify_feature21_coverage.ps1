param()

# This verifier measures every feature package and the feature-owned functions inside the historical CLI package.
$feature21Root = Split-Path -Parent $PSScriptRoot
$feature21Coverage = Join-Path ([System.IO.Path]::GetTempPath()) ("featureproof-feature21-coverage-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $feature21Coverage | Out-Null

function Invoke-Feature21Coverage {
    param([string]$Package, [string]$Name)
    $profile = Join-Path $feature21Coverage ($Name + ".cover")
    $testOutput = & go test -coverprofile $profile $Package
    if ($LASTEXITCODE -ne 0) { throw "coverage tests failed for $Package" }
    $testOutput | ForEach-Object { Write-Host $_ }
    $rows = & go tool cover -func $profile
    if ($LASTEXITCODE -ne 0) { throw "coverage report failed for $Package" }
    return ,$rows
}

function Assert-Feature21Total {
    param([string[]]$Rows, [string]$Package)
    $total = $Rows | Where-Object { $_ -match '^total:' }
    if ($null -eq $total -or $total -notmatch '(\d+(?:\.\d+)?)%$') {
        throw "$Package coverage total is unavailable: $total"
    }
    $percentage = [double]$Matches[1]
    if ($percentage -lt 85) {
        throw "$Package modified-code coverage is below 85%: $total"
    }
}

function Assert-Feature21Function {
    param([string[]]$Rows, [string]$Name)
    $row = $Rows | Where-Object { $_ -match ("workflow.go:\d+:\s+" + [regex]::Escape($Name) + "\s+") } | Select-Object -First 1
    if ($null -eq $row -or $row -notmatch '(\d+(?:\.\d+)?)%$') {
        throw "$Name coverage is unavailable: $row"
    }
    $percentage = [double]$Matches[1]
    if ($percentage -lt 85) {
        throw "$Name coverage is below 85%: $row"
    }
}

$feature21LocationPushed = $false
try {
    Push-Location $feature21Root
	$feature21LocationPushed = $true
    foreach ($entry in @(
        @{ Package = './internal/contractcatalog'; Name = 'contractcatalog' },
        @{ Package = './internal/openapi'; Name = 'openapi' },
        @{ Package = './internal/agentplanning'; Name = 'agentplanning' },
        @{ Package = './internal/planapproval'; Name = 'planapproval' },
        @{ Package = './internal/replanning'; Name = 'replanning' },
        @{ Package = './internal/validationworkflow'; Name = 'validationworkflow' },
        @{ Package = './internal/agenteval'; Name = 'agenteval' }
    )) {
        $rows = Invoke-Feature21Coverage $entry.Package $entry.Name
        Assert-Feature21Total $rows $entry.Package
        $rows | Select-Object -Last 1
    }
    $cliRows = Invoke-Feature21Coverage './cmd/featureproof' 'featureproof'
    foreach ($functionName in @('policy', 'loadWorkflowConfig', 'composeWorkflowCLI', 'Reduce')) {
        Assert-Feature21Function $cliRows $functionName
        $cliRows | Where-Object { $_ -match ("workflow.go:\d+:\s+" + [regex]::Escape($functionName) + "\s+") }
    }
} catch {
	Write-Error $_
	exit 1
} finally {
	if ($feature21LocationPushed) { Pop-Location }
    Remove-Item -LiteralPath $feature21Coverage -Recurse -Force -ErrorAction SilentlyContinue
}
