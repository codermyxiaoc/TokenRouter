# TokenRouter full verification script (Windows PowerShell, no WSL needed).
# Kept ASCII-only on purpose: Windows PowerShell 5.1 reads BOM-less UTF-8 as the ANSI code page.
# Steps: build, vet, wire consistency, per-package unit tests (-p 1), race on hot paths, frontend checks.
# Logs: .agents\verify\logs, summary in summary.log. This directory is not committed.
$ErrorActionPreference = 'Continue'
$Root = 'C:\Users\ASUS\Desktop\TokenRouter'
$Log = Join-Path $Root '.agents\verify\logs'
New-Item -ItemType Directory -Force -Path $Log | Out-Null
Get-ChildItem -Path $Log -Filter *.log -ErrorAction SilentlyContinue | Remove-Item -Force
$Summary = Join-Path $Log 'summary.log'

# Run one step, write its output to its own log and record PASS/FAIL in the summary.
function Invoke-Step {
    param([string]$Name, [string]$WorkDir, [string]$Command)
    $line = ">>> $Name $(Get-Date -Format HH:mm:ss)"
    Write-Host $line
    Add-Content -Path $Summary -Value $line
    $out = Join-Path $Log "$Name.log"
    Push-Location $WorkDir
    $global:LASTEXITCODE = 0
    Invoke-Expression "$Command *> '$out'"
    $code = $LASTEXITCODE
    Pop-Location
    if ($code -eq 0) {
        $result = "PASS $Name"
    } else {
        $result = "FAIL $Name (exit $code, see $out)"
    }
    Write-Host $result
    Add-Content -Path $Summary -Value $result
}

$Backend = Join-Path $Root 'backend'
$Frontend = Join-Path $Root 'frontend'

Invoke-Step 'build' $Backend 'go build ./...'
Invoke-Step 'vet' $Backend 'go vet ./internal/service/ ./internal/repository/ ./internal/handler/... ./internal/server/... ./internal/config/ ./cmd/server/'

# Wire: regenerate, diff against the hand-edited file, then always restore the original.
$WireFile = Join-Path $Backend 'cmd\server\wire_gen.go'
$WireBackup = Join-Path $env:TEMP 'wire_gen.before.go'
Copy-Item $WireFile $WireBackup -Force
Invoke-Step 'wire_generate' $Backend 'go run github.com/google/wire/cmd/wire ./cmd/server'
Invoke-Step 'wire_diff' $Backend "git diff --no-index --exit-code '$WireBackup' cmd/server/wire_gen.go"
Copy-Item $WireBackup $WireFile -Force

$Packages = @(
    './internal/config/',
    './internal/pkg/...',
    './internal/server/middleware/',
    './internal/server/routes/',
    './internal/server/',
    './internal/handler/admin/',
    './internal/handler/',
    './internal/repository/',
    './internal/service/'
)
foreach ($pkg in $Packages) {
    $name = 'unit_' + ($pkg -replace '[\./]', '_')
    Invoke-Step $name $Backend "go test -tags=unit -p 1 -count=1 -timeout 40m $pkg"
}

# Race detector on the most concurrent changed paths (needs CGO + gcc; failure here does not affect other steps).
$env:CGO_ENABLED = '1'
Invoke-Step 'race_image' $Backend 'go test -tags=unit -race -p 1 -count=1 -timeout 30m -run Image ./internal/service/ ./internal/repository/ ./internal/handler/'
Invoke-Step 'race_smart_routing' $Backend 'go test -tags=unit -race -p 1 -count=1 -timeout 30m -run SmartRouting ./internal/server/middleware/'
Remove-Item Env:CGO_ENABLED

Invoke-Step 'vue_tsc' $Frontend 'npx vue-tsc --noEmit'
Invoke-Step 'vitest' $Frontend 'npx vitest run'

$done = "done $(Get-Date -Format HH:mm:ss)"
Write-Host $done
Add-Content -Path $Summary -Value $done
