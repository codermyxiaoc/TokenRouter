# Targeted re-run for the audit fixes (ASCII-only, see verify.ps1). Logs: .agents\verify\logs-audit
$ErrorActionPreference = 'Continue'
$Root = 'C:\Users\ASUS\Desktop\TokenRouter'
$Log = Join-Path $Root '.agents\verify\logs-audit'
New-Item -ItemType Directory -Force -Path $Log | Out-Null
Get-ChildItem -Path $Log -Filter *.log -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
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
# Compile-only check of integration-tagged tests (catches the stale cfg.ProtocolAdapter reference).
Invoke-Step 'vet_integration' $Backend 'go vet -tags=integration ./internal/repository/'
Invoke-Step 'unit_middleware' $Backend 'go test -tags=unit -p 1 -count=1 -timeout 30m ./internal/server/middleware/'
Invoke-Step 'unit_handler' $Backend 'go test -tags=unit -p 1 -count=1 -timeout 40m ./internal/handler/'
Invoke-Step 'unit_repository' $Backend 'go test -tags=unit -p 1 -count=1 -timeout 30m ./internal/repository/'
Invoke-Step 'unit_service' $Backend 'go test -tags=unit -p 1 -count=1 -timeout 40m ./internal/service/'
$env:CGO_ENABLED = '1'
Invoke-Step 'race_image' $Backend 'go test -tags=unit -race -p 1 -count=1 -timeout 30m -run Image ./internal/service/ ./internal/repository/ ./internal/handler/'
Invoke-Step 'race_video' $Backend 'go test -tags=unit -race -p 1 -count=1 -timeout 30m -run Video ./internal/service/'
Invoke-Step 'race_smart_routing' $Backend 'go test -tags=unit -race -p 1 -count=1 -timeout 30m -run SmartRouting ./internal/server/middleware/'
Remove-Item Env:CGO_ENABLED
Invoke-Step 'vue_tsc' $Frontend 'npx vue-tsc --noEmit'
Invoke-Step 'vitest_api_docs' $Frontend 'npx vitest run src/views/admin/settings src/views/__tests__/ApiDocsView.spec.ts'
$done = "done $(Get-Date -Format HH:mm:ss)"
Write-Host $done
Add-Content -Path $Summary -Value $done
