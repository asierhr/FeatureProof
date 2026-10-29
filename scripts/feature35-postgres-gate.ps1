[CmdletBinding(DefaultParameterSetName = 'AuthorizedDsn')]
param(
    [Parameter(Mandatory = $true, ParameterSetName = 'AuthorizedDsn')]
    [string]$AdminDsn,

    [Parameter(Mandatory = $true, ParameterSetName = 'BootstrapWsl')]
    [switch]$BootstrapWsl,

    [Parameter(Mandatory = $true)]
    [switch]$AuthorizeIsolatedTestResources,

    [ValidateRange(1, 20)]
    [int]$Repeat = 1,

    [switch]$Race,

    [switch]$Coverage,

    [string]$CCompiler
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# gateRepositoryRoot anchors all versioned commands without trusting the caller's current directory.
$gateRepositoryRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
# gateIdentity names every destructive target with one unpredictable invocation-owned UUID.
$gateIdentity = [guid]::NewGuid().ToString('N')
# gateDatabase is accepted by the helper's closed destructive-name allowlist.
$gateDatabase = "featureproof_gate_$gateIdentity"
# gateOwnershipToken proves this invocation owns the isolated database before cleanup.
$gateOwnershipToken = ([guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N'))
# gateDistro names only the optional WSL distribution created by this invocation.
$gateDistro = "FeatureProofPG35-$gateIdentity"
# gateRoot is the only host directory eligible for recursive teardown.
$gateRoot = Join-Path ([IO.Path]::GetTempPath()) "featureproof-pg35-gate-$gateIdentity"
# gateDatabaseCreated tracks whether ownership-checked database teardown is required.
$gateDatabaseCreated = $false
# gateCoverageDatabase tracks the separately owned clean database used only by coverage.
$gateCoverageDatabase = ''
# gateCoverageOwnershipToken authenticates cleanup of the coverage-only database.
$gateCoverageOwnershipToken = ''
# gateCoverageDatabaseCreated ensures outer cleanup handles an interrupted coverage run.
$gateCoverageDatabaseCreated = $false
# gateWslCreated tracks whether this invocation registered the optional distribution.
$gateWslCreated = $false
# gateAdminDsn remains process-local and is never printed or written to disk.
$gateAdminDsn = $AdminDsn
# gateTargetDsn remains process-local and identifies only the invocation-owned database.
$gateTargetDsn = ''
# gateGoDirective reads the repository's exact toolchain contract instead of using a host default.
$gateGoDirective = [regex]::Match((Get-Content -Raw -LiteralPath (Join-Path $gateRepositoryRoot 'go.mod')), '(?m)^go\s+(\d+\.\d+(?:\.\d+)?)$')
if (-not $gateGoDirective.Success) { throw 'go.mod lacks an exact Go directive' }
# gateToolchain pins every build and test in this invocation to the reviewed Go version.
$gateToolchain = 'go' + $gateGoDirective.Groups[1].Value
# gatePreviousToolchain restores the caller environment after the isolated gate.
$gatePreviousToolchain = $env:GOTOOLCHAIN

# Assert-GateCommand converts a failed child process into a fail-closed gate result.
function Assert-GateCommand {
    param([Parameter(Mandatory = $true)][string]$Description)
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE"
    }
}

# Invoke-GateNative suppresses expected native stderr warnings while preserving the exact process exit code.
function Invoke-GateNative {
    param([Parameter(Mandatory = $true)][scriptblock]$Command, [Parameter(Mandatory = $true)][string]$Description)
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $null = & $Command 2>&1
        $exitCode = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previousPreference }
    if ($exitCode -ne 0) { throw "$Description failed with exit code $exitCode" }
}

# New-TargetDsn replaces only the URI database path while retaining explicit caller connection options.
function New-TargetDsn {
    param([Parameter(Mandatory = $true)][string]$Source, [Parameter(Mandatory = $true)][string]$Database)
    $builder = [UriBuilder]::new($Source)
    if ($builder.Scheme -notin @('postgres', 'postgresql') -or [string]::IsNullOrWhiteSpace($builder.Host)) {
        throw 'AdminDsn must be an explicit PostgreSQL URI'
    }
    $builder.Path = "/$Database"
    return $builder.Uri.AbsoluteUri
}

# Remove-OwnedGateRoot deletes only the exact temporary directory derived from this invocation UUID.
function Remove-OwnedGateRoot {
    if (-not (Test-Path -LiteralPath $gateRoot)) { return }
    $resolvedTemp = (Resolve-Path -LiteralPath ([IO.Path]::GetTempPath())).Path.TrimEnd([IO.Path]::DirectorySeparatorChar)
    $resolvedGate = (Resolve-Path -LiteralPath $gateRoot).Path
    $expectedName = "featureproof-pg35-gate-$gateIdentity"
    if ([IO.Path]::GetDirectoryName($resolvedGate) -ne $resolvedTemp -or [IO.Path]::GetFileName($resolvedGate) -ne $expectedName) {
        throw 'refusing unsafe gate root cleanup'
    }
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        try {
            [IO.Directory]::Delete($resolvedGate, $true)
            return
        }
        catch [UnauthorizedAccessException] {
            if ($attempt -eq 19) { throw }
            Start-Sleep -Milliseconds 250
        }
        catch [IO.IOException] {
            if ($attempt -eq 19) { throw }
            Start-Sleep -Milliseconds 250
        }
    }
}

