$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$manifestPath = Join-Path $PSScriptRoot 'feature22-coverage-manifest.json'
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
Push-Location $root
try {
  $markers = @{}
  Get-ChildItem -LiteralPath (Join-Path $root 'cmd'), (Join-Path $root 'internal') -Recurse -Filter '*.go' | ForEach-Object {
    $relative = $_.FullName.Substring($root.Length + 1).Replace('\', '/')
    foreach ($line in Get-Content -LiteralPath $_.FullName) {
      if ($line -match '^// feature22:coverage (.+)$') {
        if ($markers.ContainsKey($Matches[1])) { throw "duplicate feature22 coverage marker $($Matches[1])" }
        $markers[$Matches[1]] = $relative
      }
    }
  }
  $targets = @($manifest.targets)
  if ($targets.Count -ne $markers.Count) { throw "coverage manifest has $($targets.Count) targets but source has $($markers.Count) mandatory markers" }
  foreach ($target in $targets) {
    if (-not $markers.ContainsKey($target.name) -or $markers[$target.name] -ne $target.file) { throw "coverage manifest omitted or misplaced mandatory target $($target.name)" }
  }
  foreach ($name in $markers.Keys) {
    if (-not ($targets | Where-Object name -EQ $name)) { throw "coverage manifest omitted mandatory source marker $name" }
  }
  foreach ($property in $manifest.profiles.PSObject.Properties) {
    $profile = $property.Value
    go test -count=1 -coverprofile $profile.profile $profile.package
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  }
  foreach ($target in $targets) {
    $source = Get-Content -LiteralPath $target.file
    $line = 1 + [array]::FindIndex([string[]]$source, [Predicate[string]]{ param($value) $value -match $target.pattern })
    if ($line -le 0) { throw "missing feature22 composition function $($target.name)" }
    $profileName = $manifest.profiles.($target.profile).profile
    $report = go tool cover -func $profileName
    $match = $report | Where-Object { $_ -match ([regex]::Escape($target.file) + ":$line`:") }
    if (-not $match -or $match -notmatch '\s(\d+(?:\.\d+)?)%$') { throw "missing coverage for $($target.name)" }
    if ([double]$Matches[1] -lt 85) { throw "$($target.name) coverage is $($Matches[1])%, expected >=85%" }
  }
} finally {
  foreach ($property in $manifest.profiles.PSObject.Properties) { Remove-Item -LiteralPath (Join-Path $root $property.Value.profile) -Force -ErrorAction SilentlyContinue }
  Pop-Location
}