# Get-GateWslDistros removes the NUL padding emitted by wsl.exe on some Windows hosts before exact-name ownership checks.
function Get-GateWslDistros {
    return @(& wsl.exe --list --quiet 2>$null) | ForEach-Object { $_.Replace(([char]0).ToString(), '').Trim() } | Where-Object { $_ -ne '' }
}

# Get-GateSha256 authenticates one downloaded asset using framework primitives available on Windows PowerShell.
function Get-GateSha256 {
    param([Parameter(Mandatory = $true)][string]$Path)
    $stream = [IO.File]::OpenRead($Path)
    try {
        $algorithm = [Security.Cryptography.SHA256]::Create()
        try { $bytes = $algorithm.ComputeHash($stream) }
        finally { $algorithm.Dispose() }
    }
    finally { $stream.Dispose() }
    return ([BitConverter]::ToString($bytes).Replace('-', '').ToLowerInvariant())
}

# Initialize-WslPostgres creates a checksum-authenticated Alpine/PostgreSQL 16 service owned by this invocation.
function Initialize-WslPostgres {
    $archiveUri = 'https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/x86_64/alpine-minirootfs-3.22.2-x86_64.tar.gz'
    $archiveSha256 = '412454ed98025ad9cd910d13f3d448b184e81502baa83180e89f98d0f13674be'
    $archive = Join-Path $gateRoot 'alpine-minirootfs.tar.gz'
    $install = Join-Path $gateRoot 'wsl'
    [IO.Directory]::CreateDirectory($gateRoot) | Out-Null
    Invoke-WebRequest -Uri $archiveUri -OutFile $archive
    $actualHash = Get-GateSha256 -Path $archive
    if ($actualHash -ne $archiveSha256) { throw 'Alpine rootfs checksum mismatch' }
    $script:gateWslCreated = $true
    Invoke-GateNative -Description 'WSL import' -Command { & wsl.exe --import $gateDistro $install $archive --version 1 }
    Invoke-GateNative -Description 'PostgreSQL 16 installation' -Command { & wsl.exe -d $gateDistro -- sh -lc 'apk add --no-cache postgresql16=16.15-r0 postgresql16-client=16.15-r0' }
    Invoke-GateNative -Description 'PostgreSQL cluster initialization' -Command { & wsl.exe -d $gateDistro -- sh -lc "mkdir -p /var/lib/postgresql/16/data /run/postgresql && chown -R postgres:postgres /var/lib/postgresql /run/postgresql && su postgres -c '/usr/libexec/postgresql16/initdb -D /var/lib/postgresql/16/data --auth=trust --encoding=UTF8'" }
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    Invoke-GateNative -Description 'PostgreSQL startup' -Command { & wsl.exe -d $gateDistro -- su postgres -c "/usr/libexec/postgresql16/pg_ctl -D /var/lib/postgresql/16/data -o '-h 127.0.0.1 -p $port' -l /tmp/postgres.log start" }
    $ready = $false
    for ($attempt = 1; $attempt -le 30; $attempt++) {
        $previousPreference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        $null = & wsl.exe -d $gateDistro -- su postgres -c "/usr/libexec/postgresql16/pg_isready -h 127.0.0.1 -p $port" 2>&1
        $readyExit = $LASTEXITCODE
        $ErrorActionPreference = $previousPreference
        if ($readyExit -eq 0) { $ready = $true; break }
        Start-Sleep -Milliseconds 200
    }
    if (-not $ready) { throw 'PostgreSQL readiness deadline exceeded' }
    return "postgres://postgres@127.0.0.1:$port/postgres?sslmode=disable"
}

# ConvertTo-GateWslPath maps one absolute host file into WSL's fixed drive mount without shell parsing.
function ConvertTo-GateWslPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    $absolute = [IO.Path]::GetFullPath($Path)
    $root = [IO.Path]::GetPathRoot($absolute)
    if ($root.Length -ne 3 -or $root[1] -ne ':') { throw 'Linux runtime requires a drive-qualified host path' }
    $drive = [char]::ToLowerInvariant($root[0])
    $remainder = $absolute.Substring($root.Length).Replace('\', '/')
    return "/mnt/$drive/$remainder"
}

# Invoke-GateLinuxRuntime cross-compiles and executes neutral and tagged test binaries inside the owned Alpine runtime.
function Invoke-GateLinuxRuntime {
    $unitBinary = Join-Path $gateRoot 'controlplane-linux-amd64'
    $postgresBinary = Join-Path $gateRoot 'controlplane-postgres-linux-amd64'
    $previousGOOS = $env:GOOS
    $previousGOARCH = $env:GOARCH
    $previousCGO = $env:CGO_ENABLED
    try {
        $env:GOOS = 'linux'
        $env:GOARCH = 'amd64'
        $env:CGO_ENABLED = '0'
        & go test -c -o $unitBinary ./internal/controlplane
        Assert-GateCommand 'Linux control-plane test build'
        & go test -c -tags=postgres_integration -o $postgresBinary ./internal/controlplane/postgres
        Assert-GateCommand 'Linux PostgreSQL test build'
    }
    finally {
        $env:GOOS = $previousGOOS
        $env:GOARCH = $previousGOARCH
        $env:CGO_ENABLED = $previousCGO
    }
    $unitWsl = ConvertTo-GateWslPath -Path $unitBinary
    $postgresWsl = ConvertTo-GateWslPath -Path $postgresBinary
    Invoke-GateNative -Description 'Linux control-plane runtime' -Command { & wsl.exe -d $gateDistro -- $unitWsl '-test.count=1' }
    Invoke-GateNative -Description 'Linux PostgreSQL runtime' -Command { & wsl.exe -d $gateDistro -- env "FEATUREPROOF_POSTGRES_TEST_DSN=$gateTargetDsn" $postgresWsl '-test.count=1' }
}

if (-not $AuthorizeIsolatedTestResources) {
    throw 'AuthorizeIsolatedTestResources is mandatory; the gate never infers a target'
}

Push-Location $gateRepositoryRoot
try {
    $env:GOTOOLCHAIN = $gateToolchain
    $actualToolchain = (& go env GOVERSION).Trim()
    Assert-GateCommand 'Go toolchain resolution'
    if ($actualToolchain -ne $gateToolchain) { throw "required toolchain $gateToolchain, got $actualToolchain" }
    if ($BootstrapWsl) {
        $gateAdminDsn = Initialize-WslPostgres
    }
    if ([string]::IsNullOrWhiteSpace($gateAdminDsn)) {
        throw 'an explicit AdminDsn or BootstrapWsl is required'
    }
    $gateTargetDsn = New-TargetDsn -Source $gateAdminDsn -Database $gateDatabase
    $env:FEATUREPROOF_POSTGRES_ADMIN_DSN = $gateAdminDsn
    $env:FEATUREPROOF_POSTGRES_TEST_DSN = $gateTargetDsn
    & go run ./cmd/featureproof-postgres-gate create $gateDatabase $gateOwnershipToken
    Assert-GateCommand 'owned database create'
    $gateDatabaseCreated = $true
    & go run ./cmd/featureproof-postgres-gate verify $gateDatabase $gateOwnershipToken
    Assert-GateCommand 'owned database verify'
    # gateCompatN and gateCompatN1 are separately compiled artifacts with adjacent embedded migration manifests.
    $gateCompatN = Join-Path $gateRoot 'featureproof-postgres-compat-n.exe'
    $gateCompatN1 = Join-Path $gateRoot 'featureproof-postgres-compat-n1.exe'
    [IO.Directory]::CreateDirectory($gateRoot) | Out-Null
    & go build -tags=postgres_schema_n -o $gateCompatN ./cmd/featureproof-postgres-compat
    Assert-GateCommand 'compatibility artifact N build'
    & go build -o $gateCompatN1 ./cmd/featureproof-postgres-compat
    Assert-GateCommand 'compatibility artifact N+1 build'
    $env:FEATUREPROOF_POSTGRES_GATE_AUTHORIZED = 'true'
    & $gateCompatN migrate
    Assert-GateCommand 'compatibility artifact N migrate and ready'
    & $gateCompatN1 migrate
    Assert-GateCommand 'compatibility artifact N+1 migrate and ready'
    & $gateCompatN ready
    Assert-GateCommand 'compatibility artifact N rollback ready'
    & $gateCompatN1 ready
    Assert-GateCommand 'compatibility artifact N+1 ready'
    & go test -p=1 -tags=postgres_integration ("-count={0}" -f $Repeat) ./cmd/featureproof ./internal/controlplane/postgres ./internal/planapproval
    Assert-GateCommand 'PostgreSQL integration matrix'
    if ($BootstrapWsl) {
        Invoke-GateLinuxRuntime
    }
    if ($Race) {
        if ([string]::IsNullOrWhiteSpace($CCompiler) -or -not (Test-Path -LiteralPath $CCompiler)) {
            throw 'Race requires an explicit existing CCompiler path'
        }
        $previousCGO = $env:CGO_ENABLED
        $previousCC = $env:CC
        $previousPath = $env:PATH
        try {
            $env:CGO_ENABLED = '1'
            $env:CC = $CCompiler
            $env:PATH = ([IO.Path]::GetDirectoryName($CCompiler) + [IO.Path]::PathSeparator + $env:PATH)
            & go test -p=1 -race -tags=postgres_integration -count=1 ./cmd/featureproof ./internal/controlplane/postgres ./internal/planapproval
            Assert-GateCommand 'PostgreSQL race matrix'
        }
        finally {
            $env:CGO_ENABLED = $previousCGO
            $env:CC = $previousCC
            $env:PATH = $previousPath
        }
    }
    if ($Coverage) {
        # gateCoverageSignatures compares complete block truth across independently created databases.
        $gateCoverageSignatures = @()
        foreach ($gateCoverageRun in 1..2) {
            # gateCoverageIdentity gives this measurement a database distinct from both normal tests and its peer.
            $gateCoverageIdentity = [guid]::NewGuid().ToString('N')
            $gateCoverageDatabase = "featureproof_gate_$gateCoverageIdentity"
            $gateCoverageOwnershipToken = ([guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N'))
            # gateCoverageDsn is exposed only to the migration and coverage children for this owned database.
            $gateCoverageDsn = New-TargetDsn -Source $gateAdminDsn -Database $gateCoverageDatabase
            # gateCoverageSignature stays inside the UUID-owned root so teardown cannot leave a workspace profile.
            $gateCoverageSignature = Join-Path $gateRoot "coverage-signature-$gateCoverageRun.json"
            try {
                $env:FEATUREPROOF_POSTGRES_TEST_DSN = $gateCoverageDsn
                $env:FEATUREPROOF_COVERAGE_SIGNATURE_PATH = $gateCoverageSignature
                & go run ./cmd/featureproof-postgres-gate create $gateCoverageDatabase $gateCoverageOwnershipToken
                Assert-GateCommand 'owned coverage database create'
                $gateCoverageDatabaseCreated = $true
                & $gateCompatN migrate
                Assert-GateCommand 'coverage compatibility artifact N migrate'
                & $gateCompatN1 migrate
                Assert-GateCommand 'coverage compatibility artifact N+1 migrate'
                & python ./scripts/feature35_coverage.py
                Assert-GateCommand 'feature 35 differential coverage'
                $gateCoverageSignatures += $gateCoverageSignature
            }
            finally {
                Remove-Item Env:FEATUREPROOF_COVERAGE_SIGNATURE_PATH -ErrorAction SilentlyContinue
                if ($gateCoverageDatabaseCreated) {
                    $env:FEATUREPROOF_POSTGRES_TEST_DSN = $gateCoverageDsn
                    & go run ./cmd/featureproof-postgres-gate drop $gateCoverageDatabase $gateCoverageOwnershipToken
                    if ($LASTEXITCODE -ne 0) { throw 'owned coverage database cleanup failed' }
                    $gateCoverageDatabaseCreated = $false
                }
                $env:FEATUREPROOF_POSTGRES_TEST_DSN = $gateTargetDsn
            }
        }
        # gateCoverageSignatureHashes make per-statement drift fail even when aggregate percentages coincide.
        $gateCoverageSignatureHashes = @($gateCoverageSignatures | ForEach-Object { (Get-FileHash -Algorithm SHA256 -LiteralPath $_).Hash })
        if ($gateCoverageSignatureHashes[0] -ne $gateCoverageSignatureHashes[1]) {
            throw 'independent coverage databases produced different changed-statement signatures'
        }
    }
}
finally {
    $cleanupFailed = $false
    if ($gateCoverageDatabaseCreated) {
        $env:FEATUREPROOF_POSTGRES_TEST_DSN = $gateCoverageDsn
        & go run ./cmd/featureproof-postgres-gate drop $gateCoverageDatabase $gateCoverageOwnershipToken
        if ($LASTEXITCODE -ne 0) { $cleanupFailed = $true }
        $env:FEATUREPROOF_POSTGRES_TEST_DSN = $gateTargetDsn
    }
    if ($gateDatabaseCreated) {
        & go run ./cmd/featureproof-postgres-gate drop $gateDatabase $gateOwnershipToken
        if ($LASTEXITCODE -ne 0) { $cleanupFailed = $true }
    }
    Remove-Item Env:FEATUREPROOF_POSTGRES_ADMIN_DSN -ErrorAction SilentlyContinue
    Remove-Item Env:FEATUREPROOF_POSTGRES_TEST_DSN -ErrorAction SilentlyContinue
    Remove-Item Env:FEATUREPROOF_COVERAGE_SIGNATURE_PATH -ErrorAction SilentlyContinue
    Remove-Item Env:FEATUREPROOF_POSTGRES_GATE_AUTHORIZED -ErrorAction SilentlyContinue
    $env:GOTOOLCHAIN = $gatePreviousToolchain
    if ($gateWslCreated) {
        $previousPreference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        $null = & wsl.exe -d $gateDistro -- sh -lc "if test -x /usr/libexec/postgresql16/pg_ctl; then su postgres -c '/usr/libexec/postgresql16/pg_ctl -D /var/lib/postgresql/16/data stop -m fast'; fi" 2>&1
        $null = & wsl.exe --terminate $gateDistro 2>&1
        # unregister can race WSL shutdown; retry only while the exact invocation-owned
        # name is still registered and never broaden cleanup to another distribution.
        for ($attempt = 0; $attempt -lt 20; $attempt++) {
            $registered = (Get-GateWslDistros) -contains $gateDistro
            if (-not $registered) { break }
            $null = & wsl.exe --unregister $gateDistro 2>&1
            Start-Sleep -Milliseconds 500
        }
        $registered = (Get-GateWslDistros) -contains $gateDistro
        if ($registered) { $cleanupFailed = $true }
        $ErrorActionPreference = $previousPreference
    }
    Remove-OwnedGateRoot
    Pop-Location
    if ($cleanupFailed) { throw 'owned database cleanup failed' }
}
